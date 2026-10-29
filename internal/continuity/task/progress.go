package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/budget"
	"github.com/PizenLabs/ohgo/internal/control/cognition"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/project"
)

// ProgressVersion is the on-disk schema version of progress.json. A reader
// refuses a checkpoint it does not understand rather than guessing at a shape,
// because a silently misread recovery artifact is worse than a reported one.
//
// Version 2 adds the persisted completion contract, pre-run target snapshot,
// and review reason. Version 1 checkpoints still load: they simply cannot
// authorize completion, because a task with no contract has no evidence
// boundary. They are resolvable only by an explicit human cancel/replace.
//
// Phase 9's evidenceState and audit fields are additive and optional. They do
// not change the meaning of an existing field, so the schema version stays 2;
// older readers ignore them and a reader of an older checkpoint sees nil. A
// field whose meaning changes would still require a version bump.
const ProgressVersion = 2

// File names inside the project-local .ohgo directory.
const (
	// TaskFileName is the human- and model-readable projection.
	TaskFileName = "task.md"
	// ProgressFileName is the machine-readable recovery checkpoint.
	ProgressFileName = "progress.json"
)

// Progress is the machine-readable checkpoint.
//
// It carries only what recovery needs. It is deliberately not a transcript: the
// session store already owns conversation history, and duplicating it here
// would make a second, competing source of truth.
type Progress struct {
	// Version is the schema version written.
	Version int `json:"version"`
	// TaskID identifies the task this checkpoint belongs to.
	TaskID string `json:"taskId"`
	// Status is the task status at checkpoint time.
	Status Status `json:"status"`
	// Phase is the coarse current stage.
	Phase string `json:"phase,omitempty"`
	// ActiveTodo is the id of the active todo, if any.
	ActiveTodo string `json:"activeTodo,omitempty"`
	// CompletedTodos lists the ids of finished todos.
	CompletedTodos []string `json:"completedTodos,omitempty"`
	// LastAction is the last meaningful action observed.
	LastAction string `json:"lastAction,omitempty"`
	// Evidence references the runtime observations that justify progress.
	Evidence []string `json:"evidence,omitempty"`
	// NextAction is the next safe action after recovery.
	NextAction string `json:"nextAction,omitempty"`
	// CheckpointAt orders checkpoints: an older stamp never overwrites a newer.
	CheckpointAt time.Time `json:"checkpointAt"`
	// Budget is the task's cumulative resource ledger. It is optional on
	// read: checkpoints written before Phase 5 carry no budget and resume
	// with a fresh ledger rather than failing.
	Budget *budget.State `json:"budget,omitempty"`
	// Contract is the authoritative completion contract. A non-terminal task
	// without one cannot complete; the missing boundary is reported rather
	// than replaced by a new prompt.
	Contract *contract.IntentContract `json:"contract,omitempty"`
	// Snapshot is the pre-run target evidence that belongs to Contract.
	Snapshot *agent.TargetSnapshot `json:"snapshot,omitempty"`
	// ReviewReason is the evidence-boundary explanation persisted when the
	// task entered NEEDS_REVIEW.
	ReviewReason string `json:"reviewReason,omitempty"`
	// EvidenceState is the bounded durable knowledge the runtime acquired. It
	// lets a resumed turn avoid rediscovering facts and lets a projection
	// explain what it retained or elided. It is evidence, never authority.
	EvidenceState *cognition.EvidenceState `json:"evidenceState,omitempty"`
	// Audit is the last context-projection audit, bounded. It is diagnostic
	// state: editing it cannot change authority, evidence, or completion.
	Audit string `json:"audit,omitempty"`
}

// ProgressFrom projects a task into its checkpoint form.
func ProgressFrom(t Task) Progress {
	p := Progress{
		Version:      ProgressVersion,
		TaskID:       t.ID,
		Status:       t.Status,
		Phase:        t.Phase,
		CheckpointAt: t.UpdatedAt,
	}
	if active, ok := t.ActiveTodo(); ok {
		p.ActiveTodo = active.ID
	}
	for _, todo := range t.Todos {
		if todo.Status == TodoDone {
			p.CompletedTodos = append(p.CompletedTodos, todo.ID)
			if todo.Evidence != "" {
				p.Evidence = append(p.Evidence, todo.Evidence)
			}
		}
	}
	p.NextAction = t.NextAction
	if t.Contract != nil {
		cloned := cloneContract(*t.Contract)
		p.Contract = &cloned
	}
	if t.Snapshot != nil {
		cloned := cloneSnapshot(*t.Snapshot)
		p.Snapshot = &cloned
	}
	p.ReviewReason = t.ReviewReason
	return p
}

