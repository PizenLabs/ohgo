// Package diff implements the interactive visual diff and context inspector
// behind the /diff command.
//
// It is deliberately not a git client. It reads the working tree, the index,
// and committed history through a handful of read-only git invocations, then
// renders the result as a two-pane inspector: a hierarchical changed-file tree
// on the left and the hunks for the selected file on the right.
//
// The package is self-contained: it depends on the shared Myers diff engine in
// internal/diff, bubbletea, and lipgloss, and on nothing in the tui package.
// That keeps the viewport, the parser, and the git driver testable without a
// terminal and without an import cycle back into tui.
package diff

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// FileStatus is the single-letter change kind git reports for a path.
//
// Rename detection is disabled (see Git.diffArgs and Git.runDiff), so a rename
// arrives as a delete plus an add and every entry is one of these three. That
// keeps the tree badge a single letter and removes the two-path bookkeeping
// git's rename records would otherwise force on the parser.
type FileStatus string

// Change kinds, matching git's --name-status letters.
const (
	// StatusModified is "M": the path exists on both sides but differs.
	StatusModified FileStatus = "M"
	// StatusAdded is "A": the path is new on the target side.
	StatusAdded FileStatus = "A"
	// StatusDeleted is "D": the path is gone on the target side.
	StatusDeleted FileStatus = "D"
)

// FileChange is one changed path with its status and line counts.
//
// It is the flat form the initial, content-free fetch produces; BuildTree folds
// a slice of these into the sidebar's hierarchy.
type FileChange struct {
	// Path is repository-root-relative and slash-separated.
	Path string
	// Status is the single-letter change kind.
	Status FileStatus
	// Added and Removed are the numstat line counts. Both are zero for a
	// binary file, which reports "-" rather than a count.
	Added   int
	Removed int
	// Binary reports that line counts are unavailable: git reported "-", or an
	// untracked file was binary, unreadable, or larger than the scan bound.
	Binary bool
}

// Spec names what a diff compares. It is built once from the /diff arguments
// and threaded through every fetch so a single revision selection decides both
// the file list and each file's hunks.
type Spec struct {
	// Rev is the revision argument handed to git: "HEAD" for the working tree
	// against HEAD, a single revision for "that revision against the working
	// tree", or an explicit "base..target" range.
	Rev string
	// Label is the human-readable title shown in the pane header.
	Label string
}

// emptyTreeSHA is git's empty tree object. Comparing against it turns every
// file git knows about into an addition, which is exactly the view a repository
// with no commits should show: there is no HEAD to diff against, but the files
// on disk and in the index are real.
//
// It is the SHA-1 empty tree. A repository using the sha256 object format has a
// different one, but such a repository is not the case this fallback exists
// for, and every command that reads it still runs through the repository's own
// git.
const emptyTreeSHA = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// DefaultSpec is the bare `/diff`: staged and unstaged worktree changes
// against HEAD. `git diff HEAD` is exactly that union.
//
// The driver substitutes the empty tree for HEAD when the repository has no
// commits, so this spec is also the unborn-repository form.
func DefaultSpec() Spec {
	return Spec{Rev: "HEAD", Label: "working tree vs HEAD"}
}

// worktree reports whether the spec compares the working tree against HEAD, the
// bare `/diff` form, rather than naming revisions. Only the worktree form has
// an unborn-HEAD fallback and untracked files to add.
func (s Spec) worktree() bool { return s.Rev == "" || s.Rev == "HEAD" }

// ParseArgs turns /diff arguments into a Spec.
//
// The grammar is deliberately small: nothing targets HEAD, one word targets a
// revision, and one word containing ".." is an explicit range. A second word is
// rejected rather than guessed at, because there is no unambiguous way to read
// it: "/diff main dev" could mean either direction.
func ParseArgs(args []string) (Spec, error) {
	var words []string
	for _, arg := range args {
		if trimmed := strings.TrimSpace(arg); trimmed != "" {
			words = append(words, trimmed)
		}
	}
	switch len(words) {
	case 0:
		return DefaultSpec(), nil
	case 1:
		rev := words[0]
		if strings.Contains(rev, "..") {
			return Spec{Rev: rev, Label: rev}, nil
		}
		return Spec{Rev: rev, Label: rev + " vs working tree"}, nil
	default:
		return Spec{}, fmt.Errorf("usage: /diff [target] or /diff <base>..<target>")
	}
}

