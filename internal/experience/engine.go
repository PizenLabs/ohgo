package experience

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/cognition"
)

// Errors, matched with errors.Is.
var (
	// ErrNotFound means the requested record does not exist.
	ErrNotFound = errors.New("experience: record not found")
	// ErrNotValidated means promotion was attempted before validation.
	ErrNotValidated = errors.New("experience: candidate is not validated")
	// ErrContradicted means contradictory evidence blocks promotion.
	ErrContradicted = errors.New("experience: candidate has contradictory evidence")
	// ErrNoRegression means a candidate has no regression link.
	ErrNoRegression = errors.New("experience: candidate has no regression coverage")
	// ErrInsufficientRecurrence means global promotion lacks observations.
	ErrInsufficientRecurrence = errors.New("experience: insufficient independent observations")
	// ErrNoStore means the requested scope has no configured store.
	ErrNoStore = errors.New("experience: scope store is not configured")
	// ErrGlobalPromotionOnly means a caller tried to observe directly into
	// global scope. Global records are written only by PromoteToGlobal.
	ErrGlobalPromotionOnly = errors.New("experience: global records require explicit validated promotion")
)

// Config configures an engine for one project/runtime context.
type Config struct {
	// ProjectRoot is the repository root. It supplies the default project file
	// path; it is never used as authority.
	ProjectRoot string
	// ProjectID is the stable project identity used to scope records.
	ProjectID string
	// TaskID scopes in-memory task experience.
	TaskID string
	// RuntimeKey scopes runtime experience. Empty uses "runtime".
	RuntimeKey string
	// ProjectPath overrides the project document path.
	ProjectPath string
	// RuntimePath is the optional runtime document path.
	RuntimePath string
	// GlobalPath is the optional global document path. Global experience is
	// never written unless this is explicitly configured.
	GlobalPath string
	// Now is the clock. Nil uses time.Now.
	Now func() time.Time
	// AutoPromote enables promotion after recurrence validation. It is false by
	// default so a host must opt into automatic advisory changes.
	AutoPromote bool
	// MinObservations is the number of independent matching observations
	// required before a candidate is validated. Values below 2 are raised to 2.
	MinObservations int
}

// LearnResult summarizes one learning pass.
type LearnResult struct {
	Events     int
	Candidates int
	Promoted   int
	Retired    int
	RecordIDs  []string
}

// Summary is the compact read-only projection used by inspection surfaces.
type Summary struct {
	ID             string
	Status         CandidateStatus
	Scope          Scope
	Kind           CandidateKind
	LessonKind     string
	Statement      string
	Hint           string
	Context        string
	Observations   int
	Contradictions int
	Regression     string
	LastValidated  time.Time
	RetiredAt      *time.Time
}

// Engine owns the experience lifecycle for one project/runtime context.
//
// It is safe for concurrent use. Observe and the explicit lifecycle methods are
// writers; Advise, List, and Explain are readers. The engine never calls a
// provider, executes a tool, or grants a capability.
type Engine struct {
	cfg Config
	now func() time.Time

	mu         sync.Mutex
	loaded     bool
	project    *Store
	runtime    *Store
	global     *Store
	projectDoc Document
	runtimeDoc Document
	globalDoc  Document
	taskDoc    Document
	warnings   []string
}

// New returns an engine. It performs no I/O until first use, so a cold start is
// always safe even when the project or state directories are unavailable.
func New(cfg Config) *Engine {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	projectPath := strings.TrimSpace(cfg.ProjectPath)
	if projectPath == "" && strings.TrimSpace(cfg.ProjectRoot) != "" {
		projectPath = filepath.Join(cfg.ProjectRoot, ".ohgo", "experience.json")
	}
	return &Engine{
		cfg:     cfg,
		now:     now,
		project: NewStore(projectPath),
		runtime: NewStore(strings.TrimSpace(cfg.RuntimePath)),
		global:  NewStore(strings.TrimSpace(cfg.GlobalPath)),
	}
}

