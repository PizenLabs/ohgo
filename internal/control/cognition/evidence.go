package cognition

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/external/knowledge"
)

// EvidenceStatus is what the runtime currently knows about one fact.
type EvidenceStatus string

// Evidence statuses.
const (
	// StatusKnown means the fact was observed directly.
	StatusKnown EvidenceStatus = "KNOWN"
	// StatusUnknown means the fact is not known yet.
	StatusUnknown EvidenceStatus = "UNKNOWN"
	// StatusConflicting means two observations disagree.
	StatusConflicting EvidenceStatus = "CONFLICTING"
	// StatusDerived means the fact was inferred from other evidence.
	StatusDerived EvidenceStatus = "DERIVED"
	// StatusRequired means the fact is needed but has not been acquired.
	StatusRequired EvidenceStatus = "REQUIRED"
)

// Retention is how much of an observation must survive projection.
//
// The vocabulary is deliberately small and semantic. A diagnostic and a bug
// window are retained exactly; a directory listing or a search hit is
// structural; a large unrelated file read is eligible for a bounded excerpt;
// external content keeps its provenance and untrusted envelope; transient
// exploration output may be compacted to a line.
type Retention string

// Retention classes.
const (
	RetentionExact      Retention = "exact"
	RetentionStructural Retention = "structural"
	RetentionDerived    Retention = "derived"
	RetentionDiagnostic Retention = "diagnostic"
	RetentionExternal   Retention = "external"
	RetentionTransient  Retention = "transient"
)

// Evidence bounds.
const (
	// MaxEvidenceValueBytes bounds one retained payload.
	MaxEvidenceValueBytes = 16 * 1024
	// MaxDiagnosticValueBytes bounds an exact error payload.
	MaxDiagnosticValueBytes = 8 * 1024
	// MaxEvidenceSummaryBytes bounds the model-facing one-line summary.
	MaxEvidenceSummaryBytes = 400
	// MaxEvidenceItems bounds the in-memory and persisted state. Trimming
	// removes low-value transient evidence first; required, conflicting, and
	// diagnostic items are retained longest.
	MaxEvidenceItems = 256
	// MaxEvidenceBytes bounds the retained payload total, so a checkpoint
	// cannot grow into a second transcript. Values are bounded per item and
	// the lowest-value items are evicted until the total fits.
	MaxEvidenceBytes = 64 * 1024
	// EvidenceVersion is the persisted schema version.
	EvidenceVersion = 1
)

// Source records where one observation came from.
type Source struct {
	// Tool is the tool that produced it.
	Tool string `json:"tool,omitempty"`
	// CallID is the tool call identity, for replay and audit.
	CallID string `json:"callId,omitempty"`
	// Path is a repository-relative file or directory, when one applies.
	Path string `json:"path,omitempty"`
	// StartLine and EndLine bound an exact window.
	StartLine int `json:"startLine,omitempty"`
	EndLine   int `json:"endLine,omitempty"`
	// URL, Provider, and Trust describe external evidence.
	URL      string `json:"url,omitempty"`
	Provider string `json:"provider,omitempty"`
	Trust    string `json:"trust,omitempty"`
	// Check names a verification kind, when the observation is a check.
	Check string `json:"check,omitempty"`
}

// EvidenceItem is one durable fact.
type EvidenceItem struct {
	// ID is a stable identity, e.g. "file:README.md".
	ID string `json:"id"`
	// Status is what the runtime knows.
	Status EvidenceStatus `json:"status"`
	// Retention is how the evidence survives projection.
	Retention Retention `json:"retention"`
	// Summary is a bounded model-facing description.
	Summary string `json:"summary"`
	// Value is the bounded retained payload. It is not sent whole by default;
	// projection chooses a view.
	Value string `json:"value,omitempty"`
	// Digest identifies the observed payload without duplicating it.
	Digest string `json:"digest,omitempty"`
	// Source records provenance and coordinates.
	Source Source `json:"source,omitempty"`
	// Complete reports that a listing or search result was not truncated.
	Complete bool `json:"complete,omitempty"`
	// Seq is the observation order, used to detect stale file state.
	Seq int `json:"seq,omitempty"`
	// Conflict explains a status of CONFLICTING.
	Conflict string `json:"conflict,omitempty"`
}

// Requirement is one evidence need declared by a task shape.
type Requirement struct {
	// Key is the stable identity the evidence must satisfy.
	Key string `json:"key"`
	// Reason explains why it matters.
	Reason string `json:"reason,omitempty"`
}