// Fetcher is the git access the model needs: a cheap file list and an
// on-demand per-file diff. It is an interface so the viewport, the parser, and
// the tree can be tested against a scripted fake with no repository and no
// subprocess.
type Fetcher interface {
	// ChangedFiles lists the changed paths and their stats. It must not read
	// file contents: the initial /diff frame pays for the name and count
	// columns only.
	ChangedFiles(ctx context.Context, spec Spec) ([]FileChange, error)
	// FileDiff returns the unified-diff hunks for one path.
	FileDiff(ctx context.Context, spec Spec, path string) ([]DiffChunk, error)
}

// Git is the streaming git driver. A zero Dir means the process working
// directory.
//
// Every invocation runs from the repository root, resolved once, so the
// root-relative paths git prints are also valid pathspecs when they are handed
// back to `git diff -- <path>`.
type Git struct {
	// Dir is any directory inside the repository.
	Dir string

	// root resolves once: rev-parse is a subprocess, and the answer cannot
	// change while an inspector is open. The mutex, rather than sync.Once,
	// lets a failed resolution be retried by a later call.
	mu       sync.Mutex
	root     string
	rootErr  error
	resolved bool

	// base is the revision the worktree is diffed against: HEAD, or the empty
	// tree in a repository with no commits. It is cached under its own mutex,
	// separate from root so resolving it can call out to git without holding
	// the lock the subprocess helpers take.
	baseMu       sync.Mutex
	base         string
	baseResolved bool
}

// NewGit returns a driver rooted at dir.
func NewGit(dir string) *Git { return &Git{Dir: dir} }

// Root resolves and caches the repository top level.
func (g *Git) Root(ctx context.Context) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.resolved {
		return g.root, g.rootErr
	}
	// rev-parse runs in the caller's directory, not the root: resolving the
	// root is what this call is for, so it must not recurse into run().
	out, err := runIn(ctx, g.Dir, "rev-parse", "--show-toplevel")
	if err != nil {
		g.rootErr = err
		g.resolved = true
		return "", err
	}
	g.root = strings.TrimSpace(out)
	g.resolved = true
	return g.root, nil
}

// ChangedFiles lists the changed paths plus their line counts.
//
// Two queries feed the tracked side because git has no single porcelain that
// emits both a status letter and a numstat count. Both are content-free, which
// is the lazy-evaluation boundary the inspector is built around: opening /diff
// reads names and counts, never hunks.
//
// The bare worktree form resolves its base first — HEAD when the branch has a
// commit, the empty tree otherwise — and then appends the untracked files,
// which no `git diff` reports.
func (g *Git) ChangedFiles(ctx context.Context, spec Spec) ([]FileChange, error) {
	if !spec.worktree() {
		changes, err := g.revChangedFiles(ctx, spec.Rev)
		if err != nil {
			return nil, err
		}
		sortChanges(changes)
		return changes, nil
	}

	changes, err := g.revChangedFiles(ctx, g.baseRev(ctx))
	if err != nil {
		return nil, err
	}
	untracked, err := g.untrackedChanges(ctx)
	if err != nil {
		return nil, err
	}
	changes = append(changes, untracked...)
	sortChanges(changes)
	return changes, nil
}

// revChangedFiles lists the tracked changes against one revision.
func (g *Git) revChangedFiles(ctx context.Context, rev string) ([]FileChange, error) {
	statusOut, err := g.runDiff(ctx, rev, "--name-status", "-z")
	if err != nil {
		return nil, err
	}
	numstatOut, err := g.runDiff(ctx, rev, "--numstat", "-z")
	if err != nil {
		return nil, err
	}
	return parseNameStatus(statusOut, parseNumstat(numstatOut)), nil
}

