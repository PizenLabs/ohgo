// Package recovery owns OHGO's reversible edit history.
//
// A session's conversation and its workspace edit history are different
// records. This package owns the second one: it captures the exact workspace
// state before and after each completed agent mutation, keeps both states
// durably, and restores them only when the live workspace is proven to be the
// state the recorded transition expects.
//
// Three invariants govern everything here:
//
//   - A checkpoint is captured before a mutation is authorized, never after.
//   - A published checkpoint is immutable; later operations append transitions.
//   - A restore is reported successful only after the workspace is re-scanned
//     and compared against the target state.
//
// When any of those cannot be established the operation is refused. Blocking is
// always preferable to guessing, and preserving user state is always preferable
// to completing a recovery.
package recovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/PizenLabs/ohgo/internal/intelligence/ignore"
)

// EntryKind is what a workspace path is.
type EntryKind string

// Entry kinds. Only these can be captured and restored.
const (
	// KindFile is a regular file.
	KindFile EntryKind = "file"
	// KindDir is a directory.
	KindDir EntryKind = "dir"
	// KindSymlink is a symbolic link.
	KindSymlink EntryKind = "symlink"
)

// Entry is one captured workspace path.
type Entry struct {
	// Path is slash-relative to the workspace root.
	Path string `json:"path"`
	// Kind is what the path is.
	Kind EntryKind `json:"kind"`
	// Mode is the permission bits, so an executable bit survives a restore.
	Mode uint32 `json:"mode"`
	// Hash is the SHA-256 of a file's content, or of a symlink's target.
	// Directories carry the empty hash.
	Hash string `json:"hash,omitempty"`
	// Size is the file size in bytes, or zero for a directory.
	Size int64 `json:"size,omitempty"`
}

// Tree is an immutable capture of a workspace's captured paths.
//
// Entries are sorted by path, which makes the encoding deterministic and the
// tree hash meaningful. Paths outside the capture scope — .git, the ohgo state
// directory, and anything the repository's own ignore rules exclude — are
// absent by construction, and recovery never touches them.
type Tree struct {
	// Entries are the captured paths in ascending path order.
	Entries []Entry `json:"entries"`
	// Skipped names paths that exist but could not be captured (oversized
	// files, unsupported node types). They are excluded from the tree and the
	// hash; a checkpoint carrying any records the gap as metadata so a restore
	// can state what it did not cover.
	Skipped []string `json:"-"`
}

// Hash returns the content identity of the tree.
//
// Two workspaces with the same hash have the same captured paths with the same
// kinds, modes, and content. It is the value verification compares.
func (t *Tree) Hash() string {
	if t == nil {
		return emptyTreeHash
	}
	return hashEntries(t.Entries)
}