// EvidenceState is the durable knowledge the runtime has acquired.
//
// It is a projection of observations, never a transcript. Two tool calls that
// observe the same fact collapse to one item; a later observation of the same
// path either updates it (after a recorded mutation) or marks it conflicting
// (a change the runtime did not perform).
type EvidenceState struct {
	// Version is the persisted schema version.
	Version int `json:"version"`
	// Items are the facts, in observation order.
	Items []EvidenceItem `json:"items,omitempty"`
	// Requirements are the needs the task shape declared.
	Requirements []Requirement `json:"requirements,omitempty"`
	// Seq is the last observation sequence.
	Seq int `json:"seq,omitempty"`
	// ObservedCalls prevents replaying an already-observed tool outcome when a
	// projector is called more than once for the same turn.
	ObservedCalls map[string]bool `json:"observedCalls,omitempty"`
	// LastMutationSeq records the observation order of the last recorded
	// mutation per path, so a later differing read is not mistaken for a
	// conflict when the runtime itself changed the file.
	LastMutationSeq map[string]int `json:"lastMutationSeq,omitempty"`
}

// NewEvidenceState returns an empty state.
func NewEvidenceState() *EvidenceState {
	return &EvidenceState{
		Version:         EvidenceVersion,
		ObservedCalls:   map[string]bool{},
		LastMutationSeq: map[string]int{},
	}
}

// Clone returns a deep copy, so a checkpoint cannot alias the live state.
func (s *EvidenceState) Clone() *EvidenceState {
	if s == nil {
		return NewEvidenceState()
	}
	out := *s
	out.Items = make([]EvidenceItem, len(s.Items))
	copy(out.Items, s.Items)
	out.Requirements = append([]Requirement(nil), s.Requirements...)
	out.ObservedCalls = make(map[string]bool, len(s.ObservedCalls))
	for key, value := range s.ObservedCalls {
		out.ObservedCalls[key] = value
	}
	out.LastMutationSeq = make(map[string]int, len(s.LastMutationSeq))
	for key, value := range s.LastMutationSeq {
		out.LastMutationSeq[key] = value
	}
	return &out
}

// Empty reports whether the state has no observed facts.
func (s *EvidenceState) Empty() bool { return s == nil || len(s.Items) == 0 }

// Find returns the item with the given id.
func (s *EvidenceState) Find(id string) (EvidenceItem, bool) {
	if s == nil {
		return EvidenceItem{}, false
	}
	for _, item := range s.Items {
		if item.ID == id {
			return item, true
		}
	}
	return EvidenceItem{}, false
}

// Conflicts returns the items whose observations disagree.
func (s *EvidenceState) Conflicts() []EvidenceItem {
	if s == nil {
		return nil
	}
	var out []EvidenceItem
	for _, item := range s.Items {
		if item.Status == StatusConflicting {
			out = append(out, item)
		}
	}
	return out
}

// Missing returns the declared requirements the state cannot satisfy.
func (s *EvidenceState) Missing(execution ExecutionState) []Requirement {
	var out []Requirement
	for _, requirement := range s.Requirements {
		if s.Satisfied(requirement.Key, execution) {
			continue
		}
		out = append(out, requirement)
	}
	return out
}

// Satisfied reports whether one requirement key has adequate evidence.
func (s *EvidenceState) Satisfied(key string, execution ExecutionState) bool {
	if s == nil {
		return false
	}
	switch {
	case strings.HasPrefix(key, "target_state:"):
		path := strings.TrimPrefix(key, "target_state:")
		if path == "answer" {
			return s.hasAnyExact()
		}
		return s.targetStateKnown(path)
	case strings.HasPrefix(key, "target_content:"):
		path := strings.TrimPrefix(key, "target_content:")
		if item, ok := s.Find("file:" + path); ok && item.Status != StatusConflicting &&
			item.Status != StatusRequired && item.Status != StatusUnknown &&
			strings.TrimSpace(item.Value) != "" {
			return true
		}
		// A structural query naming the exact path is content-bearing
		// coordinate evidence for a question about a declaration or package.
		for _, item := range s.Items {
			if item.Source.Path != path {
				continue
			}
			if strings.HasPrefix(item.ID, "symbol:") || strings.HasPrefix(item.ID, "symbols:") {
				return item.Status != StatusConflicting && item.Status != StatusRequired && item.Status != StatusUnknown
			}
		}
		return false
	case strings.HasPrefix(key, "target_coordinates:"):
		value := strings.TrimPrefix(key, "target_coordinates:")
		return s.containsEvidence("symbol", value)
	case key == "repository_overview":
		return s.hasRepositoryOverview()
	case key == "verification":
		return execution.VerifiedAfterMutation || len(execution.Verifications) > 0
	case key == "execution_result":
		return execution.Executed
	default:
		for _, item := range s.Items {
			if item.ID == key && item.Status != StatusRequired && item.Status != StatusUnknown {
				return true
			}
		}
		return false
	}
}

