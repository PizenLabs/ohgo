// Package history persists the prompts a user submits at the interactive
// prompt.
//
// History is two-tiered. Every prompt is appended to the active project's
// <root>/.ohgo/history and to the machine-wide ~/.ohgo/history. On startup the
// project entries win and sit at the tail of the merged view, while global
// entries fill in ahead of them with duplicates removed, so walking backwards
// reaches project prompts before global ones.
//
// A multi-line prompt is escaped rather than split, so recalling it restores
// the original text instead of replaying its fragments one line at a time.
// File failures never reach the user: an unreadable or unwritable scope keeps
// working in memory and its persistence path becomes a silent no-op.
package history

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/PizenLabs/ohgo/internal/project"
	"github.com/PizenLabs/ohgo/internal/state"
)

// DefaultMaxEntries bounds each scope's history. It is the same order of
// magnitude as a shell history: large enough to be useful, small enough that a
// prefix scan stays imperceptible.
const DefaultMaxEntries = 10000

// FileName is the history file inside both the machine-level and project-level
// OHGO directories.
const FileName = "history"

// ProjectDirName is the project-local OHGO directory. It mirrors the project
// package's boundary rather than re-declaring the name here.
const ProjectDirName = project.DirName

// Store is the two-tier prompt history: a project-local ring buffer with a
// machine-wide fallback.
//
// Entries are ordered oldest first and arrange project history at the tail, so
// walking backwards reaches recent project prompts before global ones. Add
// records a prompt in both scopes; recall and suggestion read the merged view.
type Store struct {
	local  *scopedStore
	global *scopedStore

	// mu guards entries, the merged navigation order.
	mu      sync.Mutex
	entries []string
}

// Load opens the project and global histories for one session.
//
// projectRoot is the workspace root. An empty root or an unresolvable home
// degrades that scope to memory only rather than failing the session.
func Load(projectRoot string) *Store {
	globalPath, err := GlobalPath()
	if err != nil {
		globalPath = ""
	}
	return Open(ProjectPath(projectRoot), globalPath, DefaultMaxEntries)
}

// Open loads explicit local and global history files and starts their writers.
//
// max <= 0 selects DefaultMaxEntries. An empty path produces a memory-only
// scope, which is the fallback every file error degrades to.
func Open(localPath, globalPath string, max int) *Store {
	if max <= 0 {
		max = DefaultMaxEntries
	}
	s := &Store{
		local:  openScoped(localPath, max),
		global: openScoped(globalPath, max),
	}
	s.entries = merge(s.local.Entries(), s.global.Entries())
	return s
}

// GlobalPath resolves ~/.ohgo/history.
func GlobalPath() (string, error) {
	return state.Path(FileName)
}

// ProjectPath resolves <projectRoot>/.ohgo/history. An empty root has no
// project scope.
//
// A nested working directory maps to the repository root, so one project is
// one history no matter where inside it the session started.
func ProjectPath(projectRoot string) string {
	root := strings.TrimSpace(projectRoot)
	if root == "" {
		return ""
	}
	return filepath.Join(project.Root(root), ProjectDirName, FileName)
}

// Entries returns a copy of the merged history, oldest first, with project
// entries after global ones so reverse traversal prefers the project.
func (s *Store) Entries() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.entries...)
}

// Add records one submitted prompt in both scopes.
//
// Empty prompts are ignored. Each scope independently drops a consecutive
// duplicate, and the merged view is rebuilt from the two bounded scopes so it
// can never drift from what recall will actually see. The project file is
// written before the global one, as the visible order implies.
func (s *Store) Add(prompt string) {
	prompt = strings.TrimRight(prompt, "\r\n")
	if strings.TrimSpace(prompt) == "" {
		return
	}

	s.local.Add(prompt)
	s.global.Add(prompt)

	s.mu.Lock()
	s.entries = merge(s.local.Entries(), s.global.Entries())
	s.mu.Unlock()
}

// Flush waits for every pending append in both scopes to reach its file.
func (s *Store) Flush() error {
	return errors.Join(s.local.Flush(), s.global.Flush())
}

// Close flushes and stops both background writers. It is idempotent.
func (s *Store) Close() error {
	return errors.Join(s.local.Close(), s.global.Close())
}

