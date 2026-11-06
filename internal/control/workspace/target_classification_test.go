package workspace

import (
	"reflect"
	"testing"

	"github.com/PizenLabs/ohgo/internal/control/contract"
)

// A slash token is a filesystem coordinate only when the request frames it as
// one. A conceptual compound must not acquire target_state evidence.
func TestBuildIntentContractDistinguishesPathTargetsFromRepositoryConcepts(t *testing.T) {
	cases := []struct {
		text       string
		wantPaths  []string
		wantTopics []string
	}{
		{
			text:      "Read order/checkout.go",
			wantPaths: []string{"order/checkout.go"},
		},
		{
			text:      "Inspect the order/checkout directory",
			wantPaths: []string{"order/checkout"},
		},
		{
			text:      "Open order/checkout",
			wantPaths: []string{"order/checkout"},
		},
		{
			text:      "Read the file order/checkout",
			wantPaths: []string{"order/checkout"},
		},
		{
			text:      "Implement the fix in order/checkout",
			wantPaths: []string{"order/checkout"},
		},
		{
			text:       "Check this Go e-commerce project and understand how the main order/checkout flow works.",
			wantTopics: []string{"order/checkout flow"},
		},
		{
			text:       "Explain how the checkout flow works.",
			wantTopics: []string{"checkout flow"},
		},
		{
			text:       "Understand how the main checkout flow works.",
			wantTopics: []string{"checkout flow"},
		},
		{
			text:       "Explain this project's checkout flow.",
			wantTopics: []string{"checkout flow"},
		},
		{
			text: "What is a cron expression?",
		},
		{
			text: "Explain how a checkout flow works.",
		},
		{
			text: "How do checkout flows work?",
		},
		{
			text: "Explain how TCP/IP works",
		},
		{
			text:      "Read order/checkout.go and explain the checkout flow.",
			wantPaths: []string{"order/checkout.go"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.text, func(t *testing.T) {
			intent := BuildIntentContract(tc.text)
			var paths []string
			for _, target := range intent.Targets {
				if target.Kind == TargetPath {
					paths = append(paths, target.Value)
				}
			}
			if !reflect.DeepEqual(paths, tc.wantPaths) {
				t.Fatalf("path targets = %v, want %v (targets=%+v)", paths, tc.wantPaths, intent.Targets)
			}

			var topics []string
			if intent.Grounding != nil {
				for _, entity := range intent.Grounding.Entities {
					if entity.Kind == contract.EntityTopic {
						topics = append(topics, entity.Value)
					}
				}
			}
			if !reflect.DeepEqual(topics, tc.wantTopics) {
				t.Fatalf("topics = %v, want %v (entities=%+v)", topics, tc.wantTopics, intent.Grounding.Entities)
			}
		})
	}
}

// A named concept plus an explicit check bounds a mutation branch without
// inventing a path. A concept alone, or a generic "update the code", remains
// ambiguous because there is no oracle for completion.
func TestConceptWithNamedCheckBoundsAMutationBranch(t *testing.T) {
	staged := BuildIntentContract(stagedWorkflowPrompt)
	if len(staged.Targets) != 0 {
		t.Fatalf("staged concept acquired literal targets: %+v", staged.Targets)
	}
	if !staged.BehavioralObjective || staged.Ambiguous {
		t.Fatalf("staged concept contract = behavioral %v ambiguous %v, want a checked behavioural branch",
			staged.BehavioralObjective, staged.Ambiguous)
	}
	if !reflect.DeepEqual(staged.Verification, []VerificationRequirement{{
		Kind: VerifyScope, Scope: "*", Check: CheckTest,
	}}) {
		t.Fatalf("verification = %+v, want the named test oracle", staged.Verification)
	}

	conceptOnly := BuildIntentContract("Fix the checkout flow.")
	if !conceptOnly.Ambiguous || conceptOnly.BehavioralObjective {
		t.Fatalf("concept without a check = ambiguous %v behavioral %v, want review",
			conceptOnly.Ambiguous, conceptOnly.BehavioralObjective)
	}

	generic := BuildIntentContract("Update the code to be more robust and run go test ./...")
	if !generic.Ambiguous {
		t.Fatal("a generic code change with a check became behavioural")
	}
}

// A topic is repository context, not a named entity scope. Treating it as
// ScopeNamed would make an answer about this repository look like a claim about
// an external entity set.
func TestConceptTopicDoesNotBecomeANamedGroundingScope(t *testing.T) {
	intent := BuildIntentContract("Explain how the checkout flow works.")
	if intent.Grounding == nil {
		t.Fatal("no grounding")
	}
	if intent.Grounding.Scope.Kind == contract.ScopeNamed {
		t.Fatalf("scope = %+v, want the topic to stay repository context", intent.Grounding.Scope)
	}
}
