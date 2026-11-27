package agent

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/PizenLabs/ohgo/internal/control/contract"
)

// AnswerPlanToolName is the control tool a model calls to preflight the claims
// of its final answer before writing prose.
//
// The plan is not evidence and not a publication decision. It exists so the
// deterministic claim audits can name a missing read while the action surface
// is still open, instead of spending an answer turn on prose that the review
// will refuse afterwards.
const AnswerPlanToolName = "prepare_answer"

// Answer-plan bounds. They match the correction contract: a plan is a bounded
// preflight, not a second answer.
const (
	// MaxAnswerPlanClaims bounds the assertions one plan may name.
	MaxAnswerPlanClaims = 12
	// MaxAnswerPlanLimitations bounds the honest limitations one plan may name.
	MaxAnswerPlanLimitations = 6
	maxAnswerPlanClaimBytes  = 600
	// MaxAnswerPlanAttempts bounds how many refused plans may reopen the
	// investigation surface before the normal publication review takes over.
	// The plan tool is withheld until an observation is recorded, so each
	// counted retry follows new evidence rather than repeating the same
	// proposal. A plan that cannot converge must not become an infinite
	// preflight.
	MaxAnswerPlanAttempts = 3
)

// AnswerPlan is the model's declaration of what its final answer will assert
// and what it will leave honestly unverified.
//
// Claims are audited against the same evidence the answer review uses.
// Limitations are not audited as claims: they are the honest scope statements
// the final answer is allowed to make when the run's observations cannot
// establish a requested detail.
type AnswerPlan struct {
	// Claims are complete declarative sentences the answer intends to make.
	Claims []string
	// Limitations are facts the answer will explicitly leave unverified.
	Limitations []string
}

// AnswerPlanResult is the tool's structured outcome.
//
// Reviewed distinguishes a preflight the runtime actually audited from a plan
// recorded by a host that installed no plan review. Only a reviewed result may
// move the run's plan state.
type AnswerPlanResult struct {
	// Reviewed reports that the runtime audited the claims.
	Reviewed bool
	// Accepted reports that every declared claim is supported.
	Accepted bool
	// Claims is the number of assertions the plan declared.
	Claims int
	// Issues are the refused claims, bounded to the repair instruction limit.
	Issues []contract.GroundingIssue
	// Plan is the normalised declaration this result describes. An accepted
	// plan is returned so the loop can carry its exact claims into the answer
	// request; the model must compose from that ledger, not an earlier draft.
	Plan AnswerPlan
}

// ParseAnswerPlan validates and normalises the tool arguments.
//
// It is the one parser for the plan wire shape. The tool and every test use
// it, so a plan cannot be accepted by one path and rejected by another.
func ParseAnswerPlan(args map[string]any) (AnswerPlan, error) {
	if args == nil {
		return AnswerPlan{}, fmt.Errorf("claims are required")
	}
	claims, err := planStringList(args["claims"], "claims", MaxAnswerPlanClaims, maxAnswerPlanClaimBytes)
	if err != nil {
		return AnswerPlan{}, err
	}
	if len(claims) == 0 {
		return AnswerPlan{}, fmt.Errorf("claims must contain at least one declarative sentence")
	}
	limitations, err := planStringList(args["limitations"], "limitations", MaxAnswerPlanLimitations, maxAnswerPlanClaimBytes)
	if err != nil {
		return AnswerPlan{}, err
	}
	return AnswerPlan{Claims: claims, Limitations: limitations}, nil
}