// hasAnyExact reports whether any exact observation exists.
func (s *EvidenceState) hasAnyExact() bool {
	for _, item := range s.Items {
		if item.Retention == RetentionExact && item.Status != StatusRequired && item.Status != StatusUnknown {
			return true
		}
	}
	return false
}

// hasRepositoryOverview reports whether the state has content-bearing
// evidence about the repository.
//
// A directory listing is structure, not an explanation: it tells the model
// which names exist without telling it what any of them do. Treating `ls` as
// enough for a repository overview let a model answer "explain this project"
// from filenames alone. A file read or a search that returned matching
// content is the minimum needed to support a content claim; symbols may
// establish coordinates for a structural answer.
func (s *EvidenceState) hasRepositoryOverview() bool {
	for _, item := range s.Items {
		if item.Status == StatusConflicting {
			continue
		}
		if strings.HasPrefix(item.ID, "file:") ||
			strings.HasPrefix(item.ID, "search:grep:") ||
			strings.HasPrefix(item.ID, "symbols:") {
			return true
		}
	}
	return false
}

// containsEvidence reports whether an item kind contains value.
func (s *EvidenceState) containsEvidence(kind, value string) bool {
	for _, item := range s.Items {
		if !strings.HasPrefix(item.ID, kind+":") {
			continue
		}
		if strings.Contains(item.Summary, value) || strings.Contains(item.Value, value) {
			return true
		}
	}
	return false
}

// targetStateKnown reports whether a path's existence state is known from a
// read, a complete directory listing, or a runtime mutation.
func (s *EvidenceState) targetStateKnown(path string) bool {
	path = cleanPath(path)
	if path == "" {
		return false
	}
	if item, ok := s.Find("file:" + path); ok {
		return item.Status == StatusKnown || item.Status == StatusDerived
	}
	if item, ok := s.Find("mutation:" + path); ok {
		return item.Status == StatusKnown
	}
	// A structural query that names the exact path proves the path exists and
	// carries declarations. It is not a file read, but it is target-state and
	// coordinate evidence for a question about a declaration/package.
	for _, item := range s.Items {
		if item.Source.Path != path {
			continue
		}
		if strings.HasPrefix(item.ID, "symbol:") || strings.HasPrefix(item.ID, "symbols:") {
			return item.Status != StatusConflicting && item.Status != StatusRequired && item.Status != StatusUnknown
		}
	}
	dir := filepath.ToSlash(filepath.Dir(path))
	if dir == "." {
		dir = "."
	}
	// A complete listing of the parent establishes both presence and absence:
	// either the entry is in the listing or the file is not there.
	if item, ok := s.Find("dir:" + dir); ok && item.Complete {
		return true
	}
	return false
}

// ObserveTurns folds completed turns into the state. It is idempotent per tool
// call, so a projector may call it on every model turn.
//
// It reports the evidence gain of the final turn: how many outcomes recorded a
// repository fact, and how many of those facts were new. A turn that observed
// the repository without adding a fact is a turn whose exploration is spent,
// which is the only signal the runtime needs to stop offering tools. The
// counters describe one turn, not the run, so a model is never stopped for the
// total number of calls it made.
func (s *EvidenceState) ObserveTurns(shape TaskShape, turns []agent.Turn) (observed, novel int) {
	if s == nil {
		return 0, 0
	}
	if s.ObservedCalls == nil {
		s.ObservedCalls = map[string]bool{}
	}
	if s.LastMutationSeq == nil {
		s.LastMutationSeq = map[string]int{}
	}
	s.ensureRequirements(shape)
	last := len(turns) - 1
	for index, turn := range turns {
		for _, outcome := range turn.Results {
			if outcome.CallID != "" && s.ObservedCalls[outcome.CallID] {
				continue
			}
			if outcome.CallID != "" {
				s.ObservedCalls[outcome.CallID] = true
			}
			fact, changed := s.observe(outcome)
			if index == last && fact {
				observed++
				if changed {
					novel++
				}
			}
		}
	}
	s.trim()
	return observed, novel
}

// ensureRequirements records the shape's needs once.
func (s *EvidenceState) ensureRequirements(shape TaskShape) {
	for _, need := range shape.RequiredKnowledge {
		found := false
		for _, existing := range s.Requirements {
			if existing.Key == need.Key {
				found = true
				break
			}
		}
		if !found {
			s.Requirements = append(s.Requirements, Requirement(need))
		}
	}
}