// Checkpoint errors, matched with errors.Is.
var (
	// ErrCorruptCheckpoint means progress.json could not be decoded.
	ErrCorruptCheckpoint = errors.New("task: corrupt progress checkpoint")
	// ErrCheckpointVersion means progress.json carries a schema this build
	// does not understand.
	ErrCheckpointVersion = errors.New("task: unsupported progress checkpoint version")
	// ErrStaleCheckpoint means a write carried an older stamp than the
	// checkpoint already on disk.
	ErrStaleCheckpoint = errors.New("task: stale checkpoint refused")
)

// Store persists the task artifact and its checkpoint under <root>/.ohgo.
//
// It is the single writer for both files. Writes are atomic (temp-then-rename)
// so a crash mid-write can never leave a half-written checkpoint a later run
// would refuse to read or, worse, misread as a different task.
type Store struct {
	root string
}

// NewStore returns a store rooted at a project root.
func NewStore(root string) *Store { return &Store{root: root} }

// Root returns the project root the store writes under.
func (s *Store) Root() string { return s.root }

// Dir returns the project-local .ohgo directory.
func (s *Store) Dir() string { return filepath.Join(s.root, project.DirName) }

// TaskPath returns the projection path.
func (s *Store) TaskPath() string { return filepath.Join(s.Dir(), TaskFileName) }

// ProgressPath returns the checkpoint path.
func (s *Store) ProgressPath() string { return filepath.Join(s.Dir(), ProgressFileName) }

// Save writes the markdown projection and the JSON checkpoint, deriving the
// checkpoint from the task.
func (s *Store) Save(t Task) error {
	return s.SaveCheckpoint(t, ProgressFrom(t))
}

// SaveCheckpoint writes the markdown projection and a caller-built checkpoint.
//
// It exists so the controller can record a "last meaningful action" that is
// recovery context rather than part of the task contract. It refuses a write
// whose checkpoint stamp predates what is already on disk: a resume that raced
// a newer checkpoint must never roll recovery backwards. The markdown write is
// best-effort — a projection failure must not lose the authoritative
// checkpoint — so its error is returned but the checkpoint has already landed.
func (s *Store) SaveCheckpoint(t Task, progress Progress) error {
	if err := t.Validate(); err != nil {
		return err
	}
	if progress.TaskID == "" {
		progress.TaskID = t.ID
	}
	if progress.Status == "" {
		progress.Status = t.Status
	}
	if !progress.CheckpointAt.IsZero() && t.UpdatedAt.After(progress.CheckpointAt) {
		progress.CheckpointAt = t.UpdatedAt
	}
	if progress.CheckpointAt.IsZero() {
		progress.CheckpointAt = time.Now().UTC()
	}
	if progress.Version == 0 {
		progress.Version = ProgressVersion
	}

	existing, found, err := s.LoadProgress()
	if err != nil {
		return err
	}
	if found && existing.CheckpointAt.After(progress.CheckpointAt) {
		return fmt.Errorf("%w: on-disk %s, incoming %s",
			ErrStaleCheckpoint,
			existing.CheckpointAt.UTC().Format(time.RFC3339Nano),
			progress.CheckpointAt.UTC().Format(time.RFC3339Nano))
	}

	dir := s.Dir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("task: create %s: %w", dir, err)
	}

	progressData, err := json.MarshalIndent(progress, "", "  ")
	if err != nil {
		return fmt.Errorf("task: encode progress: %w", err)
	}
	// The checkpoint lands first: it is the recovery artifact, and a crash
	// between the two writes leaves an older human projection over a valid
	// checkpoint, which recovery reads correctly.
	if err := writeFileAtomic(s.ProgressPath(), append(progressData, '\n')); err != nil {
		return err
	}
	if err := writeFileAtomic(s.TaskPath(), []byte(RenderMarkdown(t))); err != nil {
		return fmt.Errorf("task: write projection: %w", err)
	}
	return nil
}

// Snapshot is what a load found on disk.
type Snapshot struct {
	// Task is the recovered task. When only the checkpoint exists it is built
	// from the checkpoint fields.
	Task Task
	// Progress is the checkpoint, zero when none was present.
	Progress Progress
	// Found reports whether either artifact existed.
	Found bool
}

