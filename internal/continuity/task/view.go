package task

import (
	"fmt"
	"sort"
	"strings"

	"github.com/PizenLabs/ohgo/internal/control/workspace"
	"github.com/PizenLabs/ohgo/internal/tokens"
)

// ItemState is the display state of one projected todo.
type ItemState string

// Projection states, matching the semantic todo vocabulary.
const (
	// ItemPending is work not started.
	ItemPending ItemState = "pending"
	// ItemActive is the work being done now.
	ItemActive ItemState = "active"
	// ItemDone is finished work.
	ItemDone ItemState = "done"
)

// Item is one projected todo line: a state and a label, with no presentation
// glyph. The renderer chooses the glyph, so the vocabulary lives in one place.
type Item struct {
	// State is the display state.
	State ItemState
	// Label is the todo text.
	Label string
}

// Items projects the task's todos for a UI.
//
// The vocabulary is intentionally three-valued, so a consumer never has to
// infer a display state from a workflow status it does not know.
func Items(t Task) []Item {
	items := make([]Item, 0, len(t.Todos))
	for _, todo := range t.Todos {
		state := ItemPending
		switch todo.Status {
		case TodoDone:
			state = ItemDone
		case TodoActive:
			state = ItemActive
		}
		items = append(items, Item{State: state, Label: todo.Label})
	}
	return items
}

// contextBudget bounds the reconstructed context. Semantic continuity needs
// orientation, not a transcript dump: the point is to keep the model oriented
// without letting reconstruction become the unbounded history it replaced.
const contextBudget = 6000

// contextTokenBudget is the token view of the same briefing. It uses the
// repository's existing estimator, so context assembly inherits the same
// truthful "estimated, not exact" semantics as prompt budgeting.
const contextTokenBudget = 1500

// contextFieldCap bounds one section. A single giant finding or constraint
// list must not crowd out the orientation that follows it.
const contextFieldCap = 1200

// contextRoleReserve reserves room for the active role's execution context.
// The role section is the most execution-specific part of the briefing, so it
// is not allowed to disappear merely because optional knowledge sections were
// rendered first. The reserve is released after the role block so evidence and
// legacy notes can use whatever remains.
const (
	contextRoleReserve       = contextFieldCap + 200
	contextRoleReserveTokens = 350
)

// contextFindings bounds how many legacy, unclassified notes reconstruction
// carries: recent observations matter, and an append-only log is not
// orientation.
const contextFindings = 8

// contextRoleNotes bounds one role-context list. Progress is orientation, not
// an audit log; the durable record keeps the full task evidence.
const contextRoleNotes = 10

// Context returns deterministic orientation blocks for a task under the
// default BUILD role. It is kept for callers that predate role-aware assembly;
// new callers should use ContextFor.
func Context(t Task, lastAction string) []string {
	return ContextFor(t, workspace.Build, lastAction)
}