// ObserveOutcome folds one tool outcome into the state and reports whether it
// recorded a repository fact the state did not already hold.
//
// A refusal, a diagnostic, and a generic tool result are control signals, not
// repository facts: they never count as evidence gain, so a model that is
// adapting to errors is not mistaken for a model that is repeating itself.
func (s *EvidenceState) ObserveOutcome(outcome agent.ToolOutcome) bool {
	_, changed := s.observe(outcome)
	return changed
}

// observe is the one place the outcome-to-evidence switch lives. It reports
// whether the outcome carried a repository fact and whether that fact was new.
func (s *EvidenceState) observe(outcome agent.ToolOutcome) (fact, changed bool) {
	if s == nil || outcome.Refused() {
		// A withheld or denied action never ran. It is a control signal, not
		// an observation about the repository, and must not become evidence.
		return false, false
	}
	defer s.trim()
	s.Seq++
	seq := s.Seq
	details := detailsMap(outcome.Result.Details)
	path := detailString(details, "path")
	if path == "" {
		path = argString(outcome.Args, "path")
	}
	tool := outcome.ToolName

	if outcome.Result.IsError {
		// A diagnostic is not a repository observation. The one exception is a
		// missing-file error, which establishes that the named path is absent.
		// A turn made only of errors therefore never counts as stagnation: the
		// model is correcting itself, not repeating a completed observation.
		changed := s.addError(outcome, path, seq)
		return changed, changed
	}

	switch tool {
	case "read":
		return true, s.addRead(outcome, path, details, seq)
	case "ls":
		return true, s.addListing(outcome, path, details, seq)
	case "glob":
		return true, s.addSearch(outcome, details, seq)
	case "grep":
		return true, s.addSearch(outcome, details, seq)
	case "symbols":
		return true, s.addSymbols(outcome, details, seq)
	case "write", "edit", "delete":
		return true, s.addMutation(outcome, path, details, seq)
	case "bash":
		return true, s.addBash(outcome, details, seq)
	case "web_fetch", "search", "browser", "mcp_list", "mcp_read", "mcp_call":
		return true, s.addExternal(outcome, details, seq)
	default:
		s.addGeneric(outcome, seq)
		return false, false
	}
}

// addError records an exact diagnostic. A missing-file error also establishes
// that the named path is absent, which is legitimate target-state evidence.
func (s *EvidenceState) addError(outcome agent.ToolOutcome, path string, seq int) bool {
	text := bound(outcome.Result.Text, MaxDiagnosticValueBytes)
	summary := firstLine(outcome.Result.Text)
	id := "error:" + outcome.ToolName + ":" + outcome.CallID
	s.upsert(EvidenceItem{
		ID:        id,
		Status:    StatusKnown,
		Retention: RetentionDiagnostic,
		Summary:   bound("error from "+outcome.ToolName+": "+summary, MaxEvidenceSummaryBytes),
		Value:     text,
		Digest:    digest(text),
		Source:    Source{Tool: outcome.ToolName, CallID: outcome.CallID, Path: path},
		Seq:       seq,
	})
	if path != "" && isMissingFileError(outcome.Result.Text) {
		return s.upsert(EvidenceItem{
			ID:        "file:" + cleanPath(path),
			Status:    StatusKnown,
			Retention: RetentionExact,
			Summary:   cleanPath(path) + ": absent (observed by " + outcome.ToolName + ")",
			Source:    Source{Tool: outcome.ToolName, CallID: outcome.CallID, Path: cleanPath(path)},
			Seq:       seq,
		})
	}
	return false
}