// hashEntries computes the canonical identity of an entry list.
func hashEntries(entries []Entry) string {
	data, err := json.Marshal(entries)
	if err != nil {
		// Entries are plain data with no custom marshallers, so this is not
		// reachable; failing closed beats returning a hash of nothing.
		panic(fmt.Sprintf("recovery: encode tree: %v", err))
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// emptyTreeHash is the identity of an empty workspace capture.
var emptyTreeHash = hashEntries(nil)

// Lookup returns the entry for path, if captured.
func (t *Tree) Lookup(path string) (Entry, bool) {
	if t == nil {
		return Entry{}, false
	}
	index := sort.Search(len(t.Entries), func(i int) bool { return t.Entries[i].Path >= path })
	if index < len(t.Entries) && t.Entries[index].Path == path {
		return t.Entries[index], true
	}
	return Entry{}, false
}

// ChangeStatus is the direction of one changed path.
type ChangeStatus string

// Change directions, using the conventional short form.
const (
	// ChangeAdded is a path the edit created.
	ChangeAdded ChangeStatus = "A"
	// ChangeModified is a path the edit changed.
	ChangeModified ChangeStatus = "M"
	// ChangeDeleted is a path the edit removed.
	ChangeDeleted ChangeStatus = "D"
)

// Change is one path that differs between two trees.
type Change struct {
	// Path is slash-relative to the workspace root.
	Path string `json:"path"`
	// Status is what happened to it.
	Status ChangeStatus `json:"status"`
}

// equalEntry reports whether two captures describe the same path.
func equalEntry(a, b Entry) bool {
	return a.Kind == b.Kind && a.Mode == b.Mode && a.Hash == b.Hash
}

// Diff returns the changes that turn before into after.
//
// Both inputs must be sorted by path, which Scan guarantees. The result is
// sorted by path as well.
func Diff(before, after *Tree) []Change {
	var changes []Change
	beforeEntries := entriesOf(before)
	afterEntries := entriesOf(after)

	index := 0
	for _, b := range beforeEntries {
		for index < len(afterEntries) && afterEntries[index].Path < b.Path {
			changes = append(changes, Change{Path: afterEntries[index].Path, Status: ChangeAdded})
			index++
		}
		if index >= len(afterEntries) || afterEntries[index].Path != b.Path {
			changes = append(changes, Change{Path: b.Path, Status: ChangeDeleted})
			continue
		}
		if !equalEntry(b, afterEntries[index]) {
			changes = append(changes, Change{Path: b.Path, Status: ChangeModified})
		}
		index++
	}
	for ; index < len(afterEntries); index++ {
		changes = append(changes, Change{Path: afterEntries[index].Path, Status: ChangeAdded})
	}
	return changes
}

// entriesOf returns a tree's entries without allocating for the empty case.
func entriesOf(t *Tree) []Entry {
	if t == nil {
		return nil
	}
	return t.Entries
}

// DifferentPaths returns up to limit paths that differ between two trees.
//
// It is used to explain a blocked recovery: the user needs to see that a file
// changed outside ohgo, not merely that something did.
func DifferentPaths(expected, actual *Tree, limit int) []Change {
	changes := Diff(expected, actual)
	if limit > 0 && len(changes) > limit {
		changes = changes[:limit]
	}
	return changes
}

// ScanOptions bounds a workspace capture.
type ScanOptions struct {
	// MaxBlobBytes is the largest file that may be captured. A larger file is
	// skipped and named in Tree.Skipped rather than silently truncated.
	MaxBlobBytes int64
}

// DefaultMaxBlobBytes is the capture limit for one file.
//
// It is deliberately generous: the cap exists to keep a stray disk image or
// vendored media from turning every capture into a copy of the repository, not
// to make ordinary source files uncapturable.
const DefaultMaxBlobBytes = 64 << 20

// Scanner captures a workspace tree with an incremental hash cache.
//
// A scan is the expensive part of a checkpoint, so unchanged files are not
// re-read while the process lives: a (size, mtime, mode) match reuses the
// previously computed hash. A cold cache reads the tree once, which is the
// price of being able to prove what the workspace contained.
type Scanner struct {
	root    string
	matcher *ignore.Matcher
	maxBlob int64

	mu    sync.Mutex
	cache map[string]cachedEntry
}

// cachedEntry is a hash reuse record for one path.
type cachedEntry struct {
	kind EntryKind
	mode uint32
	size int64
	mod  int64
	hash string
}

// NewScanner returns a scanner rooted at root.
func NewScanner(root string) *Scanner {
	return &Scanner{
		root:    root,
		matcher: ignore.New(root, true),
		maxBlob: DefaultMaxBlobBytes,
		cache:   map[string]cachedEntry{},
	}
}

// Root returns the scanner's workspace root.
func (s *Scanner) Root() string { return s.root }

// BlobSink receives captured file content.
//
// Want reports whether content is needed for a hash: a checkpoint capture needs
// it once per unique blob, while a verification scan needs none. Put stores it.
type BlobSink interface {
	Want(hash string) bool
	Put(hash string, data []byte) error
}

// Scan captures the workspace.
//
// sink may be nil for a verification-only scan. A context cancellation stops
// the walk and returns the error rather than a partial tree: a half-captured
// tree that looked complete would be exactly the false evidence this package
// exists to prevent.
func (s *Scanner) Scan(ctx context.Context, sink BlobSink) (*Tree, error) {
	// Ignore rules are part of the workspace's current definition, so they are
	// reloaded per capture. A .gitignore the agent wrote this turn must apply
	// to this turn's checkpoint, not the next session's.
	s.mu.Lock()
	s.matcher = ignore.New(s.root, true)
	matcher := s.matcher
	s.mu.Unlock()

	tree := &Tree{Entries: make([]Entry, 0, 256)}

	err := filepath.WalkDir(s.root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if path == s.root {
				return walkErr
			}
			// A vanished or unreadable path is reported as a skip: it cannot
			// be captured, and pretending it is absent would misrepresent the
			// workspace.
			tree.Skipped = append(tree.Skipped, s.relative(path))
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == s.root {
			return nil
		}
		rel := s.relative(path)
		name := d.Name()
		if d.IsDir() {
			// VCS metadata and ohgo's own state are outside the workspace
			// boundary: checkpointing them would be both enormous and wrong.
			if name == ".git" || name == ".ohgo" {
				return filepath.SkipDir
			}
			if matcher.Ignored(rel, true) {
				return filepath.SkipDir
			}
			info, infoErr := d.Info()
			if infoErr != nil {
				tree.Skipped = append(tree.Skipped, rel)
				return nil
			}
			tree.Entries = append(tree.Entries, Entry{
				Path: rel, Kind: KindDir, Mode: uint32(info.Mode().Perm()),
			})
			return nil
		}
		if matcher.Ignored(rel, false) {
			return nil
		}

		info, infoErr := d.Info()
		if infoErr != nil {
			tree.Skipped = append(tree.Skipped, rel)
			return nil
		}
		mode := info.Mode()
		switch {
		case mode&fs.ModeSymlink != 0:
			entry, ok, entryErr := s.captureSymlink(rel, path, info, sink)
			if entryErr != nil {
				return fmt.Errorf("recovery: read link %s: %w", rel, entryErr)
			}
			if !ok {
				tree.Skipped = append(tree.Skipped, rel)
				return nil
			}
			tree.Entries = append(tree.Entries, entry)
		case mode.IsRegular():
			entry, ok, entryErr := s.captureFile(rel, path, info, sink)
			if entryErr != nil {
				return fmt.Errorf("recovery: read %s: %w", rel, entryErr)
			}
			if !ok {
				tree.Skipped = append(tree.Skipped, rel)
				return nil
			}
			tree.Entries = append(tree.Entries, entry)
		default:
			// Sockets, devices, and named pipes cannot be restored as files.
			tree.Skipped = append(tree.Skipped, rel)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(tree.Entries, func(i, j int) bool { return tree.Entries[i].Path < tree.Entries[j].Path })
	return tree, nil
}

// relative renders a path relative to the root, slash-separated.
func (s *Scanner) relative(path string) string {
	rel, err := filepath.Rel(s.root, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
}

// captureSymlink records a link by its target, so a restore can recreate it.
func (s *Scanner) captureSymlink(rel, path string, info fs.FileInfo, sink BlobSink) (Entry, bool, error) {
	target, err := os.Readlink(path)
	if err != nil {
		return Entry{}, false, nil
	}
	sum := sha256.Sum256([]byte(target))
	hash := hex.EncodeToString(sum[:])
	if sink != nil && sink.Want(hash) {
		if err := sink.Put(hash, []byte(target)); err != nil {
			return Entry{}, false, err
		}
	}
	return Entry{
		Path: rel, Kind: KindSymlink, Mode: uint32(info.Mode().Perm()), Hash: hash,
	}, true, nil
}

// captureFile records one regular file, reusing a cached hash when the file has
// not changed since it was last read.
func (s *Scanner) captureFile(rel, path string, info fs.FileInfo, sink BlobSink) (Entry, bool, error) {
	size := info.Size()
	if s.maxBlob > 0 && size > s.maxBlob {
		return Entry{}, false, nil
	}
	mode := uint32(info.Mode().Perm())
	mod := info.ModTime().UnixNano()

	s.mu.Lock()
	cached, ok := s.cache[rel]
	s.mu.Unlock()
	if ok && cached.kind == KindFile && cached.size == size && cached.mod == mod && cached.mode == mode {
		if sink == nil || !sink.Want(cached.hash) {
			return Entry{Path: rel, Kind: KindFile, Mode: mode, Hash: cached.hash, Size: size}, true, nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return Entry{}, false, err
		}
		sum := sha256.Sum256(data)
		hash := hex.EncodeToString(sum[:])
		if hash != cached.hash {
			// The file changed without its stat signature changing (a coarse
			// mtime clock, for example). The content is authoritative.
			cached.hash = hash
			s.remember(rel, cached)
		}
		if err := sink.Put(hash, data); err != nil {
			return Entry{}, false, err
		}
		return Entry{Path: rel, Kind: KindFile, Mode: mode, Hash: hash, Size: size}, true, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Entry{}, false, nil
		}
		return Entry{}, false, err
	}
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	s.remember(rel, cachedEntry{kind: KindFile, mode: mode, size: size, mod: mod, hash: hash})
	if sink != nil && sink.Want(hash) {
		if err := sink.Put(hash, data); err != nil {
			return Entry{}, false, err
		}
	}
	return Entry{Path: rel, Kind: KindFile, Mode: mode, Hash: hash, Size: int64(len(data))}, true, nil
}

// remember stores a cache entry.
func (s *Scanner) remember(rel string, entry cachedEntry) {
	s.mu.Lock()
	s.cache[rel] = entry
	s.mu.Unlock()
}

// Forget drops a cache entry, so the next scan re-reads the path.
func (s *Scanner) Forget(paths ...string) {
	s.mu.Lock()
	for _, path := range paths {
		delete(s.cache, filepath.ToSlash(path))
	}
	s.mu.Unlock()
}

// BlobReader resolves captured content for a restore.
type BlobReader interface {
	// Open returns a reader for the blob with the given hash.
	Open(hash string) (io.ReadCloser, int64, error)
}

// ErrMissingBlob means a checkpoint references content the store does not hold.
var ErrMissingBlob = errors.New("recovery: checkpoint blob is missing")

// Apply makes the workspace match target, touching only paths that differ from
// current.
//
// The caller must already have proven that current is the state the transition
// expects. Apply itself performs no authority decision: it is the execution
// primitive, and the controller is the only caller.
func Apply(ctx context.Context, root string, current, target *Tree, blobs BlobReader) error {
	changes := Diff(current, target)

	// Deletions go deepest-first so a directory is empty by the time its own
	// removal is attempted.
	deletions := make([]Change, 0, len(changes))
	for _, change := range changes {
		if change.Status == ChangeDeleted {
			deletions = append(deletions, change)
		}
	}
	sort.Slice(deletions, func(i, j int) bool {
		depthI := strings.Count(deletions[i].Path, "/")
		depthJ := strings.Count(deletions[j].Path, "/")
		if depthI != depthJ {
			return depthI > depthJ
		}
		return deletions[i].Path > deletions[j].Path
	})
	for _, change := range deletions {
		if err := ctx.Err(); err != nil {
			return err
		}
		entry, ok := current.Lookup(change.Path)
		if !ok {
			continue
		}
		if err := removePath(root, entry); err != nil {
			return err
		}
	}

	// Additions and modifications go shallow-first so a parent directory
	// exists before its children are materialised.
	writes := make([]Entry, 0, len(changes))
	for _, change := range changes {
		if change.Status == ChangeDeleted {
			continue
		}
		entry, ok := target.Lookup(change.Path)
		if !ok {
			continue
		}
		writes = append(writes, entry)
	}
	sort.Slice(writes, func(i, j int) bool { return writes[i].Path < writes[j].Path })
	for _, entry := range writes {
		if err := ctx.Err(); err != nil {
			return err
		}
		// A path whose type changed (a file replaced by a directory, or the
		// reverse) must be removed first: the writer cannot install one kind
		// over the other.
		if existing, ok := current.Lookup(entry.Path); ok && existing.Kind != entry.Kind {
			if err := removePath(root, existing); err != nil {
				return err
			}
		}
		if err := writePath(root, entry, blobs); err != nil {
			return err
		}
	}
	return nil
}

// removePath removes one captured path.
func removePath(root string, entry Entry) error {
	path := filepath.Join(root, filepath.FromSlash(entry.Path))
	if entry.Kind == KindDir {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("recovery: remove directory %s: %w", entry.Path, err)
		}
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("recovery: remove %s: %w", entry.Path, err)
	}
	return nil
}

// writePath materialises one captured path.
func writePath(root string, entry Entry, blobs BlobReader) error {
	path := filepath.Join(root, filepath.FromSlash(entry.Path))
	switch entry.Kind {
	case KindDir:
		if err := os.MkdirAll(path, fs.FileMode(entry.Mode)); err != nil {
			return fmt.Errorf("recovery: create directory %s: %w", entry.Path, err)
		}
		if err := os.Chmod(path, fs.FileMode(entry.Mode)); err != nil {
			return fmt.Errorf("recovery: set mode on %s: %w", entry.Path, err)
		}
		return nil
	case KindSymlink:
		target, err := readBlobString(blobs, entry.Hash)
		if err != nil {
			return fmt.Errorf("%w: %s", err, entry.Path)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("recovery: create parent of %s: %w", entry.Path, err)
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("recovery: replace symlink %s: %w", entry.Path, err)
		}
		if err := os.Symlink(target, path); err != nil {
			return fmt.Errorf("recovery: create symlink %s: %w", entry.Path, err)
		}
		return nil
	case KindFile:
		return writeFileContent(root, entry, blobs)
	default:
		return fmt.Errorf("recovery: unsupported entry kind %q for %s", entry.Kind, entry.Path)
	}
}

// writeFileContent writes a file atomically, so an interrupted restore cannot
// leave a truncated file behind.
func writeFileContent(root string, entry Entry, blobs BlobReader) error {
	path := filepath.Join(root, filepath.FromSlash(entry.Path))
	reader, size, err := blobs.Open(entry.Hash)
	if err != nil {
		return fmt.Errorf("%w: %s", err, entry.Path)
	}
	defer func() { _ = reader.Close() }()
	if entry.Size != 0 && size >= 0 && size != entry.Size {
		return fmt.Errorf("recovery: blob %s for %s is %d bytes, manifest says %d",
			entry.Hash, entry.Path, size, entry.Size)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("recovery: create parent of %s: %w", entry.Path, err)
	}
	// A symlink at the target path would make the temp-file rename replace the
	// link rather than the file it points at, so it is removed first.
	if info, err := os.Lstat(path); err == nil && info.Mode()&fs.ModeSymlink != 0 {
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("recovery: replace symlink %s: %w", entry.Path, err)
		}
	}
	temp, err := os.CreateTemp(dir, ".ohgo-restore-*")
	if err != nil {
		return fmt.Errorf("recovery: create temp for %s: %w", entry.Path, err)
	}
	tempName := temp.Name()
	cleanup := func() {
		_ = temp.Close()
		_ = os.Remove(tempName)
	}
	if _, err := io.Copy(temp, reader); err != nil {
		cleanup()
		return fmt.Errorf("recovery: write %s: %w", entry.Path, err)
	}
	if err := temp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("recovery: sync %s: %w", entry.Path, err)
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempName)
		return fmt.Errorf("recovery: close %s: %w", entry.Path, err)
	}
	if err := os.Chmod(tempName, fs.FileMode(entry.Mode)); err != nil {
		_ = os.Remove(tempName)
		return fmt.Errorf("recovery: set mode on %s: %w", entry.Path, err)
	}
	if err := os.Rename(tempName, path); err != nil {
		_ = os.Remove(tempName)
		return fmt.Errorf("recovery: install %s: %w", entry.Path, err)
	}
	return nil
}

// readBlobString loads a symlink target or other small blob.
func readBlobString(blobs BlobReader, hash string) (string, error) {
	reader, _, err := blobs.Open(hash)
	if err != nil {
		return "", err
	}
	defer func() { _ = reader.Close() }()
	data, err := io.ReadAll(reader)
	if err != nil {
		return "", fmt.Errorf("recovery: read blob %s: %w", hash, err)
	}
	return string(data), nil
}