// Suggestion returns the most recent entry that extends prefix, or "".
//
// The merged order puts project entries last, so scanning newest first reaches
// a project match before a global one. An entry equal to the prefix is not a
// suggestion, and a multi-line entry is skipped because ghost text is a single
// display row.
func Suggestion(entries []string, prefix string) string {
	if prefix == "" {
		return ""
	}
	for i := len(entries) - 1; i >= 0; i-- {
		entry := entries[i]
		if entry == prefix || strings.ContainsRune(entry, '\n') {
			continue
		}
		if strings.HasPrefix(entry, prefix) {
			return entry
		}
	}
	return ""
}

// merge joins the two scopes for recall.
//
// Project history wins: a global entry that already exists in the project is
// dropped entirely, so the project copy is the one recall and suggestion see.
// The global survivors go first and the project list last, which makes a
// backwards walk visit project prompts first.
func merge(project, global []string) []string {
	inProject := make(map[string]struct{}, len(project))
	for _, entry := range project {
		inProject[entry] = struct{}{}
	}
	merged := make([]string, 0, len(project)+len(global))
	for _, entry := range global {
		if _, exists := inProject[entry]; exists {
			continue
		}
		merged = append(merged, entry)
	}
	return append(merged, project...)
}

// scopedStore is one append-only history file with an in-memory mirror.
//
// All methods are safe for concurrent use. Add updates memory immediately and
// hands the file append to a background writer, so submitting a prompt never
// waits on disk I/O; Flush or Close makes the write durable.
type scopedStore struct {
	path string
	max  int

	// mu guards entries.
	mu      sync.Mutex
	entries []string

	// lifecycle keeps Close from stopping the worker while Add or Flush is
	// using its channel. closed is read under it.
	lifecycle sync.RWMutex
	closed    bool

	// writeMu serialises the file's sole writer with the synchronous fallback
	// used after Close.
	writeMu sync.Mutex
	failed  bool

	// requests carries writes to the background writer. done closes when the
	// worker has drained and stopped.
	requests chan writeRequest
	done     chan struct{}
	wg       sync.WaitGroup
}

type writeRequest struct {
	// line is one encoded entry to append. Ignored when rewrite is non-nil.
	line string
	// rewrite replaces the whole file with snapshot. It is used when the
	// history crosses its bound and the oldest entry must disappear.
	rewrite []string
	barrier chan struct{}
}

func openScoped(path string, max int) *scopedStore {
	if max <= 0 {
		max = DefaultMaxEntries
	}
	s := &scopedStore{path: path, max: max}
	s.load()

	if path != "" {
		s.requests = make(chan writeRequest, 128)
		s.done = make(chan struct{})
		s.wg.Add(1)
		go s.worker()
	}
	return s
}

// Entries returns a copy of one scope, oldest first.
func (s *scopedStore) Entries() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.entries...)
}

// Add records one prompt in this scope.
func (s *scopedStore) Add(prompt string) {
	prompt = strings.TrimRight(prompt, "\r\n")
	if strings.TrimSpace(prompt) == "" {
		return
	}

	s.lifecycle.RLock()
	defer s.lifecycle.RUnlock()
	if s.closed {
		return
	}

	s.mu.Lock()
	if n := len(s.entries); n > 0 && s.entries[n-1] == prompt {
		s.mu.Unlock()
		return
	}
	s.entries = append(s.entries, prompt)
	var rewrite []string
	if len(s.entries) > s.max {
		trimmed := make([]string, s.max)
		copy(trimmed, s.entries[len(s.entries)-s.max:])
		s.entries = trimmed
		// Once the bound is crossed the file has to lose its oldest line
		// too, and an append-only file cannot do that. Rewrite the bounded
		// snapshot atomically; this happens at most once per submitted
		// prompt and runs on the background writer.
		rewrite = append([]string(nil), s.entries...)
	}
	s.mu.Unlock()

	if s.requests == nil {
		return
	}
	if rewrite != nil {
		s.enqueue(writeRequest{rewrite: rewrite})
		return
	}
	s.enqueue(writeRequest{line: escape(prompt)})
}

// Flush waits for every request queued before it to reach the file.
func (s *scopedStore) Flush() error {
	if s.requests == nil {
		return nil
	}
	s.lifecycle.RLock()
	defer s.lifecycle.RUnlock()
	if s.closed {
		return nil
	}
	return s.flushLocked()
}

func (s *scopedStore) flushLocked() error {
	barrier := make(chan struct{})
	s.requests <- writeRequest{barrier: barrier}
	<-barrier
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.failed {
		return fmt.Errorf("history: writing %s failed", s.path)
	}
	return nil
}