// addRead records an exact file observation.
func (s *EvidenceState) addRead(outcome agent.ToolOutcome, path string, details map[string]any, seq int) bool {
	path = cleanPath(path)
	if path == "" {
		s.addGeneric(outcome, seq)
		return false
	}
	text := bound(outcome.Result.Text, MaxEvidenceValueBytes)
	totalLines := detailInt(details, "totalLines")
	offset := detailInt(details, "offset")
	limit := detailInt(details, "limit")
	complete := offset <= 1 && (totalLines == 0 || offset+limit-1 >= totalLines)
	summary := path
	if totalLines > 0 {
		summary += fmt.Sprintf(": %d lines", totalLines)
		if !complete {
			summary += fmt.Sprintf(" (window from line %d)", max(offset, 1))
		}
	}
	if first := firstContentLine(text); first != "" {
		summary += " — " + bound(first, 120)
	}
	item := EvidenceItem{
		ID:        "file:" + path,
		Status:    StatusKnown,
		Retention: RetentionExact,
		Summary:   bound(summary, MaxEvidenceSummaryBytes),
		Value:     text,
		Digest:    digest(text),
		Complete:  complete,
		Source:    Source{Tool: "read", CallID: outcome.CallID, Path: path, StartLine: max(offset, 1)},
		Seq:       seq,
	}
	if item.Source.StartLine > 0 && totalLines > 0 {
		item.Source.EndLine = min(item.Source.StartLine+max(limit, 1)-1, totalLines)
	}
	if previous, ok := s.Find(item.ID); ok && previous.Digest != "" && previous.Digest != item.Digest {
		if mutationSeq, changed := s.LastMutationSeq[path]; changed && mutationSeq > previous.Seq {
			// The runtime changed the file; the new read is the current truth.
		} else if sameReadWindow(previous, item) {
			// Two observations of the same window disagree without a runtime
			// mutation between them. That is a real conflict.
			item.Status = StatusConflicting
			item.Conflict = "the file changed without a recorded runtime mutation"
			item.Summary = bound(path+": CONFLICTING content observed at two different times", MaxEvidenceSummaryBytes)
			if item.Value == "" {
				item.Value = previous.Value
			}
		}
		// Different windows of one file are different observations, not a
		// contradiction. Comparing their digests directly would manufacture a
		// conflict when a model pages through a large file.
	}
	return s.upsert(item)
}

// sameReadWindow reports whether two reads cover the same source range.
//
// A complete read covers the whole file. Two partial reads are comparable only
// when they start at the same line and end at the same line; anything else
// observes a different region and may legitimately differ.
func sameReadWindow(previous, current EvidenceItem) bool {
	if previous.Complete || current.Complete {
		return previous.Complete && current.Complete
	}
	return previous.Source.StartLine == current.Source.StartLine &&
		previous.Source.EndLine == current.Source.EndLine
}

// addListing records a directory listing as structural evidence.
func (s *EvidenceState) addListing(outcome agent.ToolOutcome, path string, details map[string]any, seq int) bool {
	path = cleanPath(path)
	if path == "" {
		path = "."
	}
	text := bound(outcome.Result.Text, MaxEvidenceValueBytes)
	complete := !strings.Contains(text, "showing ") && !strings.Contains(strings.ToLower(text), "truncated")
	entries := detailInt(details, "entries")
	summary := "directory " + path
	if entries > 0 {
		summary += fmt.Sprintf(": %d entries", entries)
	}
	if first := firstContentLine(text); first != "" {
		summary += " — " + bound(first, 160)
	}
	return s.upsert(EvidenceItem{
		ID:        "dir:" + path,
		Status:    StatusKnown,
		Retention: RetentionStructural,
		Summary:   bound(summary, MaxEvidenceSummaryBytes),
		Value:     text,
		Digest:    digest(text),
		Complete:  complete,
		Source:    Source{Tool: "ls", CallID: outcome.CallID, Path: path},
		Seq:       seq,
	})
}

// addSearch records a bounded search result as derived structure.
func (s *EvidenceState) addSearch(outcome agent.ToolOutcome, details map[string]any, seq int) bool {
	pattern := detailString(details, "pattern")
	path := detailString(details, "path")
	if path == "" {
		path = argString(outcome.Args, "path")
	}
	id := "search:" + outcome.ToolName + ":" + pattern + ":" + path
	text := bound(outcome.Result.Text, MaxEvidenceValueBytes)
	summary := outcome.ToolName
	if pattern != "" {
		summary += " " + pattern
	}
	if files := detailInt(details, "files"); files > 0 {
		summary += fmt.Sprintf(": %d file(s)", files)
	}
	if matches := detailInt(details, "matches"); matches > 0 {
		summary += fmt.Sprintf(": %d match(es)", matches)
	}
	if first := firstContentLine(text); first != "" {
		summary += " — " + bound(first, 140)
	}
	complete := !strings.Contains(text, "pass skip=") && !strings.Contains(text, "raise \"limit\"")
	return s.upsert(EvidenceItem{
		ID:        id,
		Status:    StatusDerived,
		Retention: RetentionStructural,
		Summary:   bound(summary, MaxEvidenceSummaryBytes),
		Value:     text,
		Digest:    digest(text),
		Complete:  complete,
		Source:    Source{Tool: outcome.ToolName, CallID: outcome.CallID, Path: path},
		Seq:       seq,
	})
}

