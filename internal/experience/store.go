package experience

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// StoreVersion is the on-disk schema version of one experience document.
//
// A reader refuses a newer document rather than guessing at a shape. An older
// document is accepted: additive fields simply read as zero values. This is the
// same read-old/refuse-newer rule the task checkpoint follows.
const StoreVersion = 1

// MaxRecords bounds one scope document. Active records are retained first; a
// bounded store is a briefing, not a transcript.
const MaxRecords = 128

// Errors, matched with errors.Is.
var (
	// ErrUnsupportedVersion means the file was written by a newer schema.
	ErrUnsupportedVersion = errors.New("experience: unsupported store version")
	// ErrInvalidRecord means a record violates the persisted shape.
	ErrInvalidRecord = errors.New("experience: invalid record")
)

// Document is one scope's persisted experience.
type Document struct {
	Version   int       `json:"version"`
	Records   []Record  `json:"records,omitempty"`
	UpdatedAt time.Time `json:"updatedAt,omitempty"`
}

// Store persists one experience scope. It is the only writer for its file.
//
// write is a function field so tests can inject a deterministic failure at the
// persistence boundary. Production uses temp-then-rename in the destination
// directory; a crash can leave the old file, never a half-written one.
type Store struct {
	path  string
	write func(path string, data []byte) error
	mu    sync.Mutex
}

// NewStore returns a store for one file path. An empty path means the scope is
// memory-only: Load returns empty and Save is a no-op.
func NewStore(path string) *Store {
	return &Store{path: path, write: writeFileAtomic}
}

// Path returns the file this store owns.
func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// Load reads and sanitizes one document.
//
// A malformed file is ignored safely: the runtime continues with empty
// experience rather than failing a turn. A newer schema is refused explicitly so
// a downgrade cannot erase fields it does not understand. Every dropped or
// repaired record is reported as a bounded warning.
func (s *Store) Load() (Document, []string, error) {
	if s == nil || strings.TrimSpace(s.path) == "" {
		return Document{Version: StoreVersion}, nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Document{Version: StoreVersion}, nil, nil
		}
		return Document{Version: StoreVersion}, []string{"experience: cannot read " + s.path + ": " + err.Error()}, nil
	}
	var doc Document
	if err := json.Unmarshal(data, &doc); err != nil {
		return Document{Version: StoreVersion}, []string{"experience: ignoring malformed " + s.path}, nil
	}
	if doc.Version > StoreVersion {
		return Document{Version: StoreVersion}, []string{
			fmt.Sprintf("experience: ignoring %s at version %d (this build supports %d)", s.path, doc.Version, StoreVersion),
		}, ErrUnsupportedVersion
	}
	if doc.Version == 0 {
		doc.Version = StoreVersion
	}
	records, warnings := sanitizeRecords(doc.Records)
	doc.Records = records
	return doc, warnings, nil
}

// Save writes a versioned document atomically.
func (s *Store) Save(doc Document) error {
	if s == nil || strings.TrimSpace(s.path) == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if doc.Version == 0 {
		doc.Version = StoreVersion
	}
	if doc.Version > StoreVersion {
		return fmt.Errorf("%w: %d", ErrUnsupportedVersion, doc.Version)
	}
	records, warnings := sanitizeRecords(doc.Records)
	if len(warnings) > 0 {
		return fmt.Errorf("%w: %s", ErrInvalidRecord, strings.Join(warnings, "; "))
	}
	doc.Records = records
	doc.UpdatedAt = time.Now().UTC()
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("experience: encode %s: %w", s.path, err)
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("experience: create %s: %w", filepath.Dir(s.path), err)
	}
	write := s.write
	if write == nil {
		write = writeFileAtomic
	}
	if err := write(s.path, append(data, '\n')); err != nil {
		return err
	}
	return nil
}

// sanitizeRecords validates and bounds the persisted set.
func sanitizeRecords(records []Record) ([]Record, []string) {
	if len(records) == 0 {
		return nil, nil
	}
	var warnings []string
	seen := make(map[string]int, len(records))
	out := make([]Record, 0, len(records))
	for _, record := range records {
		clean, changed, valid := sanitizeRecord(record)
		if !valid {
			warnings = append(warnings, "dropped an invalid experience record")
			continue
		}
		key := recordIdentity(clean)
		if index, ok := seen[key]; ok {
			warnings = append(warnings, "dropped a duplicate experience record")
			if keepRecord(clean, out[index]) {
				out[index] = clean
			}
			continue
		}
		seen[key] = len(out)
		out = append(out, clean)
		if changed {
			warnings = append(warnings, "repaired a bounded experience record")
		}
	}
	out = trimRecords(out)
	return out, warnings
}

// cloneDocument returns a deep-enough copy for rollback after a failed write.
// Record values are copied, and the slices a mutation can append to are copied
// too, so an in-memory update cannot survive a persistence failure.
func cloneDocument(doc Document) Document {
	out := doc
	out.Records = make([]Record, len(doc.Records))
	for i, record := range doc.Records {
		out.Records[i] = cloneRecord(record)
	}
	return out
}

