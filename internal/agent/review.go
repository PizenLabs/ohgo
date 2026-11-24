package agent

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// ReviewInformationalAnswer is the answer-level gate a host installs on the
// loop for an informational or investigative turn.
//
// It is deliberately narrower than completion: it can ask for a bounded,
// claim-level corrected answer, but it cannot prove a mutation, grant
// authority, or publish a result. The final verdict still comes from
// EvaluateTaskCompletion after the loop stops. A mutation objective is not
// reviewed here because its correction is a real change plus a check, not a
// rewritten sentence.
func ReviewInformationalAnswer(intent contract.IntentContract, turns []Turn, answer string) string {
	if intent.Kind != contract.ObjectiveInformational {
		return ""
	}
	if strings.TrimSpace(answer) == "" {
		return "the answer contains no project finding"
	}
	if answerIsDeferredPlan(answer) {
		// A promise to keep investigating is not a finding. The loop already
		// withholds it from the transcript; this review spends the one
		// correction window on the answer the user actually asked for.
		return "the previous message only announces a further action; state the answer from the evidence already observed"
	}
	assessment := AssessGrounding(intent, turns, answer)
	if assessment.Pass() {
		return ""
	}
	if reason := strings.TrimSpace(assessment.Reason()); reason != "" {
		return reason
	}
	return "the answer does not satisfy the request's evidence contract"
}

// ReviewInformationalRepairs returns the claim-level repairs for a rejected
// informational answer.
//
// It is a projection of the same deterministic audits the review runs:
// grounding and the required facets. It never re-audits the answer with a
// second rule set, and it never widens what a repair may claim. A host installs
// it next to AnswerReview so the correction request can name the claims instead
// of asking for a full rewrite.
func ReviewInformationalRepairs(intent contract.IntentContract, turns []Turn, answer string) []contract.GroundingIssue {
	if intent.Kind != contract.ObjectiveInformational || strings.TrimSpace(answer) == "" {
		return nil
	}
	// AssessGrounding already includes the required-facet audit at its end;
	// appending it again would name the same claim twice and spend two repair
	// instructions on one gap.
	return AssessGrounding(intent, turns, answer).Repairs()
}

// SetInformationalRepairs installs the claim-level planner for one
// informational contract. It is the host-facing convenience over
// SetAnswerRepairs: the planner is still the review's own audit.
func (l *Loop) SetInformationalRepairs(intent contract.IntentContract) {
	if l == nil {
		return
	}
	l.cfg.AnswerRepairs = func(turns []Turn, answer string) []contract.GroundingIssue {
		return ReviewInformationalRepairs(intent, turns, answer)
	}
}

// ClearInformationalRepairs removes the claim-level planner.
func (l *Loop) ClearInformationalRepairs() {
	if l != nil {
		l.cfg.AnswerRepairs = nil
	}
}

// answerReviewReason runs the installed answer review, if any.
//
// An open plan is reviewed before the answer is published. The plan is the
// model's own guidance, not evidence, so this can only ask for reconciliation:
// the correction turn keeps the plan tool even after the evidence allowance is
// spent. Without this gate a model could answer from a plan it had not closed,
// and the durable task would then refuse a completion the model could no longer
// reach.
func (l *Loop) answerReviewReason(turns []Turn) string {
	if l == nil || len(turns) == 0 {
		return ""
	}
	reason := ""
	if l.cfg.AnswerReview != nil {
		answer := turns[len(turns)-1].Assistant.Text()
		reason = strings.TrimSpace(l.cfg.AnswerReview(turns, answer))
	}
	if plan := l.planReviewReason(); plan != "" {
		if reason == "" {
			return plan
		}
		return plan + "; " + reason
	}
	return reason
}

// planReviewReason reports an open plan as an answer-boundary correction.
//
// The report is a union of the plan owners' own state. It never judges whether
// the plan was good, complete, or relevant; it only requires each item to reach
// a terminal disposition before the answer.
func (l *Loop) planReviewReason() string {
	if l == nil {
		return ""
	}
	items := OpenPlanItems(l.cfg.Tools)
	if len(items) == 0 {
		return ""
	}
	return fmt.Sprintf(
		"the task plan still has %d open item(s) (%s); mark each done, or remove it if you will not do it, with the plan tool before the final answer. A blocked item keeps the task open",
		len(items), boundRepair(strings.Join(items, "; "), 240))
}

