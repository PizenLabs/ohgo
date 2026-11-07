package history

// Ephemeral scratch storage for oversized master prompts.
//
// A prompt that crosses either threshold is copied to the project-local
// .ohgo/scratch directory before the turn starts, and the bytes read back from
// that file become the model's user message. The copy is not a tool handoff:
// the payload already contains the full text, so the model never spends a
// round-trip opening it. The copy is compressed deterministically — greetings,
// politeness, and markdown decoration are removed while code and identifiers
// stay byte-for-byte — because the scratch file is context, not a durable
// record. Scratch is deliberately disposable. The durable copy is the raw
// prompt history entry, written before this file exists, which is what recall
// reads after a session closes or a crash sweeper removes the file.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/PizenLabs/ohgo/internal/project"
	"github.com/PizenLabs/ohgo/internal/promptopt"
)

const (
	// ScratchDirName is the ephemeral directory inside the project-local OHGO
	// boundary. The enclosing .ohgo directory is already gitignored.
	ScratchDirName = "scratch"
	// ScratchLineThreshold is the line count at which a prompt is offloaded.
	ScratchLineThreshold = 50
	// ScratchRuneThreshold is the rune count at which a prompt is offloaded.
	ScratchRuneThreshold = 4000
	// ScratchStaleAge is how long an orphaned scratch file may survive a
	// startup sweep. A file this old cannot belong to a live prompt in the
	// normal interactive lifecycle.
	ScratchStaleAge = 24 * time.Hour
)

// ShouldOffload reports whether a prompt crosses either hybrid threshold.
//
// The two measures are independent: a short prompt with many blank lines is
// still large on screen, and a single enormous paragraph is large in context.
func ShouldOffload(prompt string) bool {
	if len([]rune(prompt)) >= ScratchRuneThreshold {
		return true
	}
	if prompt == "" {
		return false
	}
	return strings.Count(prompt, "\n")+1 >= ScratchLineThreshold
}

// Scratch owns the lifecycle of this process's scratch files.
//
// It is safe for concurrent use. Every file is tracked by session id so a
// session switch can remove precisely that conversation's scratch without
// touching another live session in the same project.
type Scratch struct {
	root string
	dir  string
	now  func() time.Time

	mu     sync.Mutex
	active map[int64]map[string]struct{}
}

// NewScratch opens the project-local scratch boundary for root.
//
// The directory is created lazily by the first offload, so a normal session
// leaves no empty scratch tree behind.
func NewScratch(root string) *Scratch {
	root = project.Root(root)
	return &Scratch{
		root:   root,
		dir:    filepath.Join(root, project.DirName, ScratchDirName),
		now:    time.Now,
		active: map[int64]map[string]struct{}{},
	}
}

// Dir returns the scratch directory this owner writes to.
func (s *Scratch) Dir() string {
	if s == nil {
		return ""
	}
	return s.dir
}

// ShouldOffload reports whether a prompt crosses either hybrid threshold.
func (s *Scratch) ShouldOffload(prompt string) bool {
	return ShouldOffload(prompt)
}