// cloneRecord copies the slices a lifecycle transition can append to.
func cloneRecord(record Record) Record {
	out := record
	out.Incident.Evidence = append([]string(nil), record.Incident.Evidence...)
	out.Candidate.PreferredTools = append([]string(nil), record.Candidate.PreferredTools...)
	out.Candidate.Validation = append([]Validation(nil), record.Candidate.Validation...)
	out.Candidate.Transitions = append([]Transition(nil), record.Candidate.Transitions...)
	return out
}

// recordIdentity is the stable uniqueness key of one scoped record.
func recordIdentity(record Record) string {
	return strings.Join([]string{
		record.Scope.String(),
		record.ScopeKey,
		record.Candidate.Key,
	}, "|")
}

// keepRecord reports whether incoming should replace existing in a duplicate
// collision. More observations win, then a newer update.
func keepRecord(incoming, existing Record) bool {
	if incoming.Candidate.Observations != existing.Candidate.Observations {
		return incoming.Candidate.Observations > existing.Candidate.Observations
	}
	return incoming.UpdatedAt.After(existing.UpdatedAt)
}

// sanitizeRecord validates one record and bounds every persisted string.
func sanitizeRecord(record Record) (Record, bool, bool) {
	changed := false
	if record.Scope == "" {
		record.Scope = ScopeProject
		changed = true
	}
	if record.Candidate.Scope == "" {
		record.Candidate.Scope = record.Scope
		changed = true
	}
	if record.Candidate.Scope != record.Scope {
		return Record{}, false, false
	}
	if strings.TrimSpace(record.ID) == "" ||
		strings.TrimSpace(record.ScopeKey) == "" ||
		strings.TrimSpace(record.Candidate.Key) == "" ||
		strings.TrimSpace(record.Lesson.Statement) == "" {
		return Record{}, false, false
	}
	if !record.Scope.Valid() || !record.Candidate.Kind.Valid() || !record.Candidate.Status.Valid() {
		return Record{}, false, false
	}
	if record.Candidate.Context.Key() == "" {
		record.Candidate.Context = record.Lesson.Context
		changed = true
	}
	bounded := func(value string, limit int) string {
		next := boundText(value, limit)
		if next != value {
			changed = true
		}
		return next
	}
	record.Incident.Summary = bounded(record.Incident.Summary, 512)
	record.Lesson.Statement = bounded(record.Lesson.Statement, 512)
	record.Candidate.Statement = bounded(record.Candidate.Statement, 512)
	record.Candidate.Hint = bounded(record.Candidate.Hint, 320)
	record.Candidate.RetirementReason = bounded(record.Candidate.RetirementReason, 320)
	record.Lesson.Regression.Name = bounded(record.Lesson.Regression.Name, 256)
	record.Lesson.Regression.Description = bounded(record.Lesson.Regression.Description, 512)
	record.Candidate.Regression.Name = bounded(record.Candidate.Regression.Name, 256)
	record.Candidate.Regression.Description = bounded(record.Candidate.Regression.Description, 512)
	record.Candidate.PreferredTools = boundedTools(record.Candidate.PreferredTools)
	if len(record.Incident.Evidence) > maxEventRefs {
		record.Incident.Evidence = record.Incident.Evidence[:maxEventRefs]
		changed = true
	}
	if len(record.Candidate.Validation) > 32 {
		record.Candidate.Validation = record.Candidate.Validation[:32]
		changed = true
	}
	if len(record.Candidate.Transitions) > 32 {
		record.Candidate.Transitions = record.Candidate.Transitions[:32]
		changed = true
	}
	return record, changed, true
}

// trimRecords bounds the store. Active records are retained first so a
// historical candidate cannot evict a currently applied lesson.
func trimRecords(records []Record) []Record {
	if len(records) <= MaxRecords {
		return records
	}
	sort.SliceStable(records, func(i, j int) bool {
		if records[i].Candidate.Status.Active() != records[j].Candidate.Status.Active() {
			return records[i].Candidate.Status.Active()
		}
		if records[i].Candidate.Observations != records[j].Candidate.Observations {
			return records[i].Candidate.Observations > records[j].Candidate.Observations
		}
		return records[i].UpdatedAt.After(records[j].UpdatedAt)
	})
	return records[:MaxRecords]
}

// String renders a scope for persistence and diagnostics.
func (s Scope) String() string {
	if s == "" {
		return string(ScopeProject)
	}
	return string(s)
}

// writeFileAtomic writes data to path via a temp file in the same directory.
//
// The temp file must share the directory: rename is only atomic within one
// filesystem, and a temp in /tmp would turn the install into a cross-device
// copy that a crash could truncate. The experience store has its own file
// lifecycle and schema; keeping the primitive local avoids making task
// persistence a dependency of the learning layer.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("experience: create temp for %s: %w", path, err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("experience: write %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("experience: close %s: %w", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("experience: install %s: %w", path, err)
	}
	return nil
}