// answerRepairs runs the installed claim-level planner, if any.
func (l *Loop) answerRepairs(turns []Turn) []contract.GroundingIssue {
	if l == nil || l.cfg.AnswerRepairs == nil || len(turns) == 0 {
		return nil
	}
	answer := turns[len(turns)-1].Assistant.Text()
	return l.cfg.AnswerRepairs(turns, answer)
}

// answerPassesReview evaluates a candidate text without mutating the run's
// turns. It is used when a fallback repair restores valid claims from the
// strongest draft: the restored text must pass the same audit before commit.
func (l *Loop) answerPassesReview(turns []Turn, text string) bool {
	if len(turns) == 0 {
		return false
	}
	candidate := append([]Turn(nil), turns...)
	candidate[len(candidate)-1].Assistant = replaceAssistantText(
		candidate[len(candidate)-1].Assistant, text)
	reason := l.answerReviewReason(candidate)
	if strings.TrimSpace(reason) == "" {
		return true
	}
	return !blocksPublication(l.answerRepairs(candidate))
}

// adoptAnswerText replaces the candidate prose in both the turn and the live
// conversation. The model's next request and the host's projection must see the
// same semantic answer; otherwise a repair could be reviewed as one text and
// replayed as another.
func adoptAnswerText(turns []Turn, live *[]Message, text string) {
	if len(turns) == 0 {
		return
	}
	last := &turns[len(turns)-1]
	last.Assistant = replaceAssistantText(last.Assistant, text)
	replaceLiveAssistant(live, last.Assistant)
}

// replaceLiveAssistant updates the most recent assistant message in the live
// conversation. A turn that executed tools has tool results after its assistant
// message, so replacing the slice's last element would overwrite evidence.
func replaceLiveAssistant(live *[]Message, message ai.AssistantMessage) {
	if live == nil {
		return
	}
	for index := len(*live) - 1; index >= 0; index-- {
		if _, ok := (*live)[index].(AssistantMessage); ok {
			(*live)[index] = AssistantMessage{Message: message}
			return
		}
	}
}

// correctionDirective renders the one bounded correction for a rejected
// answer. It prefers the claim-level list and falls back to the prose reason.
func (l *Loop) correctionDirective(turns []Turn, reason string, preserved []string) string {
	if l == nil {
		return ""
	}
	return completionRepairDirectiveWithPreserved(
		l.cfg.RepairRequest,
		l.cfg.CompletionReviewRequest,
		reason,
		l.answerRepairs(turns),
		preserved,
		l.correctionToolBudget(),
	)
}

// remainingCorrectionCalls is the allowance the next correction request may
// spend. It is zero outside a correction window.
func (l *Loop) remainingCorrectionCalls() int {
	if l == nil || !l.correctionActive.Load() {
		return 0
	}
	remaining := l.correctionToolBudget() - int(l.correctionCalls.Load())
	if remaining < 0 {
		return 0
	}
	return remaining
}

// correctionToolBudget returns the per-correction tool-call bound.
func (l *Loop) correctionToolBudget() int {
	if l != nil && l.cfg.CorrectionToolCalls > 0 {
		return l.cfg.CorrectionToolCalls
	}
	return DefaultCorrectionToolCalls
}

// completionReviewDirective renders the one correction instruction. An empty
// template means the host did not authorize a correction turn, so the rejected
// answer is returned to the completion evaluator instead.
func completionReviewDirective(template, reason string) string {
	template = strings.TrimSpace(template)
	reason = strings.TrimSpace(reason)
	if template == "" || reason == "" {
		return ""
	}
	return strings.ReplaceAll(template, "{{reason}}", reason)
}

// boundRepair truncates a repair field without splitting a UTF-8 rune.
func boundRepair(text string, limit int) string {
	text = strings.TrimSpace(text)
	if limit <= 0 || len(text) <= limit {
		return text
	}
	if limit <= 3 {
		return strings.ToValidUTF8(text[:limit], "")
	}
	return strings.ToValidUTF8(text[:limit-3], "") + "..."
}

