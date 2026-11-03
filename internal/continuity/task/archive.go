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
	progress.Timeline = cloneTimeline(t.Timeline)
	if t.Completion != nil {
		cloned := t.Completion.Clone()
		progress.Completion = &cloned
	} else {
		progress.Completion = nil
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
	if err := s.writeFile(s.archivePath(t.ID), append(data, '\n')); err != nil {
		return err
	}
	return nil
}

// ArchiveProblem is one detached-task file that could not be listed.
//
// A corrupt archive is not silently indistinguishable from a task that never
// existed: the store records the file and the reason so the host can surface a
// lost resumable task instead of hiding it.
type ArchiveProblem struct {
	// Path is the unreadable archive file.
	Path string
	// Reason explains why it was skipped.
	Reason string
}

// ArchiveProblems returns the corrupt archive files observed by the most recent
// listing. It is diagnostic only: naming a lost archive never grants authority
// and never fabricates a task.
func (s *Store) ArchiveProblems() []ArchiveProblem {
	s.problemsMu.Lock()
	defer s.problemsMu.Unlock()
	return append([]ArchiveProblem(nil), s.archiveProblems...)
}

// recordProblems replaces the diagnostic list from one listing.
func (s *Store) recordProblems(problems []ArchiveProblem) {
	s.problemsMu.Lock()
	s.archiveProblems = problems
	s.problemsMu.Unlock()
}

// ListArchived returns every detached task, oldest first by checkpoint stamp.
//
// A corrupt archive file is skipped rather than failing the whole list: one
// bad detachment must not hide every other resumable task. The skip is
// recorded as an ArchiveProblem so the loss is observable rather than silent.
func (s *Store) ListArchived() ([]Snapshot, error) {
	dir := s.ArchiveDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.recordProblems(nil)
			return nil, nil
		}
		return nil, fmt.Errorf("task: read %s: %w", dir, err)
	}
	var (
		out      []Snapshot
		problems []ArchiveProblem
	)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			problems = append(problems, ArchiveProblem{Path: path, Reason: "the file could not be read"})
			continue
		}
		var file archivedFile
		if err := json.Unmarshal(data, &file); err != nil {
			problems = append(problems, ArchiveProblem{Path: path, Reason: "the file is not a valid detached task"})
			continue
		}
		if strings.TrimSpace(file.Task.ID) == "" || !file.Task.Status.Valid() {
			problems = append(problems, ArchiveProblem{Path: path, Reason: "the file carries no valid task identity or status"})
			continue
		}
		if file.Progress.Version > ProgressVersion {
			problems = append(problems, ArchiveProblem{
				Path:   path,
				Reason: fmt.Sprintf("checkpoint version %d is newer than this build supports (%d)", file.Progress.Version, ProgressVersion),
			})
			continue
		}
		repairs := sanitizeArchived(&file)
		out = append(out, Snapshot{
			Task:     file.Task,
			Progress: file.Progress,
			Recovery: archiveRecoveryReport(file.Task, repairs),
			Found:    true,
		})
	}
	s.recordProblems(problems)
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
	if file.Progress.Version > ProgressVersion {
		return Snapshot{}, fmt.Errorf("%w: archived %d > %d", ErrCheckpointVersion, file.Progress.Version, ProgressVersion)
	}
	repairs := sanitizeArchived(&file)
	return Snapshot{
		Task:     file.Task,
		Progress: file.Progress,
		Recovery: archiveRecoveryReport(file.Task, repairs),
		Found:    true,
	}, nil
}

// archiveRepairs records what sanitizing one archived task changed. It is the
// input to the recovery classification, so a repaired archive can never report
// INTACT.
type archiveRepairs struct {
	count   int
	detail  string
	missing []string
}

func (r *archiveRepairs) add(detail string, missing ...string) {
	r.count++
	if r.detail == "" {
		r.detail = detail
	}
	r.missing = append(r.missing, missing...)
}

// archiveRecoveryReport classifies one archived task from the repairs it needed.
//
// It describes structural recovery only: it never claims that a filesystem
// artifact still exists or that an in-flight tool operation can be replayed.
func archiveRecoveryReport(t Task, repairs archiveRepairs) RecoveryReport {
	report := RecoveryReport{State: RecoveryIntact}
	if repairs.count > 0 {
		report.State = RecoveryReconstructed
		report.Detail = repairs.detail
		report.Missing = uniqueStrings(repairs.missing)
	}
	if report.State != RecoveryBlocked && !t.Status.IsTerminal() && t.Contract == nil {
		if report.Detail != "" {
			report.Detail += "; "
		}
		report.Detail += "the task has no completion contract; it can be cancelled or replaced, not completed"
		report.State = RecoveryNeedsHuman
	}
	if report.State == RecoveryIntact {
		report.Detail = "detached task restored from its archive"
	}
	for _, todo := range t.Todos {
		if todo.Status != TodoDone {
			report.Outstanding++
		}
	}
	return report
}