// ContextFor returns deterministic orientation blocks for a task and role.
//
// Every field is drawn from recorded state; nothing is inferred and nothing is
// invented. The same task, role, and last action always produce the same
// blocks, which is what makes context reconstruction testable without a model.
// Only the active role's execution context is assembled: a role never sees
// another role's plan, scope, strategy, or notes.
//
// Selection is by relevance under the fixed budget, never by a fixed recency
// cap: disputed facts and facts the active role references come first, then
// the remaining active facts in record order. A section that does not fit
// reports how many items were omitted instead of pretending it was complete.
// The assembled text is context, never instruction and never authority. It
// cannot grant a capability; the workspace and security gates remain the only
// authority paths.
func ContextFor(t Task, role workspace.Workspace, lastAction string) []string {
	// Hold back the role reserve while the shared knowledge sections are
	// rendered. This guarantees the active role's context survives even when
	// facts, hypotheses, and questions are all full.
	b := newContextBuilder(contextBudget-contextRoleReserve, contextTokenBudget-contextRoleReserveTokens)

	b.addBlock("Objective", t.Objective)
	if t.Status != "" {
		b.addBlock("Status", string(t.Status))
	}
	if warning := strings.TrimSpace(t.ContextWarning); warning != "" {
		b.addBlock("Continuity", warning)
	}
	if role.Valid() {
		b.addBlock("Authority", role.Label()+" ("+role.Purpose()+")")
	} else {
		b.addBlock("Authority", "unresolved (context only; no execution role active)")
	}
	if t.Handoff != nil {
		b.addBlock("Handoff", renderHandoff(*t.Handoff))
	}
	b.addBlock("Phase", t.Phase)
	b.addBlock("Current step", t.ActiveStep)
	b.addBlock("Review reason", t.ReviewReason)
	b.addBlock("Constraints", strings.Join(t.Constraints, "; "))
	b.addBlock("Todo", renderTodoContext(t))
	if active, ok := t.ActiveTodo(); ok {
		b.addBlock("Active todo", active.Label)
	}
	b.addBlock("Next action", t.NextAction)
	b.addBlock("Last action", lastAction)

	b.addItems("Verified facts", factItems(t, role))
	b.addItems("Hypotheses", hypothesisItems(t, role))
	b.addItems("Open questions", questionItems(t, role))

	// Release the reserve: the active role's context is next, then the
	// evidence references it needs to interpret the conclusions.
	b.release(contextBudget, contextTokenBudget)
	if role.Valid() {
		if roleCtx, ok := t.Context.Role(role); ok {
			b.addBlock(role.Label()+" context", renderRoleContext(t, roleCtx))
		} else {
			b.addBlock("Role context", "none recorded for "+role.Label())
		}
	} else {
		b.addBlock("Role context", "unavailable until an execution role is resolved")
	}
	b.addItems("Evidence", evidenceItems(t, role))
	b.addItems("Unclassified notes (not verified)", findingsItems(t.Findings))

	return b.blocks
}

// contextBuilder accumulates bounded blocks. It enforces both the character
// and the estimated token budget, so a block that fits one but not the other
// is never appended.
type contextBuilder struct {
	blocks     []string
	chars      int
	tokens     int
	charLimit  int
	tokenLimit int
}

func newContextBuilder(charLimit, tokenLimit int) *contextBuilder {
	return &contextBuilder{charLimit: charLimit, tokenLimit: tokenLimit}
}

func (b *contextBuilder) canAdd(block string) bool {
	return b.chars+len(block) <= b.charLimit &&
		b.tokens+tokens.Count(block, tokens.ModeEstimate) <= b.tokenLimit
}

func (b *contextBuilder) commit(block string) {
	b.blocks = append(b.blocks, block)
	b.chars += len(block)
	b.tokens += tokens.Count(block, tokens.ModeEstimate)
}

// release raises the limits after the reserved role section is reached.
func (b *contextBuilder) release(charLimit, tokenLimit int) {
	b.charLimit = charLimit
	b.tokenLimit = tokenLimit
}

// addBlock appends one labelled block. An oversized value is truncated rather
// than dropped: the reader needs the beginning of the constraint, not nothing.
func (b *contextBuilder) addBlock(label, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	block := label + ": " + truncate(value, contextFieldCap)
	if b.canAdd(block) {
		b.commit(block)
	}
}

// addItems appends a labelled list up to the section and overall budgets. It
// stops at the section cap rather than discarding the whole section, and it
// names the omitted count so a bounded briefing is never mistaken for a
// complete one.
func (b *contextBuilder) addItems(label string, items []string) {
	if len(items) == 0 {
		return
	}
	prefix := label + ": "
	selected := make([]string, 0, len(items))
	used := len(prefix)
	processed := 0
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			processed++
			continue
		}
		separator := 0
		if len(selected) > 0 {
			separator = 2
		}
		if used+separator+len(item) > contextFieldCap {
			break
		}
		candidateItems := append(append([]string(nil), selected...), item)
		candidate := prefix + strings.Join(candidateItems, "; ")
		if !b.canAdd(candidate) {
			break
		}
		selected = append(selected, item)
		used += separator + len(item)
		processed++
	}
	if len(selected) == 0 {
		return
	}
	block := prefix + strings.Join(selected, "; ")
	if remaining := len(items) - processed; remaining > 0 {
		marker := fmt.Sprintf(" … (+%d more)", remaining)
		if len(block)+len(marker) <= contextFieldCap {
			if candidate := block + marker; b.canAdd(candidate) {
				block = candidate
			}
		}
	}
	b.commit(block)
}

