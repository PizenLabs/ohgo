package agent

import (
	"fmt"
	"sort"
	"strings"

	"github.com/PizenLabs/ohgo/internal/control/contract"
)

// AnswerDraftState is the lifecycle of one internal answer candidate.
//
// A draft is never a user-visible answer. Only a draft the response transaction
// commits may be projected by a host. The states exist so review, repair, and
// supersession are semantic facts rather than renderer inferences.
type AnswerDraftState string

// Draft lifecycle states.
const (
	// AnswerDraftCandidate is a synthesized answer the review has not judged.
	AnswerDraftCandidate AnswerDraftState = "CANDIDATE"
	// AnswerDraftUnderReview is a candidate the claim audit is evaluating.
	AnswerDraftUnderReview AnswerDraftState = "UNDER_REVIEW"
	// AnswerDraftRepairRequired carries at least one claim that must be
	// verified, attributed, weakened, replaced, or removed.
	AnswerDraftRepairRequired AnswerDraftState = "REPAIR_REQUIRED"
	// AnswerDraftSuperseded was replaced by a later revision. It remains
	// internal history and must never be shown as an answer or a withdrawal.
	AnswerDraftSuperseded AnswerDraftState = "SUPERSEDED"
	// AnswerDraftRejectedTerminal is a candidate no valid answer could be
	// recovered from. It may produce exactly one refusal artifact.
	AnswerDraftRejectedTerminal AnswerDraftState = "REJECTED_TERMINAL"
	// AnswerDraftCommitted is the authoritative user-facing answer.
	AnswerDraftCommitted AnswerDraftState = "COMMITTED"
)

// AnswerClaimState is the review state of one sentence-sized assertion.
type AnswerClaimState string

// Claim states.
const (
	// ClaimProven is an assertion the deterministic audit certified.
	ClaimProven AnswerClaimState = "PROVEN"
	// ClaimDocumented is an assertion that names its documentation source.
	ClaimDocumented AnswerClaimState = "DOCUMENTED"
	// ClaimRepairRequired is the only state a review failure may assign. It
	// scopes the failure to this claim, not to the draft.
	ClaimRepairRequired AnswerClaimState = "REPAIR_REQUIRED"
	// ClaimRemoved means a repair removed the claim after evidence failed to
	// support it. The rest of the draft remains valid.
	ClaimRemoved AnswerClaimState = "REMOVED"
	// ClaimStructural marks headings, fences, and separators: text that must
	// survive revision but is not a factual assertion.
	ClaimStructural AnswerClaimState = "STRUCTURAL"
)

// AnswerClaim is one sentence-sized unit of an answer draft.
type AnswerClaim struct {
	ID     string                    `json:"id"`
	Text   string                    `json:"text"`
	State  AnswerClaimState          `json:"state"`
	Index  int                       `json:"index"`
	Issues []contract.GroundingIssue `json:"issues,omitempty"`
	// Structural marks headings, table separators, and fenced code. It is
	// preserved through revision but never counted as a factual claim.
	Structural bool `json:"structural,omitempty"`

	// unit is the exact source segment (text plus trailing separator). It is
	// never serialized: it exists so a fallback repair can remove one claim
	// without rebuilding the whole answer from prose.
	unit answerUnit
}

// Repairable reports whether the claim is a factual assertion the audit must
// keep, repair, or remove.
func (c AnswerClaim) Repairable() bool {
	return !c.Structural && strings.TrimSpace(c.Text) != ""
}

// Valid reports whether the claim may survive into a committed answer.
func (c AnswerClaim) Valid() bool {
	return c.State == ClaimProven || c.State == ClaimDocumented || c.State == ClaimStructural
}

// AnswerDraft is the claim-preserving semantic version of one answer candidate.
//
// Review is claim-scoped: a failing sentence becomes ClaimRepairRequired while
// every other claim keeps its state and text. A revision may replace the draft,
// but a valid claim from the strongest draft is never discarded merely because
// a different claim failed.
type AnswerDraft struct {
	Version    int              `json:"version"`
	Supersedes int              `json:"supersedes,omitempty"`
	State      AnswerDraftState `json:"state"`
	Text       string           `json:"text"`
	Claims     []AnswerClaim    `json:"claims,omitempty"`

	// RepairIssues are review findings that could not be attached to one
	// claim. They make a deterministic removal unsafe; only a model revision
	// may repair them.
	RepairIssues []contract.GroundingIssue `json:"repair_issues,omitempty"`
	// FacetIssues are requested-deliverable gaps. They are completion gaps the
	// answer may honestly state, not false factual claims, so they never
	// invalidate a sentence.
	FacetIssues []contract.GroundingIssue `json:"facet_issues,omitempty"`
	// MissingPreserved names claims from the strongest draft that a revision
	// did not carry forward.
	MissingPreserved []string `json:"missing_preserved,omitempty"`
	// PreservationLost reports that the revision was materially poorer than
	// the draft it replaced.
	PreservationLost bool `json:"preservation_lost,omitempty"`
}

