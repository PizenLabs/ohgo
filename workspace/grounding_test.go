package workspace

import (
	"testing"

	"github.com/PizenLabs/ohgo/contract"
)

// The grounding contract preserves what the user named. Each case is a
// semantic invariant, not a grammar test: dropping an entity or inventing an
// action is the failure mode.
func TestGroundingPreservesIntentAndEntities(t *testing.T) {
	cases := []struct {
		name        string
		request     string
		action      contract.GroundingAction
		mutation    bool
		entities    []string
		optional    bool
		ambiguity   string
		scope       contract.GroundingScopeKind
		conditional bool
	}{
		{
			name:     "explicit mutation",
			request:  "Fix the authentication bug.",
			action:   contract.GroundingMutate,
			mutation: true,
		},
		{
			name:     "multiple named projects",
			request:  "Check k8s and opencode as additional projects.",
			action:   contract.GroundingRead,
			entities: []string{"k8s", "opencode"},
			scope:    contract.ScopeNamed,
		},
		{
			name:      "entities without an action",
			request:   "Additional projects are k8s and opencode.",
			action:    contract.GroundingUnknown,
			entities:  []string{"k8s", "opencode"},
			ambiguity: "unknown_action",
			scope:     contract.ScopeNamed,
		},
		{
			name:     "compound investigation and mutation",
			request:  "Investigate the API issue and update the documentation.",
			action:   contract.GroundingMutate,
			mutation: true,
		},
		{
			name:      "unresolved scope reference",
			request:   "Check the other two repositories too.",
			action:    contract.GroundingRead,
			ambiguity: "unresolved_reference",
		},
		{
			name:     "investigation then mutation",
			request:  "Find why auth is failing and fix it.",
			action:   contract.GroundingMutate,
			mutation: true,
		},
		{
			name:     "benign semantic expansion",
			request:  "Clean up the login flow.",
			action:   contract.GroundingMutate,
			mutation: true,
		},
		{
			name:        "conditional entities",
			request:     "The additional projects are k8s and opencode; use them if relevant.",
			action:      contract.GroundingUnknown,
			entities:    []string{"k8s", "opencode"},
			optional:    true,
			ambiguity:   "unknown_action",
			scope:       contract.ScopeNamed,
			conditional: true,
		},
		{
			name:    "colon list",
			request: "Additional projects: k8s, opencode",
			action:  contract.GroundingUnknown,
			entities: []string{
				"k8s", "opencode",
			},
			scope: contract.ScopeNamed,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			intent := BuildIntentContract(tc.request)
			if intent.Grounding == nil {
				t.Fatal("no grounding was built")
			}
			g := intent.Grounding
			if g.Action != tc.action {
				t.Fatalf("action = %q, want %q", g.Action, tc.action)
			}
			if g.MutationIntent != tc.mutation {
				t.Fatalf("mutation intent = %v, want %v", g.MutationIntent, tc.mutation)
			}
			if len(tc.entities) > 0 {
				got := g.EntityValues()
				for _, want := range tc.entities {
					found := false
					for _, value := range got {
						if value == want {
							found = true
							break
						}
					}
					if !found {
						t.Fatalf("entities = %v, missing %q", got, want)
					}
				}
			}
			for _, entity := range g.Entities {
				if tc.optional && !entity.Optional {
					t.Fatalf("entity %q is not optional", entity.Value)
				}
			}
			if tc.ambiguity != "" {
				found := false
				for _, ambiguity := range g.Ambiguities {
					if ambiguity.Kind == tc.ambiguity {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("ambiguities = %+v, missing %q", g.Ambiguities, tc.ambiguity)
				}
			}
			if tc.scope != "" && g.Scope.Kind != tc.scope {
				t.Fatalf("scope = %q, want %q", g.Scope.Kind, tc.scope)
			}
			if g.Scope.Conditional != tc.conditional {
				t.Fatalf("conditional = %v, want %v", g.Scope.Conditional, tc.conditional)
			}
		})
	}
}

// A singular description must not become a named entity list. This is the
// over-extraction half of entity preservation.
func TestGroundingDoesNotInventEntitiesFromDescriptions(t *testing.T) {
	for _, request := range []string{
		"The project is a Go service.",
		"This repository has one package.",
		"Explain the parser.",
	} {
		intent := BuildIntentContract(request)
		if got := intent.Grounding.EntityValues(); len(got) != 0 {
			t.Fatalf("%q invented entities %v", request, got)
		}
	}
}

// A compound request keeps every requested action. The primary action is the
// strongest one, but investigation must not disappear from the contract.
func TestGroundingKeepsCompoundActions(t *testing.T) {
	intent := BuildIntentContract("Investigate the API issue and update the documentation.")
	g := intent.Grounding
	if !g.HasAction(contract.GroundingInvestigate) {
		t.Fatalf("actions = %v, missing investigation", g.Actions)
	}
	if !g.HasAction(contract.GroundingMutate) {
		t.Fatalf("actions = %v, missing mutation", g.Actions)
	}
}

// A negated clause is a constraint, not an action. "Do not create it" must
// not make the grounding say the user asked for a mutation.
func TestGroundingDoesNotTurnAConstraintIntoAnAction(t *testing.T) {
	intent := BuildIntentContract("Check this project and tell me whether a README.md should be created. Do not create it.")
	if intent.Grounding.Action == contract.GroundingMutate {
		t.Fatalf("negated clause became action %q", intent.Grounding.Action)
	}
	found := false
	for _, constraint := range intent.Grounding.Constraints {
		if constraint == "no_creation" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("constraints = %v, missing no_creation", intent.Grounding.Constraints)
	}
}

// An actionless request must remain actionless. This is the invariant the
// observed failure violated when it became "inspect the README".
func TestGroundingDoesNotInventAnAction(t *testing.T) {
	intent := BuildIntentContract("Additional projects are k8s and opencode.")
	if intent.Grounding.Action != contract.GroundingUnknown {
		t.Fatalf("action = %q, want unknown", intent.Grounding.Action)
	}
	if !intent.Grounding.HasMaterialAmbiguity() {
		t.Fatal("an actionless request with named entities is not material ambiguity")
	}
}

// An unresolved alternative must not be chosen by the model on the user's
// behalf; completion is reviewable until the user decides.
func TestGroundingPreservesUnresolvedAlternative(t *testing.T) {
	intent := BuildIntentContract("Implement config loading by extending the parser vs writing a new reader.")
	found := false
	for _, ambiguity := range intent.Grounding.Ambiguities {
		if ambiguity.Kind == "unresolved_alternative" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("ambiguities = %+v, missing unresolved_alternative", intent.Grounding.Ambiguities)
	}
}