// factItems returns active facts in relevance order: disputed facts first
// because they need attention, then facts the active role references, then the
// remaining active facts in record order. Record order is deliberate: an older
// verified fact is not discarded merely because a newer one exists.
func factItems(t Task, role workspace.Workspace) []string {
	referenced := map[string]bool{}
	if roleCtx, ok := t.Context.Role(role); ok {
		for _, id := range roleCtx.FactIDs {
			referenced[id] = true
		}
	}
	type ranked struct {
		text string
		rank int
	}
	var items []ranked
	for _, fact := range t.Context.Facts {
		if fact.Status != FactActive {
			continue
		}
		rank := 2
		if len(t.ContradictingEvidence(fact.ID)) > 0 {
			rank = 0
		} else if referenced[fact.ID] {
			rank = 1
		}
		items = append(items, ranked{text: renderFact(t, fact), rank: rank})
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].rank < items[j].rank })
	out := make([]string, len(items))
	for i, item := range items {
		out[i] = item.text
	}
	return out
}

// renderFact renders one active, evidence-backed fact. A fact contradicted by
// current evidence is shown as DISPUTED but is never deleted or silently
// overwritten.
func renderFact(t Task, fact Fact) string {
	entry := "#" + fact.ID + " " + truncate(fact.Text, 300) + " [" + fact.Role.Label() + "]"
	if fact.FromHypothesis != "" {
		entry += " (confirmed hypothesis #" + fact.FromHypothesis + ")"
	}
	if len(fact.EvidenceIDs) > 0 {
		entry += " evidence:" + strings.Join(fact.EvidenceIDs, ",")
	}
	if contradictions := t.ContradictingEvidence(fact.ID); len(contradictions) > 0 {
		entry += " DISPUTED by #" + contradictions[0].ID + ": " + truncate(contradictions[0].Summary, 160)
	}
	return truncate(entry, 420)
}

// hypothesisItems returns hypotheses in disposition order: active proposals
// first, then confirmed statements not already represented by an active fact,
// then disposed history. A confirmed hypothesis whose fact is rendered is
// omitted to avoid duplicating the same statement; the fact line names it.
func hypothesisItems(t Task, _ workspace.Workspace) []string {
	type ranked struct {
		text string
		rank int
	}
	var items []ranked
	for _, hypothesis := range t.Context.Hypotheses {
		rank := 4
		switch hypothesis.Status {
		case HypothesisActive:
			rank = 0
		case HypothesisConfirmed:
			if hasActiveFactFrom(t, hypothesis.ID) {
				continue
			}
			rank = 1
		case HypothesisRejected:
			rank = 2
		case HypothesisSuperseded:
			rank = 3
		}
		items = append(items, ranked{text: renderHypothesis(hypothesis), rank: rank})
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].rank < items[j].rank })
	out := make([]string, len(items))
	for i, item := range items {
		out[i] = item.text
	}
	return out
}

func renderHypothesis(hypothesis Hypothesis) string {
	entry := "#" + hypothesis.ID + " [" + string(hypothesis.Status) + "/" + hypothesis.Role.Label() + "] " +
		truncate(hypothesis.Text, 300)
	if hypothesis.Resolution != "" {
		entry += " — " + truncate(hypothesis.Resolution, 160)
	}
	return truncate(entry, 420)
}

func hasActiveFactFrom(t Task, hypothesisID string) bool {
	for _, fact := range t.Context.Facts {
		if fact.Status == FactActive && fact.FromHypothesis == hypothesisID {
			return true
		}
	}
	return false
}

