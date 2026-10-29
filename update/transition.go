package update

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/PizenLabs/ohgo/state"
)

// TransitionSchemaVersion guards the on-disk shape of update-transition.json.
// A record written by a newer build is treated as unreadable rather than
// interpreted with missing fields.
const TransitionSchemaVersion = 1

// Operation names the version transition that actually happened.
type Operation string

const (
	// OperationUpdate means an explicit update installed a newer release.
	OperationUpdate Operation = "update"
	// OperationRollback means an installation was returned to an earlier
	// release by the existing rollback authority.
	OperationRollback Operation = "rollback"
)

// TransitionStatus reports whether the operation reached its committed state.
type TransitionStatus string

const (
	// TransitionCompleted means the operation finished and its outcome is
	// durable.
	TransitionCompleted TransitionStatus = "completed"
)

// NoticeState is the acknowledgement lifecycle of a post-update notice.
type NoticeState string

const (
	// NoticeNone means the transition carries no post-update presentation.
	// Rollbacks always use it.
	NoticeNone NoticeState = "none"
	// NoticePending means a successful update has not been acknowledged by
	// the interactive UI yet.
	NoticePending NoticeState = "pending"
	// NoticeAcknowledged means the user dismissed the post-update notice.
	NoticeAcknowledged NoticeState = "acknowledged"
)

// VersionTransition is the durable truth about the last explicit version
// transition.
//
// It is deliberately separate from update.json (which observes whether a newer
// release exists) and from changelog.json (which describes release content).
// Only an explicit update transaction may create a pending notice; a newer
// release merely existing never does.
type VersionTransition struct {
	SchemaVersion int              `json:"schema_version"`
	Operation     Operation        `json:"operation"`
	Status        TransitionStatus `json:"status"`
	From          string           `json:"from,omitempty"`
	To            string           `json:"to"`
	CompletedAt   time.Time        `json:"completed_at"`
	NoticeState   NoticeState      `json:"post_update_notice"`
}

// NewVersionTransition builds a transition for one completed operation.
//
// Only an update carries a pending post-update notice. A rollback is a version
// change too, but it must never produce the What's New surface: the user chose
// the earlier release and its notes are not news.
func NewVersionTransition(operation Operation, from, to string, at time.Time) VersionTransition {
	notice := NoticeNone
	if operation == OperationUpdate {
		notice = NoticePending
	}
	return VersionTransition{
		SchemaVersion: TransitionSchemaVersion,
		Operation:     operation,
		Status:        TransitionCompleted,
		From:          strings.TrimSpace(from),
		To:            strings.TrimSpace(to),
		CompletedAt:   at.UTC(),
		NoticeState:   notice,
	}
}

// PendingFor reports whether this transition authorizes a one-shot What's New
// presentation for the running release.
//
// The check is intentionally strict: the operation must be an update, the
// record must be complete, the notice must still be pending, and the target
// must be the version the user actually launched. Version inequality alone is
// never evidence of an update.
func (t VersionTransition) PendingFor(current string) bool {
	// A zero schema is the unversioned record a human may have written from
	// the documented shape; it is read as the current schema. Any other
	// value is a record this build does not understand.
	if t.SchemaVersion != 0 && t.SchemaVersion != TransitionSchemaVersion {
		return false
	}
	if t.Operation != OperationUpdate || t.Status != TransitionCompleted {
		return false
	}
	if t.NoticeState != NoticePending {
		return false
	}
	return sameRelease(t.To, current)
}

// sameRelease compares two release spellings by semantic precedence when both
// parse, and falls back to a trimmed exact match otherwise.
func sameRelease(a, b string) bool {
	left, right := strings.TrimSpace(a), strings.TrimSpace(b)
	if left == "" || right == "" {
		return false
	}
	if left == right {
		return true
	}
	parsedLeft, okLeft := ParseVersion(left)
	parsedRight, okRight := ParseVersion(right)
	return okLeft && okRight && parsedLeft.Equal(parsedRight)
}

// TransitionStore owns update-transition.json.
//
// Reads are tolerant: a missing, unreadable, malformed, or foreign record is
// reported as absent so a corrupt machine file can never block startup. The
// file is machine-scoped lifecycle state, not release content and not a cache
// of remote observations.
type TransitionStore struct {
	path string
}

// NewTransitionStore returns the store at the machine-level state path
// (~/.ohgo/update-transition.json).
func NewTransitionStore() (*TransitionStore, error) {
	path, err := state.Path("update-transition.json")
	if err != nil {
		return nil, fmt.Errorf("update transition path: %w", err)
	}
	return &TransitionStore{path: path}, nil
}

// NewTransitionStoreAt returns a store rooted at an explicit path. It exists
// so a test can keep lifecycle state in a temporary directory without touching
// the real machine state.
func NewTransitionStoreAt(path string) *TransitionStore {
	return &TransitionStore{path: path}
}

// Path returns the transition file path.
func (s *TransitionStore) Path() string { return s.path }

// Load reads the transition record. ok is false when the file is absent or
// unusable. A malformed record is treated as absent rather than partially
// applied.
func (s *TransitionStore) Load() (VersionTransition, bool) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return VersionTransition{}, false
	}
	var transition VersionTransition
	if err := json.Unmarshal(data, &transition); err != nil {
		return VersionTransition{}, false
	}
	if transition.SchemaVersion != 0 && transition.SchemaVersion != TransitionSchemaVersion {
		return VersionTransition{}, false
	}
	transition.SchemaVersion = TransitionSchemaVersion
	switch transition.Operation {
	case OperationUpdate, OperationRollback:
	default:
		return VersionTransition{}, false
	}
	if transition.Status != TransitionCompleted {
		return VersionTransition{}, false
	}
	switch transition.NoticeState {
	case NoticeNone, NoticePending, NoticeAcknowledged:
	default:
		return VersionTransition{}, false
	}
	if strings.TrimSpace(transition.To) == "" {
		return VersionTransition{}, false
	}
	transition.CompletedAt = transition.CompletedAt.UTC()
	return transition, true
}

// Save writes the transition atomically with owner-only permissions.
func (s *TransitionStore) Save(transition VersionTransition) error {
	transition.SchemaVersion = TransitionSchemaVersion
	transition.CompletedAt = transition.CompletedAt.UTC()
	data, err := json.MarshalIndent(transition, "", "  ")
	if err != nil {
		return fmt.Errorf("update: encode transition: %w", err)
	}
	data = append(data, '\n')
	return writeFileAtomic(s.path, data, 0o600)
}

// Record persists one completed transition. A successful update creates a
// pending post-update notice; a rollback clears it.
func (s *TransitionStore) Record(operation Operation, from, to string, at time.Time) error {
	return s.Save(NewVersionTransition(operation, from, to, at))
}

// Pending returns the pending post-update transition for current, if one
// exists. It performs no network access and never mutates the record.
func (s *TransitionStore) Pending(current string) (VersionTransition, bool) {
	transition, ok := s.Load()
	if !ok || !transition.PendingFor(current) {
		return VersionTransition{}, false
	}
	return transition, true
}

// Acknowledge consumes the pending post-update notice for current.
//
// It is the only way a notice becomes acknowledged. A mismatch, a rollback, an
// already-acknowledged record, or a corrupt file is a no-op. ok reports whether
// this call changed the record.
func (s *TransitionStore) Acknowledge(current string) (bool, error) {
	transition, ok := s.Load()
	if !ok || !transition.PendingFor(current) {
		return false, nil
	}
	transition.NoticeState = NoticeAcknowledged
	if err := s.Save(transition); err != nil {
		return false, err
	}
	return true, nil
}