// MaxRepairInstructions bounds the claims one correction may name. More than a
// handful is a rewritten answer, not a bounded repair, and a long list invites
// another full investigation.
const MaxRepairInstructions = 5

// MaxAnswerCorrections bounds the correction requests for one answer
// lifecycle. A repair can introduce a new unsupported claim; the next review
// must be allowed to repair it or the one correction silently publishes
// nothing. The bound stays small, and a correction is admitted only when the
// review names a different claim set (answerRepairKey), so this cannot become a
// retry loop.
const MaxAnswerCorrections = 2

// blocksPublication reports whether a review's issues are factual claims that
// must not be published. A review with no claim list is a prose rejection (a
// plan or an empty answer), which is also not publishable. A facet gap is a
// completion gap the answer may honestly state, so it does not block
// publication; the finalizer's Approved gate still runs.
func blocksPublication(issues []contract.GroundingIssue) bool {
	if len(issues) == 0 {
		return true
	}
	for _, issue := range issues {
		if !isFacetIssue(issue) {
			return true
		}
	}
	return false
}

// maxAnswerReviewReason bounds the review boundary a host may show under a
// refused answer. It is one sentence, not the claim list: the durable task
// owns the bounded machine-readable issues.
const maxAnswerReviewReason = 160

// answerRejectionReason renders the one-line boundary for a terminal refusal.
// The review prose wins because it is the audit's own wording; otherwise the
// first claim names what was not established. It never formats a counter,
// candidate number, or scheduler state, so a host can show it verbatim.
func answerRejectionReason(reason string, issues []contract.GroundingIssue) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		for _, issue := range issues {
			if detail := strings.TrimSpace(issue.Detail); detail != "" {
				reason = detail
				break
			}
			if claim := strings.TrimSpace(issue.Claim); claim != "" {
				reason = fmt.Sprintf("the evidence review did not support %q", claim)
				break
			}
		}
	}
	if reason == "" {
		reason = "the evidence review did not approve this answer"
	}
	return boundRepair(strings.Join(strings.Fields(reason), " "), maxAnswerReviewReason)
}

// answerRepairKey identifies the claims a review rejected. It is the progress
// signal for correction admission: a different set may earn the next pass,
// while the same set terminates at review.
func answerRepairKey(issues []contract.GroundingIssue, reason string) string {
	reason = strings.TrimSpace(reason)
	if len(issues) == 0 {
		return reason
	}
	parts := make([]string, 0, len(issues))
	for _, issue := range issues {
		parts = append(parts, strings.TrimSpace(issue.Code)+"\x00"+strings.TrimSpace(issue.Claim))
	}
	sort.Strings(parts)
	return strings.Join(parts, "\x01")
}

// RepairRequest is the bounded semantic repair contract for one answer
// revision. It is the only repair state a model receives: the target claims,
// why they failed, the claims that are already valid, and the permitted
// actions. Scheduler counters and candidate identifiers stay out of it.
type RepairRequest struct {
	// TargetClaims are the review findings that must be repaired.
	TargetClaims []contract.GroundingIssue
	// Reason is the audit's own prose boundary.
	Reason string
	// PreservedClaims are independently supported sentences the revision must
	// keep. They are context, not new evidence.
	PreservedClaims []string
	// AllowedActions are the permitted repair operations.
	AllowedActions []string
}

// RepairActions returns the permitted actions named by a repair list. VERIFY is
// always available: every other action is a weaker alternative when evidence
// cannot be acquired.
func RepairActions(repairs []contract.GroundingIssue) []string {
	actions := []string{"VERIFY"}
	seen := map[string]bool{"VERIFY": true}
	for _, repair := range repairs {
		action := strings.ToUpper(strings.TrimSpace(repair.Action))
		switch {
		case strings.Contains(action, "ATTRIBUT") || repair.Category == "attribution":
			action = "ATTRIBUTE"
		case strings.Contains(action, "WEAKEN") || strings.Contains(action, "QUALIF") ||
			strings.Contains(action, "NARROW") || strings.Contains(action, "BOUND"):
			action = "WEAKEN"
		case strings.Contains(action, "REMOVE") || strings.Contains(action, "DELETE"):
			action = "REMOVE"
		default:
			continue
		}
		if !seen[action] {
			seen[action] = true
			actions = append(actions, action)
		}
	}
	// REMOVE is always permitted: an unsupported assertion the bounded
	// investigation cannot establish must never be published.
	if !seen["REMOVE"] {
		actions = append(actions, "REMOVE")
	}
	return actions
}