// questionItems returns outstanding questions, with the active role's own
// questions first. Answered questions remain in the durable record but are not
// orientation.
func questionItems(t Task, role workspace.Workspace) []string {
	type ranked struct {
		text string
		rank int
	}
	var items []ranked
	for _, question := range t.Context.Questions {
		if question.Status != QuestionOpen {
			continue
		}
		rank := 1
		if question.Role == role {
			rank = 0
		}
		items = append(items, ranked{
			text: "#" + question.ID + " [" + question.Role.Label() + "] " + truncate(question.Text, 240),
			rank: rank,
		})
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].rank < items[j].rank })
	out := make([]string, len(items))
	for i, item := range items {
		out[i] = item.text
	}
	return out
}

// evidenceItems returns only current evidence references. Invalid and
// superseded records are history, not briefing material: they must not crowd
// out the current evidence that supports the live conclusions. Evidence
// referenced by an active fact or by the active role comes first; inconclusive
// evidence follows current supporting evidence.
func evidenceItems(t Task, role workspace.Workspace) []string {
	referenced := map[string]bool{}
	for _, fact := range t.Context.Facts {
		if fact.Status != FactActive {
			continue
		}
		for _, id := range fact.EvidenceIDs {
			referenced[id] = true
		}
	}
	if roleCtx, ok := t.Context.Role(role); ok {
		for _, id := range roleCtx.FactIDs {
			if fact, ok := t.Context.factByID(id); ok {
				for _, evidenceID := range fact.EvidenceIDs {
					referenced[evidenceID] = true
				}
			}
		}
		for _, id := range roleCtx.HypothesisIDs {
			if hypothesis, ok := t.Context.hypothesisByID(id); ok {
				for _, evidenceID := range hypothesis.EvidenceIDs {
					referenced[evidenceID] = true
				}
			}
		}
	}
	type ranked struct {
		text string
		rank int
	}
	var items []ranked
	for _, record := range t.Context.Evidence {
		if record.Validity != EvidenceCurrent {
			continue
		}
		rank := 2
		if referenced[record.ID] {
			rank = 0
		} else if isSupporting(record) {
			rank = 1
		}
		items = append(items, ranked{text: renderEvidence(record), rank: rank})
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].rank < items[j].rank })
	out := make([]string, len(items))
	for i, item := range items {
		out[i] = item.text
	}
	return out
}

// renderHandoff describes a transition without copying the records it carries.
// It is orientation: which roles, which disposition, what the target should
// know, and how much shared context is reachable by reference.
func renderHandoff(handoff Handoff) string {
	entry := handoff.From.Label() + " → " + handoff.To.Label() + " [" + string(handoff.Status) + "]"
	carried := handoff.Carried()
	var parts []string
	if carried.Facts > 0 {
		parts = append(parts, fmt.Sprintf("%d facts", carried.Facts))
	}
	if carried.Hypotheses > 0 {
		parts = append(parts, fmt.Sprintf("%d hypotheses", carried.Hypotheses))
	}
	if carried.Questions > 0 {
		parts = append(parts, fmt.Sprintf("%d questions", carried.Questions))
	}
	if carried.Evidence > 0 {
		parts = append(parts, fmt.Sprintf("%d evidence refs", carried.Evidence))
	}
	if carried.Artifacts > 0 {
		parts = append(parts, fmt.Sprintf("%d artifacts", carried.Artifacts))
	}
	if len(parts) > 0 {
		entry += " carries " + strings.Join(parts, ", ")
	}
	if handoff.Summary != "" {
		entry += " — " + truncate(handoff.Summary, 240)
	}
	if handoff.Workspace != "" {
		entry += " (workspace: " + truncate(handoff.Workspace, 120) + ")"
	}
	if handoff.Failure != "" {
		entry += " — " + truncate(handoff.Failure, 200)
	}
	return truncate(entry, 480)
}

// renderEvidence renders one evidence reference: summary, source, and
// artifact, never a copied tool payload.
func renderEvidence(record EvidenceRecord) string {
	entry := "#" + record.ID + " [" + string(record.Kind) + "/" + string(record.Outcome) + "/" +
		string(record.Validity) + "/" + record.Role.Label() + "] " + truncate(record.Summary, 240)
	if record.Source != "" {
		entry += " (source: " + truncate(record.Source, 120) + ")"
	}
	if record.Artifact != "" {
		entry += " (artifact: " + truncate(record.Artifact, 120) + ")"
	}
	if len(record.Contradicts) > 0 {
		entry += " (contradicts: " + strings.Join(record.Contradicts, ",") + ")"
	}
	return truncate(entry, 420)
}