// untrackedChanges lists files git does not track yet, each as an addition.
//
// `git diff` never reports them — not against the empty tree, and not even with
// an explicit pathspec — so the worktree view adds them here. Their counts come
// from a bounded read of the file on disk, because there is no git object to
// numstat.
func (g *Git) untrackedChanges(ctx context.Context) ([]FileChange, error) {
	paths, err := g.untrackedFiles(ctx)
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, nil
	}
	root, err := g.Root(ctx)
	if err != nil {
		return nil, err
	}
	changes := make([]FileChange, 0, len(paths))
	for _, path := range paths {
		added, binary := countUntracked(filepath.Join(root, filepath.FromSlash(path)))
		changes = append(changes, FileChange{Path: path, Status: StatusAdded, Added: added, Binary: binary})
	}
	return changes, nil
}

// untrackedFiles lists working-tree paths absent from the index, honouring
// .gitignore. -z keeps paths with spaces or newlines intact.
func (g *Git) untrackedFiles(ctx context.Context) ([]string, error) {
	out, err := g.run(ctx, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, err
	}
	return splitNUL(out), nil
}

// sortChanges gives the tree a stable order regardless of git's output order,
// which varies with rename detection and pathspec rules.
func sortChanges(changes []FileChange) {
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
}

// hasHeadRef reports whether HEAD resolves to a commit. A repository with no
// commits — a fresh `git init`, or any unborn branch — has no HEAD, and every
// `git diff HEAD` would fail with "ambiguous argument 'HEAD'". --quiet keeps
// the exit status as the only signal, so no error text can leak into the view.
func (g *Git) hasHeadRef(ctx context.Context) bool {
	_, err := g.run(ctx, "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	return err == nil
}

// baseRev is the revision the working tree is diffed against: HEAD when the
// branch has a commit, otherwise the empty tree.
//
// The answer is cached because it cannot change while an inspector is open and
// resolving it costs a subprocess that FileDiff would otherwise repeat for
// every file. The lock is released around the git call, so a concurrent caller
// racing the resolution simply repeats the cheap lookup rather than blocking.
func (g *Git) baseRev(ctx context.Context) string {
	g.baseMu.Lock()
	if g.baseResolved {
		base := g.base
		g.baseMu.Unlock()
		return base
	}
	g.baseMu.Unlock()

	base := emptyTreeSHA
	if g.hasHeadRef(ctx) {
		base = "HEAD"
	}

	g.baseMu.Lock()
	g.base, g.baseResolved = base, true
	g.baseMu.Unlock()
	return base
}

// FileDiff streams one file's unified diff and returns its parsed hunks.
//
// The output is scanned line by line rather than read whole, so a file with
// tens of thousands of changed lines never materialises as one string. The
// configured context is deliberately small: the viewport is the wrong place to
// pay for context nobody scrolls to, and the gap markers tell the reader what
// was elided.
func (g *Git) FileDiff(ctx context.Context, spec Spec, path string) ([]DiffChunk, error) {
	if !spec.worktree() {
		return g.parseDiff(ctx, g.diffArgs(spec.Rev, path)...)
	}
	untracked, err := g.isUntracked(ctx, path)
	if err != nil {
		return nil, err
	}
	if untracked {
		// There is no base side for a file git has never seen, so the
		// additions are read straight from disk.
		return g.untrackedDiff(ctx, path)
	}
	return g.parseDiff(ctx, g.diffArgs(g.baseRev(ctx), path)...)
}

// diffArgs builds the single-file diff invocation.
func (g *Git) diffArgs(rev, path string) []string {
	return []string{
		"diff", "--no-color", "--no-ext-diff", "--no-renames",
		"--unified=" + strconv.Itoa(DefaultContext), rev, "--", path,
	}
}

// isUntracked reports whether path is present in the working tree but absent
// from the index. A tracked path, including one deleted from the working tree,
// reports false.
func (g *Git) isUntracked(ctx context.Context, path string) (bool, error) {
	out, err := g.run(ctx, "ls-files", "--others", "--exclude-standard", "-z", "--", path)
	if err != nil {
		return false, err
	}
	return len(splitNUL(out)) > 0, nil
}

// runDiff builds a `git diff` invocation with the shared flags.
func (g *Git) runDiff(ctx context.Context, rev string, extra ...string) (string, error) {
	args := append([]string{"diff", "--no-color", "--no-ext-diff", "--no-renames"}, extra...)
	args = append(args, rev)
	return g.run(ctx, args...)
}

// untrackedDiff renders a file git has never seen as a single hunk of
// additions.
//
// Git cannot produce this diff: an untracked path is not in the index, so
// `git diff <tree> -- <path>` is empty whichever tree is named. The file is
// streamed from disk under the same bound that gates its line count, so a large
// added file is truncated to unavailable rather than materialised row by row.
func (g *Git) untrackedDiff(ctx context.Context, path string) ([]DiffChunk, error) {
	root, err := g.Root(ctx)
	if err != nil {
		return nil, err
	}
	full := filepath.Join(root, filepath.FromSlash(path))

	var lines []DiffLine
	binary, _ := streamUntracked(full, func(line string) {
		lines = append(lines, DiffLine{Type: LineAddition, NewLineNum: len(lines) + 1, Content: sanitize(line)})
	})
	if binary || len(lines) == 0 {
		// Unreadable, binary, or empty: no hunk to show. The empty case is
		// what git prints for a new empty file too.
		return nil, nil
	}
	newLines := len(lines)
	if !fileHasTrailingNewline(full) {
		lines = append(lines, DiffLine{Type: LineMeta, Content: "No newline at end of file"})
	}
	return []DiffChunk{{
		Header:   additionHeader(newLines),
		NewStart: 1, NewLines: newLines,
		Lines: lines,
	}}, nil
}

// parseDiff runs a diff command and parses its stdout as it streams.
func (g *Git) parseDiff(ctx context.Context, args ...string) ([]DiffChunk, error) {
	root, err := g.Root(ctx)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = root
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("diff: git %s: %w", strings.Join(args, " "), err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("diff: git %s: %w", strings.Join(args, " "), err)
	}
	chunks, parseErr := ParseUnified(pipe)
	// Wait reaps the process even when parsing failed, so a malformed stream
	// cannot leave a zombie behind.
	if err := cmd.Wait(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return nil, fmt.Errorf("diff: git %s: %s", strings.Join(args, " "), message)
	}
	if parseErr != nil {
		return nil, parseErr
	}
	return chunks, nil
}

// run executes git and returns stdout as a string.
//
// Name and status listings are small and bounded by the number of changed
// files, so buffering them whole is cheaper than the streaming the per-file
// diff needs. dir is the repository root once it is known, so pathspecs and
// printed paths agree.
func (g *Git) run(ctx context.Context, args ...string) (string, error) {
	dir := g.Dir
	if root, err := g.Root(ctx); err == nil {
		dir = root
	}
	return runIn(ctx, dir, args...)
}

// runIn runs git in dir, returning stdout or an error carrying stderr.
func runIn(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return "", fmt.Errorf("diff: git %s: %s", strings.Join(args, " "), message)
	}
	return stdout.String(), nil
}