// ValidClaims returns the factual claims a revision must preserve.
func (d AnswerDraft) ValidClaims() []AnswerClaim {
	var out []AnswerClaim
	for _, claim := range d.Claims {
		if claim.Repairable() && claim.Valid() {
			out = append(out, claim)
		}
	}
	return out
}

// RepairClaims returns the claims a review marked for repair.
func (d AnswerDraft) RepairClaims() []AnswerClaim {
	var out []AnswerClaim
	for _, claim := range d.Claims {
		if claim.Repairable() && claim.State == ClaimRepairRequired {
			out = append(out, claim)
		}
	}
	return out
}

// ClaimTexts returns the text of every valid factual claim, in answer order.
func (d AnswerDraft) ClaimTexts() []string {
	claims := d.ValidClaims()
	out := make([]string, 0, len(claims))
	for _, claim := range claims {
		out = append(out, claim.Text)
	}
	return out
}

// ReviewIssues returns every finding the review attached to this draft.
//
// It is the draft-level projection of the same findings the correction
// directive names: claim findings first, then findings the audit could not
// locate to one claim, then requested-deliverable gaps. A completion verdict
// that refuses an unpublished candidate uses this list so the durable task
// keeps the exact claims instead of only the review's prose boundary.
func (d AnswerDraft) ReviewIssues() []contract.GroundingIssue {
	out := make([]contract.GroundingIssue, 0, len(d.RepairIssues)+len(d.FacetIssues))
	for _, claim := range d.Claims {
		out = append(out, claim.Issues...)
	}
	out = append(out, d.RepairIssues...)
	out = append(out, d.FacetIssues...)
	return out
}

// answerUnit is a sentence-sized answer segment with the whitespace that
// followed it. Keeping the separator makes removal exact: the surviving answer
// is the original bytes minus the rejected segment, not a re-rendered prose
// approximation.
type answerUnit struct {
	Text string
	Sep  string
}

// newAnswerDraft builds a draft and attaches every review issue to the claim it
// names. An issue the audit cannot locate stays draft-level; a deterministic
// repair must not guess which sentence it meant.
func newAnswerDraft(version, supersedes int, text string, issues []contract.GroundingIssue) AnswerDraft {
	units := splitAnswerUnits(text)
	draft := AnswerDraft{
		Version:    version,
		Supersedes: supersedes,
		State:      AnswerDraftCandidate,
		Text:       text,
		Claims:     make([]AnswerClaim, 0, len(units)),
	}
	for index, unit := range units {
		claimText := strings.TrimSpace(unit.Text)
		if claimText == "" {
			continue
		}
		state := ClaimProven
		structural := answerUnitIsStructural(claimText)
		switch {
		case structural:
			state = ClaimStructural
		case hasAttribution(claimText):
			state = ClaimDocumented
		}
		draft.Claims = append(draft.Claims, AnswerClaim{
			ID:         fmt.Sprintf("v%d-c%d", version, index+1),
			Text:       claimText,
			State:      state,
			Index:      index,
			Structural: structural,
			unit:       unit,
		})
	}
	draft.applyIssues(issues)
	return draft
}

// applyIssues marks only the claims the issues name. It is idempotent and keeps
// the first issue ordering stable for audit.
func (d *AnswerDraft) applyIssues(issues []contract.GroundingIssue) {
	if len(issues) == 0 {
		return
	}
	for _, issue := range issues {
		if isFacetIssue(issue) {
			d.FacetIssues = appendIssueOnce(d.FacetIssues, issue)
			continue
		}
		index, ok := matchIssueClaim(d.Claims, issue)
		if !ok {
			d.RepairIssues = appendIssueOnce(d.RepairIssues, issue)
			continue
		}
		claim := &d.Claims[index]
		if claim.Structural {
			d.RepairIssues = appendIssueOnce(d.RepairIssues, issue)
			continue
		}
		claim.State = ClaimRepairRequired
		claim.Issues = appendIssueOnce(claim.Issues, issue)
	}
	if len(d.RepairClaims()) > 0 || len(d.RepairIssues) > 0 {
		d.State = AnswerDraftRepairRequired
	}
}