// sanitizeArchived repairs an archived task's optional structured context
// without discarding the task itself. The same rules as the live checkpoint
// apply: valid records survive, invalid ones are repaired or dropped, and a
// future context version is refused rather than guessed at. It returns what it
// changed so recovery can classify the archive truthfully.
func sanitizeArchived(file *archivedFile) archiveRepairs {
	var repairs archiveRepairs
	// The active role is optional continuity metadata: a damaged value is
	// dropped so the role-context fallback can supply the answer instead of
	// making the archived task unwritable.
	if file.Task.ActiveRole != "" && !file.Task.ActiveRole.Valid() {
		file.Task.ActiveRole = ""
		repairs.add("the archived active role was invalid and was dropped", "active role")
	}
	if file.Progress.ActiveRole != "" && !file.Progress.ActiveRole.Valid() {
		file.Progress.ActiveRole = ""
		repairs.add("the archived checkpoint role was invalid and was dropped", "active role")
	}
	context, changes := file.Task.Context.Sanitize(file.Task.ID)
	file.Task.Context = context
	if changes > 0 {
		if file.Task.ContextWarning == "" {
			file.Task.ContextWarning = fmt.Sprintf("%d task context record(s) were invalid and repaired or dropped on load", changes)
		}
		repairs.add(fmt.Sprintf("%d archived context record(s) were invalid and were repaired or dropped", changes), "structured context")
	}
	if file.Progress.Context != nil {
		clean, changes := file.Progress.Context.Sanitize(file.Task.ID)
		if changes > 0 {
			repairs.add(fmt.Sprintf("%d archived checkpoint context record(s) were invalid and were repaired or dropped", changes), "structured context")
		}
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
		if clean == nil && problem != "" {
			if file.Task.ContextWarning == "" {
				file.Task.ContextWarning = "task handoff was not restored: " + problem
			}
			repairs.add("the archived handoff was not restored: "+problem, "handoff record")
		}
		if changes > 0 {
			if file.Task.ContextWarning == "" {
				file.Task.ContextWarning = fmt.Sprintf("%d task handoff reference(s) were invalid and repaired or dropped on load", changes)
			}
			repairs.add(fmt.Sprintf("%d archived handoff reference(s) were invalid and were repaired or dropped", changes), "handoff record")
		}
		if clean != nil && clean.Status == HandoffApplied {
			file.Task.ActiveRole = clean.To
			file.Progress.ActiveRole = clean.To
		}
	}
	if len(file.Task.Timeline) == 0 && len(file.Progress.Timeline) > 0 {
		clean, changes := sanitizeTimeline(file.Progress.Timeline)
		file.Task.Timeline = cloneTimeline(clean)
		if changes > 0 {
			repairs.add(fmt.Sprintf("%d archived timeline event(s) were invalid and were repaired or dropped", changes), "timeline")
		}
	}
	if len(file.Task.Timeline) > 0 {
		clean, changes := sanitizeTimeline(file.Task.Timeline)
		file.Task.Timeline = clean
		file.Progress.Timeline = cloneTimeline(clean)
		if changes > 0 {
			if file.Task.ContextWarning == "" {
				file.Task.ContextWarning = fmt.Sprintf("%d task timeline event(s) were invalid and repaired or dropped on load", changes)
			}
			repairs.add(fmt.Sprintf("%d archived timeline event(s) were invalid and were repaired or dropped", changes), "timeline")
		}
	}
	if file.Task.Completion == nil && file.Progress.Completion != nil && file.Task.Status == StatusCompleted {
		cloned := file.Progress.Completion.Clone()
		file.Task.Completion = &cloned
	}
	if file.Task.Status != StatusCompleted && file.Task.Completion != nil {
		if file.Task.ContextWarning == "" {
			file.Task.ContextWarning = "task completion proof was not restored: it belongs to a non-completed task"
		}
		repairs.add("a completion proof for a non-completed task was dropped", "completion proof")
		file.Task.Completion = nil
		file.Progress.Completion = nil
	}
	if file.Task.Status == StatusCompleted && file.Task.Completion != nil {
		if err := validateCompletionProofShape(*file.Task.Completion); err != nil {
			if file.Task.ContextWarning == "" {
				file.Task.ContextWarning = "task completion proof was not restored: " + err.Error()
			}
			repairs.add("the archived completion proof was invalid: "+err.Error(), "completion proof")
			file.Task.Completion = nil
			file.Progress.Completion = nil
		}
	}
	return repairs
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