// parseNameStatus reads `--name-status -z` output: STATUS\0path\0 repeated.
func parseNameStatus(out string, counts map[string]FileChange) []FileChange {
	fields := splitNUL(out)
	var changes []FileChange
	for i := 0; i+1 < len(fields); i += 2 {
		status := strings.TrimSpace(fields[i])
		path := fields[i+1]
		if status == "" || path == "" {
			continue
		}
		change := FileChange{Path: path, Status: statusFor(status)}
		if c, ok := counts[path]; ok {
			change.Added, change.Removed, change.Binary = c.Added, c.Removed, c.Binary
		}
		changes = append(changes, change)
	}
	return changes
}

// statusFor maps a --name-status code to a badge letter. Type changes and
// unmerged entries read as modifications: the file exists on both sides.
func statusFor(code string) FileStatus {
	switch code[0] {
	case 'A':
		return StatusAdded
	case 'D':
		return StatusDeleted
	default:
		return StatusModified
	}
}

// parseNumstat reads `--numstat -z`: ADDED\tREMOVED\tpath\0 per file. A binary
// file reports "-" for both counts.
func parseNumstat(out string) map[string]FileChange {
	fields := splitNUL(out)
	counts := make(map[string]FileChange, len(fields))
	for _, field := range fields {
		if field == "" {
			continue
		}
		parts := strings.SplitN(field, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		entry := FileChange{Path: parts[2]}
		if parts[0] == "-" || parts[1] == "-" {
			entry.Binary = true
		} else {
			entry.Added, _ = strconv.Atoi(parts[0])
			entry.Removed, _ = strconv.Atoi(parts[1])
		}
		counts[entry.Path] = entry
	}
	return counts
}

// maxUntrackedScan bounds how much of an untracked file the inspector reads.
// The viewer keeps a row per line, so a generated artifact far larger than any
// source file is reported as unavailable rather than materialised line by line.
const maxUntrackedScan = 4 << 20

// streamUntracked streams an untracked file line by line, calling emit with the
// trailing newline stripped.
//
// It reports binary=true, emitting nothing after that point, at the first NUL
// byte — git's own binary heuristic — or as soon as either a single line or the
// file as a whole exceeds the scan bound. The scanner's buffer is fixed at that
// bound, so a file that is one enormous line cannot pull an unbounded
// allocation into memory, and the running total stops a file of endless short
// lines just as firmly.
func streamUntracked(path string, emit func(line string)) (binary bool, err error) {
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	// Read-only: a Close error cannot change the parsed result, and the
	// explicit discard is the codebase's mark for a deliberate one.
	defer func() { _ = file.Close() }()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64<<10), maxUntrackedScan)
	var read int64
	for scanner.Scan() {
		line := scanner.Bytes()
		read += int64(len(line)) + 1 // +1 for the newline the scanner stripped
		if read > maxUntrackedScan {
			return true, nil
		}
		if bytes.IndexByte(line, 0) >= 0 {
			return true, nil
		}
		// Bytes is only valid until the next Scan, so the callback receives a
		// copy.
		emit(string(line))
	}
	if errors.Is(scanner.Err(), bufio.ErrTooLong) {
		return true, nil
	}
	if err := scanner.Err(); err != nil {
		return false, err
	}
	return false, nil
}