// findingsItems renders the tail of the legacy, unclassified finding log,
// newest last. These are explicitly not verified facts.
func findingsItems(findings []string) []string {
	if len(findings) == 0 {
		return nil
	}
	start := 0
	if len(findings) > contextFindings {
		start = len(findings) - contextFindings
	}
	out := make([]string, 0, len(findings)-start)
	for _, finding := range findings[start:] {
		if finding = strings.TrimSpace(finding); finding != "" {
			out = append(out, truncate(finding, 240))
		}
	}
	return out
}

// renderRoleContext renders only the active role's own fields. A BUILD plan
// and scope never appear in a FUZZ or ASK briefing, which keeps role context
// isolated even when all three roles share one task.
func renderRoleContext(t Task, role RoleContext) string {
	var parts []string
	add := func(label, value string) {
		if value = strings.TrimSpace(value); value != "" {
			parts = append(parts, label+": "+truncate(value, 360))
		}
	}
	switch role.Role {
	case workspace.Ask:
		add("reasoning", strings.Join(recentRoleNotes(role.Reasoning), "; "))
		add("findings", renderFactRefs(t, role.FactIDs))
		add("progress", strings.Join(recentRoleNotes(role.Progress), "; "))
		add("artifacts", strings.Join(recentRoleNotes(role.Artifacts), "; "))
	case workspace.Fuzz:
		add("strategy", role.Strategy)
		add("hypotheses", renderHypothesisRefs(t, role.HypothesisIDs))
		add("progress", strings.Join(recentRoleNotes(role.Progress), "; "))
		add("artifacts", strings.Join(recentRoleNotes(role.Artifacts), "; "))
	case workspace.Build:
		add("plan", strings.Join(recentRoleNotes(role.Plan), "; "))
		add("planned scope", strings.Join(recentRoleNotes(role.Scope), "; "))
		add("progress", strings.Join(recentRoleNotes(role.Progress), "; "))
		add("artifacts", strings.Join(recentRoleNotes(role.Artifacts), "; "))
	}
	return strings.Join(parts, "; ")
}

func renderFactRefs(t Task, ids []string) string {
	var parts []string
	for _, id := range ids {
		fact, ok := t.Context.factByID(id)
		if !ok {
			parts = append(parts, "#"+id+" (missing)")
			continue
		}
		parts = append(parts, "#"+id+" ["+string(fact.Status)+"] "+truncate(fact.Text, 160))
	}
	return strings.Join(parts, "; ")
}

func renderHypothesisRefs(t Task, ids []string) string {
	var parts []string
	for _, id := range ids {
		hypothesis, ok := t.Context.hypothesisByID(id)
		if !ok {
			parts = append(parts, "#"+id+" (missing)")
			continue
		}
		parts = append(parts, "#"+id+" ["+string(hypothesis.Status)+"] "+truncate(hypothesis.Text, 160))
	}
	return strings.Join(parts, "; ")
}

func recentRoleNotes(notes []string) []string {
	if len(notes) <= contextRoleNotes {
		return notes
	}
	return notes[len(notes)-contextRoleNotes:]
}

// truncate bounds a value, marking the cut so a reader knows it is incomplete.
func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
}

// renderTodoContext renders the todo list on one line, marking each item.
func renderTodoContext(t Task) string {
	if len(t.Todos) == 0 {
		return ""
	}
	parts := make([]string, 0, len(t.Todos))
	for _, todo := range t.Todos {
		marker := "TODO"
		switch todo.Status {
		case TodoDone:
			marker = "DONE"
		case TodoActive:
			marker = "ACTIVE"
		}
		entry := fmt.Sprintf("%s %s", marker, todo.Label)
		if todo.Evidence != "" {
			entry += " (evidence: " + todo.Evidence + ")"
		}
		parts = append(parts, entry)
	}
	return strings.Join(parts, "; ")
}