// addSymbols records structural coordinates. The aggregate item is bounded;
// individual coordinates are retained up to a small cap.
func (s *EvidenceState) addSymbols(outcome agent.ToolOutcome, details map[string]any, seq int) bool {
	query := argString(outcome.Args, "symbol")
	if query == "" {
		query = argString(outcome.Args, "package")
	}
	if query == "" {
		query = argString(outcome.Args, "path")
	}
	text := bound(outcome.Result.Text, MaxEvidenceValueBytes)
	changed := s.upsert(EvidenceItem{
		ID:        "symbols:" + query,
		Status:    StatusDerived,
		Retention: RetentionStructural,
		Summary:   bound("symbol query "+query+": "+firstLine(text), MaxEvidenceSummaryBytes),
		Value:     text,
		Digest:    digest(text),
		Complete:  !detailBool(details, "truncated"),
		Source:    Source{Tool: "symbols", CallID: outcome.CallID},
		Seq:       seq,
	})
	coords := coordinateMaps(details["coordinates"])
	for i, coord := range coords {
		if i >= 24 {
			break
		}
		path := detailString(coord, "path")
		symbol := detailString(coord, "symbol")
		if path == "" || symbol == "" {
			continue
		}
		if s.upsert(EvidenceItem{
			ID:        "symbol:" + path + ":" + symbol,
			Status:    StatusKnown,
			Retention: RetentionStructural,
			Summary: bound(symbol+" at "+path+":"+itoa(detailInt(coord, "start_line"))+
				"-"+itoa(detailInt(coord, "end_line")), MaxEvidenceSummaryBytes),
			Source: Source{Tool: "symbols", CallID: outcome.CallID, Path: path,
				StartLine: detailInt(coord, "start_line"), EndLine: detailInt(coord, "end_line")},
			Seq: seq,
		}) {
			changed = true
		}
	}
	return changed
}

// addMutation records a definite repository change.
func (s *EvidenceState) addMutation(outcome agent.ToolOutcome, path string, details map[string]any, seq int) bool {
	path = cleanPath(path)
	value := detailString(details, "diff")
	if value == "" {
		value = outcome.Result.Text
	}
	value = bound(value, MaxEvidenceValueBytes)
	summary := "changed " + path
	if bytes := detailInt(details, "bytes"); bytes > 0 {
		summary += fmt.Sprintf(" (%d bytes)", bytes)
	}
	if path == "" {
		summary = "changed the repository"
	}
	id := "mutation:" + path
	if path == "" {
		id = "mutation:" + outcome.CallID
	}
	changed := s.upsert(EvidenceItem{
		ID:        id,
		Status:    StatusKnown,
		Retention: RetentionExact,
		Summary:   bound(summary, MaxEvidenceSummaryBytes),
		Value:     value,
		Digest:    digest(value),
		Source:    Source{Tool: outcome.ToolName, CallID: outcome.CallID, Path: path},
		Seq:       seq,
	})
	if path != "" {
		s.LastMutationSeq[path] = seq
	}
	return changed
}

// addBash records command execution, verification, and mutation evidence.
func (s *EvidenceState) addBash(outcome agent.ToolOutcome, details map[string]any, seq int) bool {
	command := detailString(details, "command")
	if command == "" {
		command = argString(outcome.Args, "command")
	}
	text := bound(outcome.Result.Text, MaxEvidenceValueBytes)
	exit := detailInt(details, "exitCode")
	summary := "$ " + bound(command, 160)
	if exit != 0 {
		summary += fmt.Sprintf(" (exit %d)", exit)
	}
	changed := s.upsert(EvidenceItem{
		ID:        "command:" + digest(command),
		Status:    StatusKnown,
		Retention: RetentionDiagnostic,
		Summary:   bound(summary, MaxEvidenceSummaryBytes),
		Value:     text,
		Digest:    digest(text),
		Source:    Source{Tool: "bash", CallID: outcome.CallID},
		Seq:       seq,
	})
	if outcome.Result.Verifies {
		check := ""
		scope := ""
		if outcome.Result.Verification != nil {
			check = string(outcome.Result.Verification.EffectiveCheck())
			for _, s := range outcome.Result.Verification.Scopes {
				if s.All {
					scope = "*"
					break
				}
				if scope == "" {
					scope = s.Path
				}
			}
		}
		id := "verification:" + digest(command)
		if s.upsert(EvidenceItem{
			ID:        id,
			Status:    StatusKnown,
			Retention: RetentionDiagnostic,
			Summary:   bound("verification "+check+" "+scope+": "+summary, MaxEvidenceSummaryBytes),
			Value:     text,
			Digest:    digest(text),
			Source:    Source{Tool: "bash", CallID: outcome.CallID, Check: check},
			Seq:       seq,
		}) {
			changed = true
		}
	}
	if outcome.Result.Mutates {
		id := "mutation:" + digest(command)
		if s.upsert(EvidenceItem{
			ID:        id,
			Status:    StatusKnown,
			Retention: RetentionExact,
			Summary:   bound("command may have changed the workspace: "+summary, MaxEvidenceSummaryBytes),
			Value:     text,
			Digest:    digest(text),
			Source:    Source{Tool: "bash", CallID: outcome.CallID},
			Seq:       seq,
		}) {
			changed = true
		}
	}
	if outcome.Result.Executes && !outcome.Result.Finished {
		if s.upsert(EvidenceItem{
			ID:        "background:" + outcome.CallID,
			Status:    StatusKnown,
			Retention: RetentionTransient,
			Summary:   bound("background job started: "+summary, MaxEvidenceSummaryBytes),
			Value:     text,
			Digest:    digest(text),
			Source:    Source{Tool: "bash", CallID: outcome.CallID},
			Seq:       seq,
		}) {
			changed = true
		}
	}
	return changed
}