// planStringList normalises one string list argument.
func planStringList(value any, name string, limit int, maxBytes int) ([]string, error) {
	if value == nil {
		return nil, nil
	}
	var raw []string
	switch typed := value.(type) {
	case []string:
		raw = typed
	case []any:
		for index, entry := range typed {
			text, ok := entry.(string)
			if !ok {
				return nil, fmt.Errorf("%s[%d] must be a string", name, index)
			}
			raw = append(raw, text)
		}
	default:
		return nil, fmt.Errorf("%s must be an array of strings", name)
	}
	if len(raw) > limit {
		return nil, fmt.Errorf("%s supports at most %d entries", name, limit)
	}
	out := make([]string, 0, len(raw))
	seen := map[string]bool{}
	for _, entry := range raw {
		text := strings.Join(strings.Fields(strings.TrimSpace(entry)), " ")
		if text == "" {
			continue
		}
		if len(text) > maxBytes {
			return nil, fmt.Errorf("%s entry is longer than %d bytes; state one sentence-sized claim", name, maxBytes)
		}
		if seen[text] {
			continue
		}
		seen[text] = true
		out = append(out, text)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// ReviewAnswerPlan runs the answer claim audit against the plan's assertions
// before any answer prose is published.
//
// It is a projection of the same deterministic audits FinalizeAnswer and the
// correction review use; it adds no second notion of truth. Limitations are
// excluded because they are honest scope statements, not claims. Requested
// deliverables are also excluded from the preflight: the final answer may
// honestly report a facet as unverified, and completion owns that gap. The
// preflight refuses only unsupported factual claims.
func ReviewAnswerPlan(intent contract.IntentContract, turns []Turn, plan AnswerPlan, provenance EvidenceProvenance) []contract.GroundingIssue {
	if intent.Kind != contract.ObjectiveInformational || len(plan.Claims) == 0 {
		return nil
	}
	candidate := strings.Join(plan.Claims, "\n\n")
	issues := dedupeGroundingIssues(auditAnswerClaims(intent, turns, candidate, provenance))
	claims := issues[:0]
	for _, issue := range issues {
		if isFacetIssue(issue) {
			// A missing or honestly unverified deliverable is a completion
			// gap, not a false claim. The final answer audit and completion
			// still require it to be stated; refusing the plan here would trap
			// the model between over-claiming and an unreachable preflight.
			continue
		}
		claims = append(claims, issue)
	}
	return usableRepairs(claims)
}

// AnswerPlanLedgerText renders the accepted claims as a bounded ledger the
// final answer must compose from. It is the one formatter for both the tool
// receipt and the answer-request directive; a second rendering could let the
// model see a different claim than the audit accepted.
func AnswerPlanLedgerText(plan AnswerPlan) (claims, limitations string) {
	var b strings.Builder
	for index, claim := range plan.Claims {
		if index > 0 {
			b.WriteByte('\n')
		}
		b.WriteString("- ")
		b.WriteString(boundRepair(claim, maxAnswerPlanClaimBytes))
	}
	claims = b.String()
	b.Reset()
	for index, limitation := range plan.Limitations {
		if index > 0 {
			b.WriteByte('\n')
		}
		b.WriteString("- ")
		b.WriteString(boundRepair(limitation, maxAnswerPlanClaimBytes))
	}
	limitations = b.String()
	if limitations == "" {
		limitations = "- none declared"
	}
	return claims, limitations
}

// RenderAnswerPlanLedger fills a host-supplied ledger template. The template
// is data from the prompt package, not a rule; the loop supplies only the
// accepted claims it audited.
func RenderAnswerPlanLedger(template string, plan AnswerPlan) string {
	template = strings.TrimSpace(template)
	if template == "" || len(plan.Claims) == 0 {
		return ""
	}
	claims, limitations := AnswerPlanLedgerText(plan)
	out := strings.ReplaceAll(template, "{{claims}}", claims)
	return strings.ReplaceAll(out, "{{limitations}}", limitations)
}

// ReviewAnswerPlanConformance checks the final prose against the accepted
// plan's documented claims. An accepted plan is a claim-strength contract, not
// merely a gate the prose may leave behind. When the plan declared a claim as
// documentation-attributed, the final answer may keep that attribution; it may
// not strengthen the same subject into an unattributed world fact.
func ReviewAnswerPlanConformance(turns []Turn, answer string, plan AnswerPlan) []contract.GroundingIssue {
	if strings.TrimSpace(answer) == "" || len(plan.Claims) == 0 {
		return nil
	}
	documented := map[string]string{} // term -> accepted claim
	for _, claim := range plan.Claims {
		if !hasAttribution(claim) {
			continue
		}
		for _, term := range planDocumentedTerms(claim) {
			if _, exists := documented[term]; !exists {
				documented[term] = claim
			}
		}
	}
	if len(documented) == 0 {
		return nil
	}
	var issues []contract.GroundingIssue
	seen := map[string]bool{}
	attributedSection := false
	previousAttributed := false
	for _, line := range strings.Split(answer, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if answerSectionHeading(trimmed) {
			attributedSection = hasAttribution(trimmed)
			previousAttributed = false
			continue
		}
		lineAttributed := hasAttribution(trimmed)
		for _, raw := range claimSentenceBoundary.Split(strings.TrimSpace(line), -1) {
			clause := strings.TrimSpace(raw)
			if clause == "" || answerUnitIsStructural(clause) || attributedSection || previousAttributed || hasAttribution(clause) {
				continue
			}
			if !planClauseStrengthensClaim(clause) {
				continue
			}
			term, accepted := planDocumentedTermIn(clause, documented)
			if term == "" || seen[clause+"\x00"+term] {
				continue
			}
			seen[clause+"\x00"+term] = true
			issues = append(issues, contract.GroundingIssue{
				Code:        "plan_claim_strengthened",
				Detail:      fmt.Sprintf("the accepted plan recorded %q as a documentation claim; the final answer states it without attribution", term),
				Claim:       clause,
				Location:    clause,
				Category:    "attribution",
				Requirement: "the accepted plan's wording or an observed source",
				Reference:   term,
				Action:      "use the accepted plan's wording: " + boundRepair(accepted, 240),
			})
			if len(issues) >= MaxRepairInstructions {
				return issues
			}
		}
		previousAttributed = lineAttributed
	}
	return issues
}

// answerSectionHeading reports a line that starts a new prose section. Both a
// Markdown heading and a bold-only label are section boundaries; treating the
// bold label as ordinary prose would carry an attribution into the next
// unrelated section.
func answerSectionHeading(line string) bool {
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "#") {
		return true
	}
	return strings.HasPrefix(trimmed, "**") && strings.HasSuffix(trimmed, "**")
}

// planDocumentedTerms extracts the significant lower-case terms and proper
// names from one plan claim. ClaimTerms intentionally keeps only proper names
// and metrics; a documentation claim such as "local SQLite storage" needs the
// lower-case subject too.
func planDocumentedTerms(claim string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(term string) {
		term = strings.ToLower(strings.TrimSpace(term))
		if len(term) < 4 || genericClaimTerm(term) || seen[term] {
			return
		}
		seen[term] = true
		out = append(out, term)
	}
	for _, term := range ClaimTerms(claim) {
		add(term)
	}
	for _, raw := range strings.Fields(claim) {
		add(strings.Trim(raw, " \t\r\n.,;:!?()[]{}<>\"'`*_"))
	}
	return out
}

// planDocumentedTermIn finds the accepted documented term in a clause. It
// uses a word boundary so "storage" does not match "storefront" accidentally.
func planDocumentedTermIn(clause string, documented map[string]string) (term, claim string) {
	lower := strings.ToLower(clause)
	candidates := make([]string, 0, len(documented))
	for candidate := range documented {
		candidates = append(candidates, candidate)
	}
	// Prefer the most specific term so the repair names "sqlite" rather than
	// the weaker "local" when a clause contains both.
	sort.Slice(candidates, func(i, j int) bool {
		if len(candidates[i]) != len(candidates[j]) {
			return len(candidates[i]) > len(candidates[j])
		}
		return candidates[i] < candidates[j]
	})
	for _, candidate := range candidates {
		accepted := documented[candidate]
		if len(candidate) == 0 {
			continue
		}
		index := strings.Index(lower, candidate)
		for index >= 0 {
			before := index == 0 || !isTermRune(rune(lower[index-1]))
			after := index+len(candidate) >= len(lower) || !isTermRune(rune(lower[index+len(candidate)]))
			if before && after {
				return candidate, accepted
			}
			next := strings.Index(lower[index+1:], candidate)
			if next < 0 {
				break
			}
			index += next + 1
		}
	}
	return "", ""
}

// isTermRune reports whether r can continue a word. It is the boundary rule
// for documentation terms, kept local to the plan audit so it cannot disagree
// with the tokenizer used to build them.
func isTermRune(r rune) bool {
	return r == '_' || r == '-' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
}

var planClauseStrengthPattern = regexp.MustCompile(`(?i)\b(?:` +
	`everything|all|every|always|never|only|none|entirely|completely|solely|exclusively|` +
	`stor(?:e|es|ed|ing)|persist(?:s|ed|ing)?|uses?|used|using|consists?\s+of|` +
	`requires?|depends?\s+on|provides?|supports?` +
	`)\b`)

// planClauseStrengthensClaim reports a clause whose wording asserts a
// documented subject as a world fact rather than reporting the source that
// documents it. A negative clause is included: "there is no hosted service"
// strengthens an accepted "the README says there is no hosted service" unless
// the final clause keeps that attribution.
func planClauseStrengthensClaim(clause string) bool {
	return planClauseStrengthPattern.MatchString(clause) || len(negativeClaims(clause)) > 0
}

// AnswerPlanRefusal renders the bounded refusal the model sees when a plan
// cannot be accepted. It names the missing observations and the permitted
// repair actions, never a scheduler counter.
func AnswerPlanRefusal(issues []contract.GroundingIssue) string {
	repairs := usableRepairs(issues)
	if len(repairs) == 0 {
		return "answer plan refused: the runtime could not accept the declared claims. Gather the missing evidence or move an uncertain claim to limitations."
	}
	return "answer plan refused; the final answer may not assert these claims yet:\n" +
		RepairInstructions(repairs) +
		"\nGather each missing observation with the available tools, or move the claim to limitations with the honest gap. Do not write the final answer until the plan is accepted."
}

// dedupeGroundingIssues keeps one issue per code, claim, and detail. The
// grounding, provenance, and verification audits can name the same sentence;
// the plan refusal should not repeat it.
func dedupeGroundingIssues(issues []contract.GroundingIssue) []contract.GroundingIssue {
	out := make([]contract.GroundingIssue, 0, len(issues))
	seen := map[string]bool{}
	for _, issue := range issues {
		key := issue.Code + "\x00" + issue.Claim + "\x00" + issue.Detail
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, issue)
	}
	return out
}
