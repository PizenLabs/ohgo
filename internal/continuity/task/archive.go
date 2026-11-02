// Package task owns OHGO's durable task continuity state.
package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// archivedFile is the on-disk form of a detached durable task.
//
// It carries the same Task and Progress a live checkpoint does, so a detached
// task loses nothing: objective, contract, snapshot, evidence, budget, audit,
// and lifecycle state all survive the session boundary. The file name is the
// task ID, which is minted once and never changes.
type archivedFile struct {
	Task     Task     `json:"task"`
	Progress Progress `json:"progress"`
}

// ArchiveDir returns the project-local directory holding detached tasks.
func (s *Store) ArchiveDir() string {
	return filepath.Join(s.Dir(), "tasks")
}

// archivePath returns the file for one detached task.
func (s *Store) archivePath(id string) string {
	safe := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, id)
	if safe == "" {
		safe = "task"
	}
	return filepath.Join(s.ArchiveDir(), safe+".json")
}

// ArchiveTask preserves a detached task durably.
//
// It upserts by task ID, so detaching the same task twice cannot duplicate it.
// The write is atomic (temp-then-rename) like the live checkpoint.
func (s *Store) ArchiveTask(t Task, progress Progress) error {
	if err := t.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(t.ID) == "" {
		return fmt.Errorf("%w: empty id", ErrInvalidTask)
	}
	if !t.Context.Empty() {
		context := t.Context.Clone()
		progress.Context = &context
	} else {
		progress.Context = nil
	}
	progress.CreatedAt = t.CreatedAt
	if progress.TaskID == "" {
		progress.TaskID = t.ID
	}
	if progress.Status == "" {
		progress.Status = t.Status
	}
	progress.ActiveRole = t.LastRole()
	if t.Handoff != nil {
		cloned := t.Handoff.Clone()
		progress.Handoff = &cloned
	} else {
		progress.Handoff = nil
	}
	if progress.Version == 0 {
		progress.Version = ProgressVersion
	}
	if err := os.MkdirAll(s.ArchiveDir(), 0o755); err != nil {
		return fmt.Errorf("task: create %s: %w", s.ArchiveDir(), err)
	}
	data, err := json.MarshalIndent(archivedFile{Task: t, Progress: progress}, "", "  ")
	if err != nil {
		return fmt.Errorf("task: encode archived task: %w", err)
	}
	if err := writeFileAtomic(s.archivePath(t.ID), append(data, '\n')); err != nil {
		return err
	}
	return nil
}

// ListArchived returns every detached task, oldest first by checkpoint stamp.
//
// A corrupt archive file is skipped rather than failing the whole list: one
// bad detachment must not hide every other resumable task. The caller can
// still reach the live checkpoint; the skip is silent because the file name
// carries the ID and a later detach overwrites it.
func (s *Store) ListArchived() ([]Snapshot, error) {
	dir := s.ArchiveDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("task: read %s: %w", dir, err)
	}
	var out []Snapshot
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		var file archivedFile
		if err := json.Unmarshal(data, &file); err != nil {
			continue
		}
		if strings.TrimSpace(file.Task.ID) == "" || !file.Task.Status.Valid() {
			continue
		}
		sanitizeArchived(&file)
		out = append(out, Snapshot{Task: file.Task, Progress: file.Progress, Found: true})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Task.UpdatedAt.Equal(out[j].Task.UpdatedAt) {
			return out[i].Task.ID < out[j].Task.ID
		}
		return out[i].Task.UpdatedAt.Before(out[j].Task.UpdatedAt)
	})
	return out, nil
}

// LoadArchived returns one detached task by ID.
func (s *Store) LoadArchived(id string) (Snapshot, error) {
	data, err := os.ReadFile(s.archivePath(id))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Snapshot{}, nil
		}
		return Snapshot{}, fmt.Errorf("task: read archived %s: %w", id, err)
	}
	var file archivedFile
	if err := json.Unmarshal(data, &file); err != nil {
		return Snapshot{}, fmt.Errorf("%w: %v", ErrCorruptCheckpoint, err)
	}
	if strings.TrimSpace(file.Task.ID) == "" {
		return Snapshot{}, fmt.Errorf("%w: empty task id", ErrCorruptCheckpoint)
	}
	sanitizeArchived(&file)
	return Snapshot{Task: file.Task, Progress: file.Progress, Found: true}, nil
}

// sanitizeArchived repairs an archived task's optional structured context
// without discarding the task itself. The same rules as the live checkpoint
// apply: valid records survive, invalid ones are repaired or dropped, and a
// future context version is refused rather than guessed at.
func sanitizeArchived(file *archivedFile) {
	// The active role is optional continuity metadata: a damaged value is
	// dropped so the role-context fallback can supply the answer instead of
	// making the archived task unwritable.
	if file.Task.ActiveRole != "" && !file.Task.ActiveRole.Valid() {
		file.Task.ActiveRole = ""
	}
	if file.Progress.ActiveRole != "" && !file.Progress.ActiveRole.Valid() {
		file.Progress.ActiveRole = ""
	}
	context, changes := file.Task.Context.Sanitize(file.Task.ID)
	file.Task.Context = context
	if changes > 0 && file.Task.ContextWarning == "" {
		file.Task.ContextWarning = fmt.Sprintf("%d task context record(s) were invalid and repaired or dropped on load", changes)
	}
	if file.Progress.Context != nil {
		clean, _ := file.Progress.Context.Sanitize(file.Task.ID)
		if clean.Empty() {
			file.Progress.Context = nil
		} else {
			file.Progress.Context = &clean
		}
	}
	if file.Task.Handoff != nil {
		clean, changes, problem := sanitizeHandoff(file.Task.Handoff, file.Task)
		file.Task.Handoff = clean
		file.Progress.Handoff = clean
		if clean == nil && problem != "" && file.Task.ContextWarning == "" {
			file.Task.ContextWarning = "task handoff was not restored: " + problem
		}
		if changes > 0 && file.Task.ContextWarning == "" {
			file.Task.ContextWarning = fmt.Sprintf("%d task handoff reference(s) were invalid and repaired or dropped on load", changes)
		}
		if clean != nil && clean.Status == HandoffApplied {
			file.Task.ActiveRole = clean.To
			file.Progress.ActiveRole = clean.To
		}
	}
}

// RemoveArchived drops one detached task. Missing is not an error: a resume
// that already moved the task to live must not fail on the cleanup.
func (s *Store) RemoveArchived(id string) error {
	if err := os.Remove(s.archivePath(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("task: remove archived %s: %w", id, err)
	}
	return nil
}

// ClearActive removes the live checkpoint files, detaching the session without
// losing the archived copy.
//
// It is how a new session starts cleanly: the live slot is empty, so the next
// Begin creates a fresh task, while the detached task remains resumable via
// ListArchived. Missing files are not an error.
func (s *Store) ClearActive() error {
	for _, path := range []string{s.ProgressPath(), s.TaskPath()} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("task: clear %s: %w", path, err)
		}
	}
	return nil
}

// IsResumable reports whether a status is durable pending work.
//
// RUNNING is included because a crash-normalized task (RUNNING on disk) is
// restored as PAUSED before this is consulted; an in-memory RUNNING task is
// active in the current session, which is the attached half of resumable.
func IsResumable(status Status) bool {
	switch status {
	case StatusPending, StatusRunning, StatusPaused, StatusNeedsReview:
		return true
	default:
		return false
	}
}