// PreservedClaimsBlock renders the valid claims a revision must keep. It is
// bounded like the repair list: preservation is a contract over the answer's
// assertions, not a request to repeat the entire draft.
func PreservedClaimsBlock(claims []string) string {
	lines := make([]string, 0, len(claims))
	seen := map[string]bool{}
	for _, claim := range claims {
		claim = strings.Join(strings.Fields(claim), " ")
		if claim == "" || seen[claim] {
			continue
		}
		seen[claim] = true
		lines = append(lines, "- "+boundRepair(claim, 180))
		if len(lines) >= MaxPreservedClaims {
			break
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return "Already supported claims that must survive this repair:\n" + strings.Join(lines, "\n")
}

// MaxPreservedClaims bounds the preservation contract carried to the model.
const MaxPreservedClaims = 12

// completionRepairDirective renders the claim-level correction. It falls back
// to the prose directive when the run has no structured repairs or the host did
// not install a repair template.
//
// Repairs are capped and each line is bounded, so a correction request is
// proportional to the unresolved claims rather than to the answer length. The
// original reason is retained at the end: it is the audit's own wording and the
// fallback if the model ignores the structured list.
func completionRepairDirective(template, fallback, reason string, repairs []contract.GroundingIssue, budget int) string {
	return completionRepairDirectiveWithPreserved(template, fallback, reason, repairs, nil, budget)
}

// completionRepairDirectiveWithPreserved renders the same directive with the
// valid claims a revision must carry forward. The model sees semantic repair
// requirements, never a scheduler allowance.
func completionRepairDirectiveWithPreserved(template, fallback, reason string, repairs []contract.GroundingIssue, preserved []string, budget int) string {
	reason = strings.TrimSpace(reason)
	repairs = usableRepairs(repairs)
	if reason == "" || len(repairs) == 0 {
		return completionReviewDirective(fallback, reason)
	}
	template = strings.TrimSpace(template)
	if template == "" {
		return completionReviewDirective(fallback, reason)
	}
	if budget < 0 {
		budget = 0
	}
	return strings.NewReplacer(
		"{{count}}", strconv.Itoa(len(repairs)),
		"{{budget}}", strconv.Itoa(budget),
		"{{repairs}}", RepairInstructions(repairs),
		"{{preserved}}", PreservedClaimsBlock(preserved),
		"{{actions}}", strings.Join(RepairActions(repairs), ", "),
		"{{reason}}", reason,
	).Replace(template)
}

// usableRepairs caps the list and drops entries without a repairable claim.
func usableRepairs(repairs []contract.GroundingIssue) []contract.GroundingIssue {
	out := make([]contract.GroundingIssue, 0, MaxRepairInstructions)
	for _, repair := range repairs {
		if strings.TrimSpace(repair.Category) == "" || strings.TrimSpace(repair.Detail) == "" {
			continue
		}
		out = append(out, repair)
		if len(out) >= MaxRepairInstructions {
			break
		}
	}
	return out
}

// RepairInstructions renders one bounded line per claim. It is the single
// formatter for machine-readable repair instructions: the run's correction
// directive, a resumed task's orientation, and the human review surface all
// render the same line for the same issue, so no consumer re-derives a
// second format for the same data.
func RepairInstructions(repairs []contract.GroundingIssue) string {
	lines := make([]string, 0, len(repairs))
	for index, repair := range repairs {
		var line strings.Builder
		fmt.Fprintf(&line, "%d. [%s] ", index+1, boundRepair(repair.Category, 24))
		if claim := strings.TrimSpace(repair.Claim); claim != "" {
			line.WriteString("\"")
			line.WriteString(boundRepair(claim, 90))
			line.WriteString("\"")
		} else if reference := strings.TrimSpace(repair.Reference); reference != "" {
			line.WriteString(boundRepair(reference, 90))
		} else if detail := strings.TrimSpace(repair.Detail); detail != "" {
			line.WriteString(boundRepair(detail, 120))
		} else {
			line.WriteString("unresolved claim")
		}
		if requirement := strings.TrimSpace(repair.Requirement); requirement != "" {
			line.WriteString(" — required: ")
			line.WriteString(boundRepair(requirement, 80))
		}
		if action := strings.TrimSpace(repair.Action); action != "" {
			line.WriteString("; action: ")
			line.WriteString(boundRepair(action, 120))
		}
		if location := strings.TrimSpace(repair.Location); location != "" {
			line.WriteString("; at: ")
			line.WriteString(boundRepair(location, 140))
		}
		lines = append(lines, line.String())
	}
	return strings.Join(lines, "\n")
}

// ReviewInformationalAnswerWithProvenance adds the documentation-only claim
// audit to the pre-answer review. The provenance is the last projection's
// evidence summary, so the model can attribute a documented term or inspect the
// source before the answer is published instead of learning the boundary only
// from the completion veto.
func ReviewInformationalAnswerWithProvenance(intent contract.IntentContract, turns []Turn, answer string, provenance EvidenceProvenance) string {
	if reason := ReviewInformationalAnswer(intent, turns, answer); reason != "" {
		return reason
	}
	if intent.Kind != contract.ObjectiveInformational || strings.TrimSpace(answer) == "" {
		return ""
	}
	if assessment := AssessAnswerProvenance(intent, answer, provenance); !assessment.Pass() {
		return assessment.Reason()
	}
	return ""
}

// ReviewInformationalRepairsWithProvenance is the claim-level projection of the
// combined review. It uses the same audits and adds no new rule.
func ReviewInformationalRepairsWithProvenance(intent contract.IntentContract, turns []Turn, answer string, provenance EvidenceProvenance) []contract.GroundingIssue {
	repairs := ReviewInformationalRepairs(intent, turns, answer)
	if intent.Kind != contract.ObjectiveInformational || strings.TrimSpace(answer) == "" {
		return repairs
	}
	return append(repairs, AssessAnswerProvenance(intent, answer, provenance).Repairs()...)
}

// SetInformationalReviews installs the combined answer review and its
// claim-level planner for one informational contract. provenance is evaluated
// at review time; it is the host's last projection summary, never model text.
func (l *Loop) SetInformationalReviews(intent contract.IntentContract, provenance func() EvidenceProvenance) {
	if l == nil {
		return
	}
	summary := func() EvidenceProvenance {
		if provenance == nil {
			return EvidenceProvenance{}
		}
		return provenance()
	}
	l.cfg.AnswerReview = func(turns []Turn, answer string) string {
		// The projection's frontier verdict outranks the prose audit. A model
		// can write a plausible, internally consistent answer before the
		// evidence that establishes it exists; that is a premature candidate,
		// not a result. The correction request reopens the surface and names
		// the missing requirement.
		if intent.Kind == contract.ObjectiveInformational {
			if gap := l.evidenceGap(); gap != "" {
				return "the runtime still lacks required evidence: " + gap
			}
		}
		return ReviewInformationalAnswerWithProvenance(intent, turns, answer, summary())
	}
	l.cfg.AnswerRepairs = func(turns []Turn, answer string) []contract.GroundingIssue {
		return ReviewInformationalRepairsWithProvenance(intent, turns, answer, summary())
	}
}

// ClearInformationalReviews removes both answer callbacks.
func (l *Loop) ClearInformationalReviews() {
	if l == nil {
		return
	}
	l.cfg.AnswerReview = nil
	l.cfg.AnswerRepairs = nil
}

// evidenceGap reads the host's current frontier verdict. It is empty when no
// evidence source is installed or the frontier is satisfied.
func (l *Loop) evidenceGap() string {
	if l == nil || l.cfg.EvidenceGap == nil {
		return ""
	}
	return strings.TrimSpace(l.cfg.EvidenceGap())
}