// Close flushes pending appends and stops the background writer.
func (s *scopedStore) Close() error {
	if s.requests == nil {
		return nil
	}
	s.lifecycle.Lock()
	if s.closed {
		s.lifecycle.Unlock()
		return nil
	}
	err := s.flushLocked()
	s.closed = true
	close(s.done)
	s.lifecycle.Unlock()
	s.wg.Wait()
	return err
}

func (s *scopedStore) load() {
	if s.path == "" {
		return
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	lines := strings.Split(string(data), "\n")
	entries := make([]string, 0, len(lines))
	fileLines := 0
	for _, line := range lines {
		if line == "" {
			continue
		}
		fileLines++
		decoded := unescape(line)
		if strings.TrimSpace(decoded) == "" {
			continue
		}
		if n := len(entries); n > 0 && entries[n-1] == decoded {
			continue
		}
		entries = append(entries, decoded)
	}
	if len(entries) > s.max {
		entries = append([]string(nil), entries[len(entries)-s.max:]...)
	}
	s.entries = entries
	if fileLines > s.max {
		// Trim an oversized file on startup rather than leaving it to grow
		// until the next overflow. The rewrite is atomic, so a crash cannot
		// leave a half-written history.
		s.rewrite(entries)
	}
}

func (s *scopedStore) enqueue(req writeRequest) {
	select {
	case s.requests <- req:
	case <-s.done:
		// The worker has stopped: finish synchronously so a late submission
		// is still durable. Close would normally reject late writes, so this
		// is only reachable for a request racing shutdown.
		s.perform(req)
	}
}

func (s *scopedStore) worker() {
	defer s.wg.Done()
	for {
		select {
		case req := <-s.requests:
			s.perform(req)
		case <-s.done:
			// Drain requests accepted before shutdown so Close never loses a
			// prompt that Add already accepted.
			for {
				select {
				case req := <-s.requests:
					s.perform(req)
				default:
					return
				}
			}
		}
	}
}

func (s *scopedStore) perform(req writeRequest) {
	if req.barrier != nil {
		close(req.barrier)
		return
	}
	if req.rewrite != nil {
		s.rewrite(req.rewrite)
		return
	}
	s.appendLine(req.line)
}

func (s *scopedStore) appendLine(line string) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.failed {
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		s.failed = true
		return
	}
	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		s.failed = true
		return
	}
	_, writeErr := f.WriteString(line + "\n")
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		s.failed = true
	}
}

func (s *scopedStore) rewrite(entries []string) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.failed || s.path == "" {
		return
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		s.failed = true
		return
	}
	tmp, err := os.CreateTemp(dir, "."+FileName+"-*")
	if err != nil {
		s.failed = true
		return
	}
	name := tmp.Name()
	cleanup := func() { _ = os.Remove(name) }
	var sb strings.Builder
	for _, entry := range entries {
		sb.WriteString(escape(entry))
		sb.WriteByte('\n')
	}
	if _, err := tmp.WriteString(sb.String()); err != nil {
		_ = tmp.Close()
		cleanup()
		s.failed = true
		return
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		cleanup()
		s.failed = true
		return
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		s.failed = true
		return
	}
	if err := os.Rename(name, s.path); err != nil {
		cleanup()
		s.failed = true
	}
}

// escape keeps one prompt on one line. Backslash is escaped first so the
// decoder can distinguish an intentional "\\n" from an encoded newline.
func escape(prompt string) string {
	prompt = strings.ReplaceAll(prompt, `\`, `\\`)
	prompt = strings.ReplaceAll(prompt, "\n", `\n`)
	return strings.ReplaceAll(prompt, "\r", `\r`)
}

// unescape reverses escape. Unknown sequences are preserved literally, so a
// hand-edited history still loads instead of being discarded.
func unescape(line string) string {
	if !strings.ContainsRune(line, '\\') {
		return line
	}
	var sb strings.Builder
	sb.Grow(len(line))
	for i := 0; i < len(line); i++ {
		if line[i] != '\\' || i+1 >= len(line) {
			sb.WriteByte(line[i])
			continue
		}
		switch line[i+1] {
		case 'n':
			sb.WriteByte('\n')
			i++
		case 'r':
			sb.WriteByte('\r')
			i++
		case '\\':
			sb.WriteByte('\\')
			i++
		default:
			sb.WriteByte(line[i])
		}
	}
	return sb.String()
}