// invalidateClaims applies only issues that name an existing claim. It is used
// when a newer revision re-raises a claim the strongest draft already holds: a
// finding about different text must not make the strongest draft unsalvageable.
func (d *AnswerDraft) invalidateClaims(issues []contract.GroundingIssue) {
	for _, issue := range issues {
		if isFacetIssue(issue) {
			continue
		}
		index, ok := matchIssueClaim(d.Claims, issue)
		if !ok {
			continue
		}
		claim := &d.Claims[index]
		if claim.Structural {
			continue
		}
		claim.State = ClaimRepairRequired
		claim.Issues = appendIssueOnce(claim.Issues, issue)
	}
	if len(d.RepairClaims()) > 0 {
		d.State = AnswerDraftRepairRequired
	}
}

// appendIssueOnce keeps one issue identity in a list.
func appendIssueOnce(issues []contract.GroundingIssue, issue contract.GroundingIssue) []contract.GroundingIssue {
	for _, existing := range issues {
		if existing.Code == issue.Code && existing.Claim == issue.Claim &&
			existing.Location == issue.Location && existing.Category == issue.Category {
			return issues
		}
	}
	return append(issues, issue)
}

// matchIssueClaim finds the one claim an issue names. It prefers the audit's
// Location (the sentence it read), then the claim term, then the reference.
func matchIssueClaim(claims []AnswerClaim, issue contract.GroundingIssue) (int, bool) {
	needles := []string{strings.TrimSpace(issue.Location), strings.TrimSpace(issue.Claim), strings.TrimSpace(issue.Reference)}
	for _, needle := range needles {
		if needle == "" {
			continue
		}
		bestIndex, bestScore, ties := -1, 0.0, 0
		for index, claim := range claims {
			if claim.Structural {
				continue
			}
			score := claimMatchScore(claim.Text, needle)
			switch {
			case score > bestScore:
				bestScore, bestIndex, ties = score, index, 1
			case score == bestScore && score > 0:
				ties++
			}
		}
		// A term that names several sentences is not a safe locator. The
		// audit's sentence Location wins when it supplied one; otherwise the
		// finding stays draft-level instead of deleting a guessed claim.
		if bestIndex >= 0 && bestScore >= 0.5 && ties == 1 {
			return bestIndex, true
		}
	}
	return -1, false
}

// claimMatchScore scores how well a needle identifies one claim. A term that
// occurs in several claims is not a safe locator unless the audit also supplied
// the sentence, so token overlap alone stays below the acceptance threshold.
func claimMatchScore(claim, needle string) float64 {
	claimText := normalizeClaim(claim)
	needleText := normalizeClaim(needle)
	if claimText == "" || needleText == "" {
		return 0
	}
	if claimText == needleText {
		return 1
	}
	if strings.Contains(claimText, needleText) || strings.Contains(needleText, claimText) {
		return 0.95
	}
	return claimSimilarity(claim, needle)
}

// claimSimilarity is a token Jaccard score with a containment boost. It is used
// to recognise a preserved claim after a model rephrases it, not to judge
// truth.
func claimSimilarity(a, b string) float64 {
	left := claimTokens(a)
	right := claimTokens(b)
	if len(left) == 0 || len(right) == 0 {
		return 0
	}
	inter := 0
	for token := range left {
		if right[token] {
			inter++
		}
	}
	union := len(left) + len(right) - inter
	if union == 0 {
		return 0
	}
	score := float64(inter) / float64(union)
	// A short claim ("uses SQLite") is fully contained in a longer sentence;
	// Jaccard alone would discard it. Containment is a preservation signal, not
	// a claim-equivalence proof.
	if inter == len(left) || inter == len(right) {
		score = max(score, 0.75)
	}
	return score
}

// normalizeClaim lowercases a claim and keeps its word content.
func normalizeClaim(text string) string {
	var b strings.Builder
	space := false
	for _, r := range strings.ToLower(text) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-', r == '.':
			b.WriteRune(r)
			space = false
		default:
			if !space && b.Len() > 0 {
				b.WriteByte(' ')
				space = true
			}
		}
	}
	return strings.TrimSpace(b.String())
}

// claimTokens returns the content tokens of a claim. Generic grammar words are
// dropped so "The project uses SQLite." and "SQLite is used." still align.
func claimTokens(text string) map[string]bool {
	out := map[string]bool{}
	for _, token := range strings.Fields(normalizeClaim(text)) {
		if len(token) < 3 || genericClaimTerm(token) {
			continue
		}
		out[token] = true
	}
	return out
}