// Observe extracts incidents, updates or creates candidates, validates
// recurrence, and promotes only when the host opted in and the record is
// regression-linked.
//
// The returned error reports a persistence or shape failure. A caller must treat
// it as diagnostic: a learning failure never changes the turn's authority,
// evidence, or completion.
func (e *Engine) Observe(obs Observation) (LearnResult, error) {
	if e == nil {
		return LearnResult{}, nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.ensureLoadedLocked(); err != nil {
		return LearnResult{}, err
	}
	scope := obs.Scope
	if !scope.Valid() {
		scope = ScopeProject
	}
	if scope == ScopeTask && strings.TrimSpace(obs.TaskID) == "" {
		scope = ScopeProject
	}
	if scope == ScopeGlobal {
		// Global experience has exactly one write path: PromoteToGlobal,
		// after the higher validation bar. A host cannot seed it by observing.
		return LearnResult{}, ErrGlobalPromotionOnly
	}
	if obs.ProjectID == "" {
		obs.ProjectID = e.cfg.ProjectID
	}
	if obs.TaskID == "" {
		obs.TaskID = e.cfg.TaskID
	}
	events := Extract(obs)
	if len(events) == 0 {
		return LearnResult{}, nil
	}
	now := e.now().UTC()
	ctx := ContextFor(obs)
	scopeKey := e.scopeKey(scope, obs)
	doc := e.docForLocked(scope)
	if doc == nil {
		return LearnResult{}, ErrNoStore
	}
	before := cloneDocument(*doc)

	result := LearnResult{Events: len(events)}
	newlyPromoted := 0
	newlyRetired := 0
	for _, event := range events {
		index := findRecordIndex(doc.Records, event.Key, scopeKey)
		if index < 0 {
			doc.Records = append(doc.Records, newRecord(event, ctx, scope, scopeKey, now))
			result.Candidates++
			result.RecordIDs = append(result.RecordIDs, doc.Records[len(doc.Records)-1].ID)
			continue
		}
		before := doc.Records[index].Candidate.Status
		doc.Records[index] = observeRecord(doc.Records[index], event, ctx, now, e.minObservations(), e.cfg.AutoPromote)
		result.RecordIDs = append(result.RecordIDs, doc.Records[index].ID)
		if !before.Active() && doc.Records[index].Candidate.Status.Active() {
			newlyPromoted++
		}
	}

	// Contradictions are evaluated after all events are known. A single
	// observation cannot both create a lesson and contradict it.
	for _, event := range events {
		for i := range doc.Records {
			before := doc.Records[i].Candidate.Status
			updated, contradicted := contradiction(doc.Records[i], event, ctx, now)
			if !contradicted {
				continue
			}
			doc.Records[i] = updated
			if before.Active() && !doc.Records[i].Candidate.Status.Active() {
				newlyRetired++
			}
		}
	}

	doc.Records = trimRecords(doc.Records)
	doc.UpdatedAt = now
	if err := e.saveLocked(scope); err != nil {
		*doc = before
		return result, err
	}
	result.Promoted = newlyPromoted
	result.Retired = newlyRetired
	return result, nil
}

// Advise returns promoted advisory guidance for one projection.
//
// Only active records with the same structural context are considered. A
// preferred tool that is not already offered is ignored. A recovery hint is
// emitted only when the current evidence state still carries a matching
// unresolved failure.
func (e *Engine) Advise(req cognition.AdviceRequest) cognition.Advice {
	if e == nil {
		return cognition.Advice{}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.ensureLoadedLocked(); err != nil {
		return cognition.Advice{}
	}
	ctx := ContextForShape(req.Shape, req.Shape.Authority.Workspace)
	toolNames := make(map[string]bool, len(req.Tools))
	for _, tool := range req.Tools {
		toolNames[tool.Name()] = true
	}
	failures := req.Evidence.FailureState()
	repeated := hasRepeatedProbe(req.Turns)
	candidates := e.activeForContextLocked(ctx)
	sort.SliceStable(candidates, func(i, j int) bool {
		ri, rj := scopeRank(candidates[i].Scope), scopeRank(candidates[j].Scope)
		if ri != rj {
			return ri < rj
		}
		if candidates[i].Candidate.Cost != candidates[j].Candidate.Cost {
			return candidates[i].Candidate.Cost.LowerThan(candidates[j].Candidate.Cost)
		}
		return candidates[i].Candidate.LastValidatedAt.After(candidates[j].Candidate.LastValidatedAt)
	})

	advice := cognition.Advice{}
	seenTools := make(map[string]bool)
	seenHints := make(map[string]bool)
	addHint := func(record Record) {
		hint := strings.TrimSpace(record.Candidate.Hint)
		if hint == "" || seenHints[hint] || len(advice.Hints) >= 3 {
			return
		}
		seenHints[hint] = true
		advice.Hints = append(advice.Hints, hint)
		advice.Matched = append(advice.Matched, record.ID)
	}
	for _, record := range candidates {
		switch record.Candidate.Kind {
		case CandidatePreferredToolOrder:
			for _, name := range record.Candidate.PreferredTools {
				if !toolNames[name] || seenTools[name] {
					continue
				}
				seenTools[name] = true
				advice.PreferredTools = append(advice.PreferredTools, name)
				if len(advice.PreferredTools) >= 8 {
					break
				}
			}
			if len(record.Candidate.PreferredTools) > 0 {
				for _, name := range record.Candidate.PreferredTools {
					if toolNames[name] {
						advice.Matched = append(advice.Matched, record.ID)
						break
					}
				}
			}
		case CandidateRecoveryHint:
			if len(failures.Records) > 0 && recoveryMatches(record, failures.Records) {
				addHint(record)
				continue
			}
			// An authority refusal is a control decision, not an execution
			// failure, so it never enters the failure ledger. Apply its guidance
			// only when this run actually contains a refusal of the same tool.
			if record.Lesson.Kind == EventAuthorityRefusal &&
				refusedToolInTurns(req.Turns, record.Incident.Tool) {
				addHint(record)
			}
		case CandidateDuplicateWarning:
			if repeated || (record.Lesson.Kind == EventHandoffRedundancy && req.Execution.ToolCalls > 0) {
				addHint(record)
			}
		case CandidateEvidencePriority:
			if req.Sufficiency.State != cognition.Sufficient || len(req.Sufficiency.Missing) > 0 {
				addHint(record)
			}
		case CandidateContextHint:
			if req.Execution.ToolCalls > cheapToolCalls {
				addHint(record)
			}
		}
	}
	advice.Matched = uniqueStrings(advice.Matched)
	return advice
}

// List returns compact read-only summaries for one scope. An invalid scope
// means every configured scope.
func (e *Engine) List(scope Scope) []Summary {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.ensureLoadedLocked(); err != nil {
		return nil
	}
	records := e.allRecordsLocked()
	out := make([]Summary, 0, len(records))
	for _, record := range records {
		if scope.Valid() && record.Scope != scope {
			continue
		}
		out = append(out, Summary{
			ID:             record.ID,
			Status:         record.Candidate.Status,
			Scope:          record.Scope,
			Kind:           record.Candidate.Kind,
			LessonKind:     record.Lesson.Kind,
			Statement:      record.Lesson.Statement,
			Hint:           record.Candidate.Hint,
			Context:        record.Candidate.Context.Summary(),
			Observations:   record.Candidate.Observations,
			Contradictions: record.Candidate.Contradictions,
			Regression:     record.Candidate.Regression.Name,
			LastValidated:  record.Candidate.LastValidatedAt,
			RetiredAt:      record.Candidate.RetiredAt,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Status.Active() != out[j].Status.Active() {
			return out[i].Status.Active()
		}
		return out[i].ID < out[j].ID
	})
	if len(out) > MaxRecords {
		out = out[:MaxRecords]
	}
	return out
}

// Explain returns the complete trace for one record.
func (e *Engine) Explain(id string) (Explanation, bool) {
	if e == nil {
		return Explanation{}, false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.ensureLoadedLocked(); err != nil {
		return Explanation{}, false
	}
	record, ok := e.recordLocked(id)
	if !ok {
		return Explanation{}, false
	}
	explanation := Explanation{
		Record:          record,
		Applied:         record.Candidate.Status.Active(),
		Regression:      record.Candidate.Regression,
		LastValidatedAt: record.Candidate.LastValidatedAt,
	}
	for _, validation := range record.Candidate.Validation {
		if validation.Kind == ValidationContradiction && !validation.Passed {
			explanation.Contradictions = append(explanation.Contradictions, validation.Ref)
		}
	}
	return explanation, true
}

// Promote validates and promotes a candidate explicitly. It refuses a
// contradiction or a missing regression link.
func (e *Engine) Promote(id string) (Record, error) {
	return e.mutateRecord(id, func(record Record, now time.Time) (Record, error) {
		if record.Candidate.Contradictions > 0 {
			return record, ErrContradicted
		}
		if record.Candidate.Status == StatusCandidate {
			if record.Candidate.Observations < e.minObservations() && !hasPassedRegression(record) {
				return record, ErrNotValidated
			}
			record = validateRecord(record, now)
		}
		if record.Candidate.Status != StatusValidated {
			return record, ErrNotValidated
		}
		if record.Candidate.Regression.Name == "" {
			return record, ErrNoRegression
		}
		if !hasPassedValidation(record) {
			return record, ErrNotValidated
		}
		return promoteRecord(record, now, "explicit validation and promotion"), nil
	})
}

// Validate appends one validation record. A passed regression or recurrence
// check can validate a candidate; a failed contradiction check adds a
// contradiction and may retire an active record.
func (e *Engine) Validate(id string, validation Validation) (Record, error) {
	if !validation.Kind.Valid() {
		return Record{}, fmt.Errorf("%w: unknown validation kind %q", ErrInvalidRecord, validation.Kind)
	}
	return e.mutateRecord(id, func(record Record, now time.Time) (Record, error) {
		validation.At = now
		record.Candidate.Validation = append(record.Candidate.Validation, validation)
		record.UpdatedAt = now
		if validation.Passed {
			record.Candidate.LastValidatedAt = now
		}
		if validation.Kind == ValidationContradiction && !validation.Passed {
			record.Candidate.Contradictions++
			record.Candidate.LastContradiction = validation.Ref
			if record.Candidate.Status.Active() &&
				record.Candidate.Contradictions >= maxInt(2, record.Candidate.Observations) {
				record = retireRecord(record, now, "explicit contradictory validation")
			}
			return record, nil
		}
		if validation.Passed && record.Candidate.Status == StatusCandidate &&
			record.Candidate.Observations >= e.minObservations() {
			record = validateRecord(record, now)
		}
		return record, nil
	})
}

// Retire removes a record from application and records why.
func (e *Engine) Retire(id, reason string) (Record, error) {
	if strings.TrimSpace(reason) == "" {
		reason = "retired by the host"
	}
	return e.mutateRecord(id, func(record Record, now time.Time) (Record, error) {
		return retireRecord(record, now, reason), nil
	})
}

// Reject marks a candidate as rejected without applying it.
func (e *Engine) Reject(id, reason string) (Record, error) {
	if strings.TrimSpace(reason) == "" {
		reason = "rejected by the host"
	}
	return e.mutateRecord(id, func(record Record, now time.Time) (Record, error) {
		return rejectRecord(record, now, reason), nil
	})
}

// PromoteToGlobal copies one active project record into global scope.
//
// This is the only global write path. It requires three independent
// observations, no contradictions, and a passed regression validation. A
// project record never becomes global merely because it exists.
func (e *Engine) PromoteToGlobal(id string) (Record, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.ensureLoadedLocked(); err != nil {
		return Record{}, err
	}
	if e.global == nil || strings.TrimSpace(e.global.Path()) == "" {
		return Record{}, ErrNoStore
	}
	record, ok := e.recordLocked(id)
	if !ok {
		return Record{}, ErrNotFound
	}
	if record.Scope != ScopeProject {
		return Record{}, fmt.Errorf("%w: only a project record may be promoted globally", ErrInvalidRecord)
	}
	if !record.Candidate.Status.Active() {
		return Record{}, ErrNotValidated
	}
	if record.Candidate.Observations < 3 {
		return Record{}, ErrInsufficientRecurrence
	}
	if record.Candidate.Contradictions > 0 {
		return Record{}, ErrContradicted
	}
	if !hasPassedRegression(record) {
		return Record{}, ErrNoRegression
	}
	now := e.now().UTC()
	global := record
	global.ID = stableID("experience", record.Candidate.Key+"|global")
	global.Scope = ScopeGlobal
	global.ScopeKey = "global"
	global.Candidate.Scope = ScopeGlobal
	global.Candidate.ScopeKey = "global"
	global.Candidate.Status = StatusPromoted
	global.Candidate.PromotedAt = &now
	global.Candidate.LastValidatedAt = now
	global.Candidate.Transitions = append(global.Candidate.Transitions, Transition{
		From:   record.Candidate.Status,
		To:     StatusPromoted,
		Reason: "explicit global promotion from project " + record.ScopeKey,
		At:     now,
	})
	global.CreatedAt = now
	global.UpdatedAt = now
	before := cloneDocument(e.globalDoc)
	if index := findRecordIndex(e.globalDoc.Records, global.Candidate.Key, "global"); index >= 0 {
		e.globalDoc.Records[index] = global
	} else {
		e.globalDoc.Records = append(e.globalDoc.Records, global)
	}
	e.globalDoc.Records = trimRecords(e.globalDoc.Records)
	if err := e.saveLocked(ScopeGlobal); err != nil {
		e.globalDoc = before
		return Record{}, err
	}
	return global, nil
}

// Warnings returns bounded diagnostics from the last load/save boundary.
func (e *Engine) Warnings() []string {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.warnings...)
}

// mutateRecord applies one lifecycle mutation atomically to its owning store.
func (e *Engine) mutateRecord(id string, fn func(Record, time.Time) (Record, error)) (Record, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.ensureLoadedLocked(); err != nil {
		return Record{}, err
	}
	now := e.now().UTC()
	record, ok := e.recordLocked(id)
	if !ok {
		return Record{}, ErrNotFound
	}
	updated, err := fn(record, now)
	if err != nil {
		return Record{}, err
	}
	scope, index := e.locateLocked(id)
	if index < 0 {
		return Record{}, ErrNotFound
	}
	doc := e.docForLocked(scope)
	if doc == nil || index >= len(doc.Records) {
		return Record{}, ErrNotFound
	}
	before := cloneDocument(*doc)
	doc.Records[index] = updated
	doc.UpdatedAt = now
	if err := e.saveLocked(scope); err != nil {
		*doc = before
		return Record{}, err
	}
	return updated, nil
}

// ensureLoadedLocked loads every configured store once.
func (e *Engine) ensureLoadedLocked() error {
	if e.loaded {
		return nil
	}
	e.loaded = true
	if e.project != nil && e.project.Path() != "" {
		doc, warnings, err := e.project.Load()
		e.warnings = append(e.warnings, warnings...)
		if err == nil {
			e.projectDoc = doc
		}
	}
	if e.runtime != nil && e.runtime.Path() != "" {
		doc, warnings, err := e.runtime.Load()
		e.warnings = append(e.warnings, warnings...)
		if err == nil {
			e.runtimeDoc = doc
		}
	}
	if e.global != nil && e.global.Path() != "" {
		doc, warnings, err := e.global.Load()
		e.warnings = append(e.warnings, warnings...)
		if err == nil {
			e.globalDoc = doc
		}
	}
	e.projectDoc.Records = e.filterScopeLocked(e.projectDoc.Records, ScopeProject)
	e.runtimeDoc.Records = e.filterScopeLocked(e.runtimeDoc.Records, ScopeRuntime)
	e.globalDoc.Records = e.filterScopeLocked(e.globalDoc.Records, ScopeGlobal)
	return nil
}

// filterScopeLocked drops records that do not belong to the configured scope
// key. A copied or moved store therefore cannot lend another project's
// experience to this one.
func (e *Engine) filterScopeLocked(records []Record, scope Scope) []Record {
	key := e.scopeKey(scope, Observation{})
	if key == "" {
		return nil
	}
	out := make([]Record, 0, len(records))
	for _, record := range records {
		if record.Scope != scope || record.ScopeKey != key {
			continue
		}
		out = append(out, record)
	}
	return out
}

// saveLocked writes one scope document.
func (e *Engine) saveLocked(scope Scope) error {
	doc := e.docForLocked(scope)
	if doc == nil {
		return nil
	}
	store := e.storeForLocked(scope)
	if store == nil || store.Path() == "" {
		return nil
	}
	return store.Save(*doc)
}

// docForLocked returns the document for a scope.
func (e *Engine) docForLocked(scope Scope) *Document {
	switch scope {
	case ScopeTask:
		return &e.taskDoc
	case ScopeProject:
		return &e.projectDoc
	case ScopeRuntime:
		return &e.runtimeDoc
	case ScopeGlobal:
		return &e.globalDoc
	default:
		return nil
	}
}

// storeForLocked returns the store for a scope.
func (e *Engine) storeForLocked(scope Scope) *Store {
	switch scope {
	case ScopeProject:
		return e.project
	case ScopeRuntime:
		return e.runtime
	case ScopeGlobal:
		return e.global
	default:
		return nil
	}
}

// scopeKey resolves the stable key of one scope for this engine and observation.
func (e *Engine) scopeKey(scope Scope, obs Observation) string {
	switch scope {
	case ScopeTask:
		if key := strings.TrimSpace(obs.TaskID); key != "" {
			return key
		}
		return strings.TrimSpace(e.cfg.TaskID)
	case ScopeProject:
		if key := strings.TrimSpace(obs.ProjectID); key != "" {
			return key
		}
		if key := strings.TrimSpace(e.cfg.ProjectID); key != "" {
			return key
		}
		if root := strings.TrimSpace(e.cfg.ProjectRoot); root != "" {
			return filepath.Clean(root)
		}
		return "project"
	case ScopeRuntime:
		if key := strings.TrimSpace(e.cfg.RuntimeKey); key != "" {
			return key
		}
		return "runtime"
	case ScopeGlobal:
		return "global"
	default:
		return ""
	}
}

// activeForContextLocked returns active records matching a context.
func (e *Engine) activeForContextLocked(ctx Context) []Record {
	records := e.allRecordsLocked()
	out := make([]Record, 0, len(records))
	for _, record := range records {
		if !record.Candidate.Status.Active() {
			continue
		}
		if record.Candidate.Context.Key() != ctx.Key() {
			continue
		}
		out = append(out, record)
	}
	return out
}

// allRecordsLocked returns every record, including task-scoped memory.
func (e *Engine) allRecordsLocked() []Record {
	out := make([]Record, 0, len(e.taskDoc.Records)+len(e.projectDoc.Records)+len(e.runtimeDoc.Records)+len(e.globalDoc.Records))
	out = append(out, e.taskDoc.Records...)
	out = append(out, e.projectDoc.Records...)
	out = append(out, e.runtimeDoc.Records...)
	out = append(out, e.globalDoc.Records...)
	return out
}

// recordLocked finds one record by id.
func (e *Engine) recordLocked(id string) (Record, bool) {
	for _, record := range e.allRecordsLocked() {
		if record.ID == id {
			return record, true
		}
	}
	return Record{}, false
}

// locateLocked finds one record by id and its owning scope/index.
func (e *Engine) locateLocked(id string) (Scope, int) {
	for i, record := range e.taskDoc.Records {
		if record.ID == id {
			return ScopeTask, i
		}
	}
	for i, record := range e.projectDoc.Records {
		if record.ID == id {
			return ScopeProject, i
		}
	}
	for i, record := range e.runtimeDoc.Records {
		if record.ID == id {
			return ScopeRuntime, i
		}
	}
	for i, record := range e.globalDoc.Records {
		if record.ID == id {
			return ScopeGlobal, i
		}
	}
	return "", -1
}

// findRecordIndex finds a record by candidate key and scope key.
func findRecordIndex(records []Record, key, scopeKey string) int {
	for i, record := range records {
		if record.Candidate.Key == key && record.ScopeKey == scopeKey {
			return i
		}
	}
	return -1
}

// minObservations returns the configured validation threshold.
func (e *Engine) minObservations() int {
	if e.cfg.MinObservations < 2 {
		return 2
	}
	return e.cfg.MinObservations
}

// recoveryMatches reports whether a promoted recovery hint applies to one of
// the current unresolved failures.
func recoveryMatches(record Record, failures []cognition.FailureRecord) bool {
	for _, failure := range failures {
		if record.Incident.Tool != "" && record.Incident.Tool != failure.Tool {
			continue
		}
		if record.Incident.FailureClass != "" &&
			string(failure.Class) != record.Incident.FailureClass {
			continue
		}
		return true
	}
	return false
}

// hasPassedRegression reports whether a record has a passed regression
// validation.
func hasPassedRegression(record Record) bool {
	for _, validation := range record.Candidate.Validation {
		if validation.Kind == ValidationRegression && validation.Passed {
			return true
		}
	}
	return false
}

// hasPassedValidation reports whether a record has recurrence or regression
// evidence sufficient to validate it.
func hasPassedValidation(record Record) bool {
	for _, validation := range record.Candidate.Validation {
		if !validation.Passed {
			continue
		}
		switch validation.Kind {
		case ValidationRegression:
			return true
		case ValidationRecurrence:
			if validation.Observations >= 2 {
				return true
			}
		}
	}
	return false
}

// refusedToolInTurns reports whether a completed turn contains a runtime
// refusal of one tool.
func refusedToolInTurns(turns []agent.Turn, tool string) bool {
	for _, turn := range turns {
		for _, outcome := range turn.Results {
			if !outcome.Refused() {
				continue
			}
			if tool == "" || outcome.ToolName == tool {
				return true
			}
		}
	}
	return false
}

// hasRepeatedProbe reports whether the completed turns contain an identical
// successful observation with no intervening mutation.
func hasRepeatedProbe(turns []agent.Turn) bool {
	type seen struct {
		position int
		ok       bool
	}
	first := make(map[string]seen)
	position := 0
	lastMutation := -1
	for _, turn := range turns {
		for _, outcome := range turn.Results {
			position++
			if ignoredTool(outcome.ToolName) {
				continue
			}
			if outcome.Result.Mutates ||
				(outcome.Authorization != nil && outcome.Authorization.Mutates) {
				lastMutation = position
			}
			signature := agent.CallSignature(outcome.ToolName, outcome.Args)
			if entry, ok := first[signature]; ok {
				if entry.ok && lastMutation < entry.position && !outcome.Result.IsError {
					return true
				}
				continue
			}
			first[signature] = seen{position: position, ok: !outcome.Result.IsError && !outcome.Refused()}
		}
	}
	return false
}
