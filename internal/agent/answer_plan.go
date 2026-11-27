package agent

import (
	"fmt"
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