// claimPresent reports whether a draft claim survives into revision text.
func claimPresent(claim string, units []answerUnit) bool {
	normalized := normalizeClaim(claim)
	if normalized == "" {
		return true
	}
	best := 0.0
	for _, unit := range units {
		candidate := normalizeClaim(unit.Text)
		if candidate == "" {
			continue
		}
		if strings.Contains(candidate, normalized) || strings.Contains(normalized, candidate) {
			// A very short fragment is not a preserved claim.
			if len(strings.Fields(candidate)) >= 3 || len(strings.Fields(normalized)) >= 3 {
				return true
			}
		}
		if score := claimSimilarity(unit.Text, claim); score > best {
			best = score
		}
	}
	return best >= 0.6
}

// missingClaims returns the valid factual claims absent from a revision.
func missingClaims(best AnswerDraft, revision string) []AnswerClaim {
	units := splitAnswerUnits(revision)
	var out []AnswerClaim
	for _, claim := range best.ValidClaims() {
		if !claimPresent(claim.Text, units) {
			out = append(out, claim)
		}
	}
	return out
}

// answerUnitIsStructural reports text that carries answer structure rather than
// a factual assertion. Structural text is preserved but never counted as a
// claim and never targeted by a repair.
func answerUnitIsStructural(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return true
	}
	if strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "```") {
		return true
	}
	// A table separator row is structure.
	if strings.Contains(trimmed, "|") {
		only := strings.NewReplacer("|", "", "-", "", ":", "", " ", "").Replace(trimmed)
		if only == "" {
			return true
		}
	}
	return false
}

// fencedRanges returns the byte ranges occupied by fenced code blocks. A
// sentence boundary inside a fence is code, not a claim, so the splitter must
// leave it intact.
func fencedRanges(text string) [][2]int {
	var ranges [][2]int
	start := -1
	offset := 0
	for _, line := range strings.SplitAfter(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			if start < 0 {
				start = offset
			} else {
				ranges = append(ranges, [2]int{start, offset + len(line)})
				start = -1
			}
		}
		offset += len(line)
	}
	if start >= 0 {
		ranges = append(ranges, [2]int{start, len(text)})
	}
	return ranges
}

func insideFence(index int, ranges [][2]int) bool {
	for _, span := range ranges {
		if index >= span[0] && index < span[1] {
			return true
		}
	}
	return false
}

// splitAnswerUnits splits an answer at sentence and line boundaries while
// preserving the exact text and separator. Fenced code is one unit so a repair
// cannot corrupt it.
func splitAnswerUnits(text string) []answerUnit {
	if text == "" {
		return nil
	}
	fences := fencedRanges(text)
	var units []answerUnit
	prev := 0
	for _, boundary := range claimSentenceBoundary.FindAllStringIndex(text, -1) {
		start, end := boundary[0], boundary[1]
		if start <= prev || insideFence(start, fences) {
			continue
		}
		claimText, sep := text[prev:start], text[start:end]
		// The regex boundary includes terminal punctuation. Keep it on the
		// claim so a preserved sentence remains a complete, readable assertion
		// instead of losing its period.
		if sep != "" && (sep[0] == '.' || sep[0] == '!' || sep[0] == '?') {
			at := 0
			for at < len(sep) && (sep[at] == '.' || sep[at] == '!' || sep[at] == '?') {
				at++
			}
			claimText += sep[:at]
			sep = sep[at:]
		}
		units = append(units, answerUnit{Text: claimText, Sep: sep})
		prev = end
	}
	if prev < len(text) {
		units = append(units, answerUnit{Text: text[prev:]})
	}
	if len(units) == 0 {
		units = append(units, answerUnit{Text: text})
	}
	// Merge a trailing separator-only unit back into the previous claim so
	// reconstruction does not create empty claims.
	out := units[:0]
	for _, unit := range units {
		if strings.TrimSpace(unit.Text) == "" && len(out) > 0 {
			out[len(out)-1].Sep += unit.Text + unit.Sep
			continue
		}
		out = append(out, unit)
	}
	return out
}