// addExternal records external evidence with mandatory provenance.
func (s *EvidenceState) addExternal(outcome agent.ToolOutcome, details map[string]any, seq int) bool {
	url := detailString(details, "url")
	if url == "" {
		url = argString(outcome.Args, "url")
	}
	evidence := externalEvidence(details)
	capability := ""
	locator := url
	source := ""
	provider := ""
	trust := string(knowledge.TrustUntrusted)
	if evidence != nil {
		capability = string(evidence.Provenance.Capability)
		locator = firstNonEmpty(evidence.Provenance.Locator, locator)
		source = evidence.Provenance.Source
		provider = evidence.Provenance.Provider
		trust = string(evidence.Trust)
	}
	if capability == "" {
		capability = outcome.ToolName
	}
	if trust == "" {
		trust = string(knowledge.TrustUntrusted)
	}
	text := bound(outcome.Result.Text, MaxEvidenceValueBytes)
	summaryLine := firstContentLine(text)
	if remote := detailString(details, "remote_error"); remote != "" {
		summaryLine = "remote_error: " + firstLine(remote)
	}
	id := "external:" + capability + ":" + firstNonEmpty(locator, digest(text))
	return s.upsert(EvidenceItem{
		ID:        id,
		Status:    StatusKnown,
		Retention: RetentionExternal,
		Summary: bound("UNTRUSTED "+capability+" from "+firstNonEmpty(source, url, "unknown")+
			": "+summaryLine, MaxEvidenceSummaryBytes),
		Value:  text,
		Digest: digest(text),
		Source: Source{Tool: outcome.ToolName, CallID: outcome.CallID, URL: locator,
			Provider: provider, Trust: trust},
		Seq: seq,
	})
}

// addGeneric records an unrecognised tool result conservatively.
func (s *EvidenceState) addGeneric(outcome agent.ToolOutcome, seq int) {
	text := bound(outcome.Result.Text, MaxEvidenceValueBytes)
	retention := RetentionTransient
	if len(text) > 0 && !outcome.Result.Useless {
		retention = RetentionExact
	}
	s.upsert(EvidenceItem{
		ID:        "tool:" + outcome.ToolName + ":" + outcome.CallID,
		Status:    StatusKnown,
		Retention: retention,
		Summary:   bound(outcome.ToolName+": "+firstLine(text), MaxEvidenceSummaryBytes),
		Value:     text,
		Digest:    digest(text),
		Source:    Source{Tool: outcome.ToolName, CallID: outcome.CallID},
		Seq:       seq,
	})
}

// upsert replaces an item with the same id, preserving observation order. It
// reports whether the item is new or its observed content changed: replaying the
// same observation is not new evidence.
func (s *EvidenceState) upsert(item EvidenceItem) bool {
	for i := range s.Items {
		if s.Items[i].ID != item.ID {
			continue
		}
		changed := s.Items[i].Digest != item.Digest ||
			s.Items[i].Status != item.Status ||
			s.Items[i].Complete != item.Complete ||
			s.Items[i].Value != item.Value
		s.Items[i] = item
		return changed
	}
	s.Items = append(s.Items, item)
	return true
}

// Bytes returns the retained payload size, for checkpoints and tests.
func (s *EvidenceState) Bytes() int {
	if s == nil {
		return 0
	}
	total := 0
	for _, item := range s.Items {
		total += len(item.ID) + len(item.Summary) + len(item.Value) + len(item.Conflict)
	}
	return total
}