// Offload compresses prompt, writes it to the session's scratch area, and
// returns the bytes read back from that file.
//
// offloaded is false for a prompt below both thresholds. On a write or read
// failure the original prompt remains the caller's payload: losing a large
// prompt because a disposable cache directory is unwritable would be the worse
// failure. The file is removed when it cannot be verified by reading it back.
//
// Compression never writes an empty payload: a prompt made entirely of filler
// still carries the user's own bytes into the model message.
func (s *Scratch) Offload(sessionID int64, prompt string) (path, text string, offloaded bool, err error) {
	if s == nil || !ShouldOffload(prompt) {
		return "", "", false, nil
	}
	payload := promptopt.CompressPrompt(prompt)
	if strings.TrimSpace(payload) == "" {
		payload = prompt
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return "", "", false, fmt.Errorf("history: create scratch directory %s: %w", s.dir, err)
	}

	stamp := s.now().UTC().UnixNano()
	for attempt := 0; ; attempt++ {
		name := fmt.Sprintf("prompt_%d_%d.md", sessionID, stamp)
		if attempt > 0 {
			name = fmt.Sprintf("prompt_%d_%d_%d.md", sessionID, stamp, attempt)
		}
		path = filepath.Join(s.dir, name)
		file, openErr := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(openErr, os.ErrExist) {
			continue
		}
		if openErr != nil {
			return "", "", false, fmt.Errorf("history: create scratch %s: %w", path, openErr)
		}
		_, writeErr := file.WriteString(payload)
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			_ = os.Remove(path)
			return "", "", false, fmt.Errorf("history: write scratch %s: %w", path, errors.Join(writeErr, closeErr))
		}
		break
	}

	content, readErr := os.ReadFile(path)
	if readErr != nil {
		_ = os.Remove(path)
		return "", "", false, fmt.Errorf("history: read scratch %s: %w", path, readErr)
	}

	s.mu.Lock()
	if s.active[sessionID] == nil {
		s.active[sessionID] = map[string]struct{}{}
	}
	s.active[sessionID][path] = struct{}{}
	s.mu.Unlock()
	return path, string(content), true, nil
}

// ReleaseSession removes every scratch file for one session, including a file
// left by an earlier process on the same project. It is called when that
// conversation closes, is replaced, or is switched away from.
func (s *Scratch) ReleaseSession(sessionID int64) error {
	if s == nil {
		return nil
	}
	paths := map[string]struct{}{}
	s.mu.Lock()
	for path := range s.active[sessionID] {
		paths[path] = struct{}{}
	}
	delete(s.active, sessionID)
	s.mu.Unlock()

	// The glob also catches an orphan from a previous process, so closing a
	// resumed session cannot leave its scratch behind until the stale sweep.
	matches, _ := filepath.Glob(filepath.Join(s.dir, fmt.Sprintf("prompt_%d_*.md", sessionID)))
	for _, path := range matches {
		paths[path] = struct{}{}
	}
	return removeAll(paths)
}

// ReleaseAll removes every file this process offloaded. It is the shutdown
// path for /exit, a signal, and a terminal that closed without a session
// switch.
func (s *Scratch) ReleaseAll() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	paths := map[string]struct{}{}
	for _, files := range s.active {
		for path := range files {
			paths[path] = struct{}{}
		}
	}
	s.active = map[int64]map[string]struct{}{}
	s.mu.Unlock()
	return removeAll(paths)
}

// SweepStale removes orphaned scratch files older than maxAge.
//
// Startup calls this asynchronously. An active file owned by this process is
// never swept, even if its modification time is old, because the process is
// still using it. maxAge <= 0 selects ScratchStaleAge.
func (s *Scratch) SweepStale(maxAge time.Duration) (int, error) {
	if s == nil {
		return 0, nil
	}
	if maxAge <= 0 {
		maxAge = ScratchStaleAge
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("history: read scratch directory %s: %w", s.dir, err)
	}

	s.mu.Lock()
	active := map[string]struct{}{}
	for _, files := range s.active {
		for path := range files {
			active[path] = struct{}{}
		}
	}
	s.mu.Unlock()

	cutoff := s.now().Add(-maxAge)
	removed := 0
	var errs []error
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "prompt_") || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		path := filepath.Join(s.dir, entry.Name())
		if _, live := active[path]; live {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			errs = append(errs, infoErr)
			continue
		}
		if !info.ModTime().Before(cutoff) {
			continue
		}
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			errs = append(errs, removeErr)
			continue
		}
		removed++
	}
	return removed, errors.Join(errs...)
}

// removeAll removes the given paths, combining failures so one locked file
// does not hide another.
func removeAll(paths map[string]struct{}) error {
	var errs []error
	for path := range paths {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