// salvageDraft removes every claim a review marked for repair. It is the
// deterministic fallback when a model revision is missing valid claims or the
// correction budget is spent: independently supported content survives, and
// only unsupported assertions are removed.
func salvageDraft(draft AnswerDraft) (string, AnswerDraft, bool) {
	if len(draft.RepairIssues) > 0 {
		// An unlocated audit finding means the runtime cannot remove the right
		// sentence; removing something else would destroy valid work.
		return "", draft, false
	}
	repair := map[int]bool{}
	for _, claim := range draft.Claims {
		if claim.Repairable() && claim.State == ClaimRepairRequired {
			repair[claim.Index] = true
		}
	}
	if len(repair) == 0 {
		return "", draft, false
	}
	units := splitAnswerUnits(draft.Text)
	kept := make([]answerUnit, 0, len(units))
	removed := 0
	for index, unit := range units {
		if repair[index] {
			removed++
			// A clause can be separated from the previous clause by a
			// semicolon or comma that belongs to the previous unit's
			// separator. Deleting only the clause leaves that punctuation
			// dangling and glues the next surviving unit onto it, producing
			// text such as "…favicon; ## Purpose". Transfer the separator
			// that followed the removed clause to the last survivor so the
			// boundary remains grammatical.
			if len(kept) > 0 && isClauseSeparator(kept[len(kept)-1].Sep) {
				kept[len(kept)-1].Sep = separatorAfterRemoval(unit)
			}
			continue
		}
		kept = append(kept, unit)
	}
	if len(kept) == 0 {
		return "", draft, false
	}
	var b strings.Builder
	for _, unit := range kept {
		b.WriteString(unit.Text)
		b.WriteString(unit.Sep)
	}
	text := strings.TrimSpace(b.String())
	if text == "" || removed == 0 {
		return "", draft, false
	}
	salvaged := draft
	salvaged.Text = text
	salvaged.State = AnswerDraftCandidate
	salvaged.Claims = append([]AnswerClaim(nil), draft.Claims...)
	for index := range salvaged.Claims {
		if salvaged.Claims[index].State == ClaimRepairRequired && repair[salvaged.Claims[index].Index] {
			salvaged.Claims[index].State = ClaimRemoved
		}
	}
	return text, salvaged, true
}

// isClauseSeparator reports a separator that joins two clauses of one sentence
// rather than ending a sentence or paragraph. Removing the clause after it must
// also remove the connector; otherwise the surviving punctuation dangles.
func isClauseSeparator(sep string) bool {
	trimmed := strings.TrimLeft(sep, " \t")
	if trimmed == "" || strings.ContainsAny(trimmed, "\n.!?") {
		return false
	}
	return strings.HasPrefix(trimmed, ";") || strings.HasPrefix(trimmed, ",")
}

// separatorAfterRemoval returns the separator the previous survivor should keep
// once unit is removed. The removed clause's own terminal punctuation and
// separator replace the connector, so "a; b. c" becomes "a. c" and "a; b; c"
// becomes "a; c". A heading after the removed clause keeps its paragraph break.
func separatorAfterRemoval(unit answerUnit) string {
	trimmed := strings.TrimLeft(unit.Sep, " \t")
	if strings.HasPrefix(trimmed, ";") || strings.HasPrefix(trimmed, ",") {
		return unit.Sep
	}
	if end := terminalPunctuation(unit.Text); end != "" {
		return end + unit.Sep
	}
	return unit.Sep
}

// terminalPunctuation returns the trailing sentence punctuation of a claim, or
// "" when the text ended without one. The splitter keeps terminal punctuation
// on the claim, so removing that claim would otherwise drop the only mark that
// ended the sentence.
func terminalPunctuation(text string) string {
	text = strings.TrimRight(text, " \t\r\n")
	for i := len(text) - 1; i >= 0; i-- {
		switch text[i] {
		case '.', '!', '?':
			continue
		}
		return text[i+1:]
	}
	return text
}

// DraftAudit is the bounded audit projection of one draft. Hosts never need
// the full claim text to tell superseded history from a committed answer.
type DraftAudit struct {
	Version          int              `json:"version"`
	Supersedes       int              `json:"supersedes,omitempty"`
	State            AnswerDraftState `json:"state"`
	Claims           int              `json:"claims,omitempty"`
	ValidClaims      int              `json:"valid_claims,omitempty"`
	RepairClaims     int              `json:"repair_claims,omitempty"`
	MissingPreserved []string         `json:"missing_preserved,omitempty"`
	PreservationLost bool             `json:"preservation_lost,omitempty"`
}

func summarizeDraft(draft AnswerDraft) DraftAudit {
	return DraftAudit{
		Version:          draft.Version,
		Supersedes:       draft.Supersedes,
		State:            draft.State,
		Claims:           len(draft.Claims),
		ValidClaims:      len(draft.ValidClaims()),
		RepairClaims:     len(draft.RepairClaims()),
		MissingPreserved: append([]string(nil), draft.MissingPreserved...),
		PreservationLost: draft.PreservationLost,
	}
}

// sortDraftAudits orders lineage by version.
func sortDraftAudits(summaries []DraftAudit) {
	sort.SliceStable(summaries, func(i, j int) bool { return summaries[i].Version < summaries[j].Version })
}