// countUntracked counts an untracked file's lines. Anything the stream cannot
// read — a permission error, a NUL byte, an oversized line — is reported as
// binary, which the tree renders as an added file with no line counts rather
// than as a failure.
func countUntracked(path string) (added int, binary bool) {
	binary, err := streamUntracked(path, func(string) { added++ })
	if err != nil || binary {
		return 0, true
	}
	return added, false
}

// fileHasTrailingNewline reports whether a file's last byte is a newline. An
// empty file counts as newline-terminated so no marker is appended to it.
func fileHasTrailingNewline(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.Size() == 0 {
		return true
	}
	file, err := os.Open(path)
	if err != nil {
		return true
	}
	defer func() { _ = file.Close() }()

	var last [1]byte
	if _, err := file.ReadAt(last[:], info.Size()-1); err != nil {
		return true
	}
	return last[0] == '\n'
}

// additionHeader is the hunk header for a file with no base side, matching
// git's own shape: a single added line omits its count ("+1", not "+1,1").
func additionHeader(newLines int) string {
	if newLines == 1 {
		return "@@ -0,0 +1 @@"
	}
	return fmt.Sprintf("@@ -0,0 +1,%d @@", newLines)
}

// splitNUL splits git's -z output into non-empty fields.
func splitNUL(out string) []string {
	raw := strings.Split(out, "\x00")
	fields := raw[:0]
	for _, entry := range raw {
		if entry != "" {
			fields = append(fields, entry)
		}
	}
	return fields
}

// scanBuffer bounds one unified-diff line. Minified assets produce very long
// lines; 4 MiB is far past anything a viewer renders and still bounds the
// scanner's own allocation.
const scanBuffer = 4 << 20

// newScanner returns a line scanner sized for diff output.
func newScanner(r io.Reader) *bufio.Scanner {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64<<10), scanBuffer)
	return scanner
}