// trim bounds the state by item count and payload bytes, dropping low-value
// evidence first.
func (s *EvidenceState) trim() {
	priority := func(item EvidenceItem) int {
		if item.Status == StatusConflicting {
			return 0
		}
		switch item.Retention {
		case RetentionDiagnostic, RetentionExternal:
			return 1
		case RetentionExact:
			return 2
		case RetentionDerived, RetentionStructural:
			return 3
		default:
			return 4
		}
	}
	// Low-value evidence sorts first so the eviction loop can drop it from the
	// front. Within one priority the oldest observation goes first.
	sort.SliceStable(s.Items, func(i, j int) bool {
		pi, pj := priority(s.Items[i]), priority(s.Items[j])
		if pi != pj {
			return pi > pj
		}
		return s.Items[i].Seq < s.Items[j].Seq
	})
	total := s.Bytes()
	for len(s.Items) > 1 && (len(s.Items) > MaxEvidenceItems || total > MaxEvidenceBytes) {
		total -= len(s.Items[0].ID) + len(s.Items[0].Summary) + len(s.Items[0].Value) + len(s.Items[0].Conflict)
		s.Items = s.Items[1:]
	}
	// The replay guard and mutation clock are metadata, not facts. Cap them so
	// a long session cannot grow the checkpoint through bookkeeping alone.
	for len(s.ObservedCalls) > MaxEvidenceItems*4 {
		for key := range s.ObservedCalls {
			delete(s.ObservedCalls, key)
			break
		}
	}
	if len(s.LastMutationSeq) > MaxEvidenceItems {
		type entry struct {
			key string
			seq int
		}
		entries := make([]entry, 0, len(s.LastMutationSeq))
		for key, seq := range s.LastMutationSeq {
			entries = append(entries, entry{key: key, seq: seq})
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].seq < entries[j].seq })
		for _, item := range entries[:len(entries)-MaxEvidenceItems] {
			delete(s.LastMutationSeq, item.key)
		}
	}
}

// detailsMap returns a tool result's details as a map.
func detailsMap(details any) map[string]any {
	if details == nil {
		return map[string]any{}
	}
	if values, ok := details.(map[string]any); ok {
		return values
	}
	return map[string]any{}
}

func detailString(details map[string]any, key string) string {
	value, _ := details[key].(string)
	return value
}

func detailInt(details map[string]any, key string) int {
	switch value := details[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	default:
		return 0
	}
}

func detailBool(details map[string]any, key string) bool {
	value, _ := details[key].(bool)
	return value
}

func argString(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	value, _ := args[key].(string)
	return value
}

// externalEvidence extracts the first knowledge record from a tool result.
func externalEvidence(details map[string]any) *knowledge.Evidence {
	switch value := details["evidence"].(type) {
	case []knowledge.Evidence:
		if len(value) > 0 {
			return &value[0]
		}
	case knowledge.Evidence:
		return &value
	case []any:
		for _, entry := range value {
			if evidence, ok := entry.(knowledge.Evidence); ok {
				return &evidence
			}
		}
	}
	return nil
}

func isMissingFileError(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "no such file") ||
		strings.Contains(lower, "does not exist") ||
		strings.Contains(lower, "cannot find")
}

func firstLine(text string) string {
	text = strings.TrimSpace(text)
	if index := strings.IndexByte(text, '\n'); index >= 0 {
		text = text[:index]
	}
	return strings.TrimSpace(text)
}

// firstContentLine returns the first line of a read result without its line
// number prefix, so a summary names what the file is rather than "1\tpackage".
func firstContentLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if tab := strings.IndexByte(line, '\t'); tab >= 0 {
			line = strings.TrimSpace(line[tab+1:])
		}
		if line == "" || strings.HasPrefix(line, "[showing ") {
			continue
		}
		return line
	}
	return ""
}

func digest(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:8])
}

func bound(text string, limit int) string {
	if limit <= 0 || len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && (text[cut]&0xc0) == 0x80 {
		cut--
	}
	return strings.TrimSpace(text[:cut]) + "…"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func itoa(value int) string { return fmt.Sprintf("%d", value) }

// coordinateMaps accepts the slice shape the symbols tool returns and the
// generic shape a decoded payload may carry.
func coordinateMaps(value any) []map[string]any {
	switch typed := value.(type) {
	case []map[string]any:
		return typed
	case []any:
		out := make([]map[string]any, 0, len(typed))
		for _, entry := range typed {
			if item, ok := entry.(map[string]any); ok {
				out = append(out, item)
			}
		}
		return out
	default:
		return nil
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