// Load reads the checkpoint and the projection.
//
// A corrupt checkpoint is reported but does not hide a readable projection: the
// returned Snapshot still carries the task parsed from task.md, so the caller
// can choose to recover from the human artifact. An unreadable projection with
// a valid checkpoint degrades the same way.
func (s *Store) Load() (Snapshot, error) {
	progress, progressFound, progressErr := s.LoadProgress()
	tm, mdFound, mdErr := s.LoadMarkdown()
	if !progressFound && !mdFound {
		return Snapshot{}, nil
	}

	t := tm
	if !mdFound || mdErr != nil {
		// Build a minimal task from the checkpoint so recovery is still
		// possible when the projection is gone.
		t = Task{ID: progress.TaskID, Status: progress.Status}
	}
	if progressFound && progressErr == nil {
		if progress.TaskID != "" {
			t.ID = progress.TaskID
		}
		t.Status = progress.Status
		t.Phase = progress.Phase
		t.NextAction = progress.NextAction
		if progress.Contract != nil {
			cloned := cloneContract(*progress.Contract)
			t.Contract = &cloned
		}
		if progress.Snapshot != nil {
			cloned := cloneSnapshot(*progress.Snapshot)
			t.Snapshot = &cloned
		}
		t.ReviewReason = progress.ReviewReason
		if progress.CheckpointAt.After(t.UpdatedAt) {
			t.UpdatedAt = progress.CheckpointAt
		}
	}

	snap := Snapshot{Task: t, Progress: progress, Found: true}
	switch {
	case progressErr != nil:
		return snap, progressErr
	case mdErr != nil:
		return snap, mdErr
	}
	return snap, nil
}

// LoadProgress reads and validates the JSON checkpoint.
func (s *Store) LoadProgress() (Progress, bool, error) {
	data, err := os.ReadFile(s.ProgressPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Progress{}, false, nil
		}
		return Progress{}, false, fmt.Errorf("task: read %s: %w", s.ProgressPath(), err)
	}
	var progress Progress
	if err := json.Unmarshal(data, &progress); err != nil {
		return Progress{}, true, fmt.Errorf("%w: %v", ErrCorruptCheckpoint, err)
	}
	if progress.Version == 0 {
		return Progress{}, true, fmt.Errorf("%w: missing version", ErrCorruptCheckpoint)
	}
	if progress.Version > ProgressVersion {
		return progress, true, fmt.Errorf("%w: %d > %d", ErrCheckpointVersion, progress.Version, ProgressVersion)
	}
	if progress.TaskID == "" {
		return progress, true, fmt.Errorf("%w: empty task id", ErrCorruptCheckpoint)
	}
	if !progress.Status.Valid() {
		return progress, true, fmt.Errorf("%w: unknown status %q", ErrCorruptCheckpoint, progress.Status)
	}
	return progress, true, nil
}

// LoadMarkdown reads and parses the projection.
func (s *Store) LoadMarkdown() (Task, bool, error) {
	data, err := os.ReadFile(s.TaskPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Task{}, false, nil
		}
		return Task{}, false, fmt.Errorf("task: read %s: %w", s.TaskPath(), err)
	}
	t, err := ParseMarkdown(string(data))
	if err != nil {
		return Task{}, true, err
	}
	return t, true, nil
}

// writeFileAtomic writes data to path via a temp file in the same directory.
//
// The temp file must share the directory: rename is only atomic within one
// filesystem, and a temp in /tmp would turn the install into a cross-device
// copy that a crash could truncate.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("task: create temp for %s: %w", path, err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("task: write %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("task: close %s: %w", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("task: install %s: %w", path, err)
	}
	return nil
}

// NormalizeRecovered returns a task ready to resume, or the task unchanged
// when it needs no adjustment.
//
// A task left RUNNING on disk means the process stopped without a terminal
// transition — a crash, a kill, a closed terminal. It is restored as PAUSED,
// never COMPLETED: the runtime has no evidence the objective was reached, and
// claiming success would be a lie. A task already paused stays paused, and a
// terminal task is returned untouched.
func NormalizeRecovered(t Task) Task {
	if t.Status == StatusRunning || t.Status == StatusPending {
		t.Status = StatusPaused
	}
	return t
}
