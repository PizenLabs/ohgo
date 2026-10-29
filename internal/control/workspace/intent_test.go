package workspace

import "testing"

func TestClassifyReadsCapabilityNotGrammar(t *testing.T) {
	// The classifier must key on the requested capability, not punctuation or
	// sentence form (§6): a question can carry execution intent and an
	// imperative can be informational.
	cases := []struct {
		text string
		want Intent
	}{
		{"Explain what this function does.", IntentRead},
		{"what is Golang?", IntentRead},
		{"Why does this function use sync.Map?", IntentRead},
		{"How does the parser work?", IntentRead},
		{"summarize the diff", IntentRead},
		{"compare these two approaches", IntentRead},
		{"what changed?", IntentRead},
		{"Why did this edge case fail?", IntentRead},
		{"describe the architecture", IntentRead},

		{"Fix this bug.", IntentExecute},
		{"implement the fix", IntentExecute},
		{"Now fix it.", IntentExecute},
		{"Can you run the tests and tell me if this is safe?", IntentExecute},
		{"refactor the module", IntentExecute},
		{"delete the dead code", IntentExecute},
		{"Apply the patch.", IntentExecute},

		{"Try to break this parser.", IntentInvestigate},
		{"reproduce the panic", IntentInvestigate},
		{"stress test the scheduler", IntentInvestigate},
		{"probe the edge cases", IntentInvestigate},
		{"benchmark the hot path", IntentInvestigate},
		{"see if it crashes under load", IntentInvestigate},
	}
	for _, tc := range cases {
		if got := Classify(tc.text); got != tc.want {
			t.Errorf("Classify(%q) = %s, want %s", tc.text, got, tc.want)
		}
	}
}

func TestClassifyFallsBackToTheLowerAuthority(t *testing.T) {
	// Ambiguity may reduce authority, never increase it (§17). Every example
	// the spec calls ambiguous must classify as READ, the lowest authority.
	for _, text := range []string{
		"Take a look at this.",
		"Can you handle this?",
		"Check this.",
		"Make this better.",
		"",
		"hi",
		"...",
	} {
		if got := Classify(text); got != IntentRead {
			t.Errorf("Classify(%q) = %s, want %s", text, got, IntentRead)
		}
	}
}

func TestClassifyPrefersInvestigationOverExecution(t *testing.T) {
	// A request that asks to both probe and change is safer served by FUZZ:
	// the lower authority wins when the signals are mixed.
	if got := Classify("fix it and then try to break it again"); got != IntentInvestigate {
		t.Errorf("mixed intent = %s, want %s", got, IntentInvestigate)
	}
}

// A read opener must not hide a later mutation clause joined by a sequencing
// word. Without this, "inspect X and then add Y" runs as a read-only turn and
// the requested change is silently never attempted.
func TestClassifyKeepsMutationAfterSequencedReadClause(t *testing.T) {
	for _, text := range []string{
		"Inspect the server and then add a timeout.",
		"Inspect the server, and then add a timeout.",
	} {
		if got := Classify(text); got != IntentExecute {
			t.Errorf("Classify(%q) = %s, want %s", text, got, IntentExecute)
		}
	}
}

// The mutation vocabulary is finite and deliberate. These are the forms the
// contract promises to recognise; a regression here means a clear change
// request is silently routed to READ.
func TestClassifyCoversMutationVocabulary(t *testing.T) {
	for _, text := range []string{
		"Fix the parser.",
		"Add a timeout.",
		"Change the retry count.",
		"Update the README.",
		"Remove the dead branch.",
		"Delete the stale file.",
		"Write the migration.",
		"Create a new handler.",
		"Implement the cache.",
		"Refactor the loop.",
		"Rename the field.",
		"Clean up the imports.",
		"Tidy up the imports.",
		"Make sure the server times out after 5s.",
		"Ensure the server times out after 5s.",
		"Organize the package.",
	} {
		if got := Classify(text); got != IntentExecute {
			t.Errorf("Classify(%q) = %s, want %s", text, got, IntentExecute)
		}
	}
}

// A mutation clause after a read opener is still the objective, including when
// the clause's mutation signal is a phrase rather than a leading verb.
func TestClassifyPreservesCompoundMutationVocabulary(t *testing.T) {
	for _, text := range []string{
		"Inspect the server and then change the timeout.",
		"Check the parser, then update the tests.",
		"Review the module and fix the leak.",
		"Inspect the server and make sure it times out.",
		"Check the imports, then tidy them up.",
		"Review the package, and ensure it compiles.",
	} {
		if got := Classify(text); got != IntentExecute {
			t.Errorf("Classify(%q) = %s, want %s", text, got, IntentExecute)
		}
	}
}

// Later actionable clauses outrank read-oriented openers. This is the
// externally visible distinction between asking about execution and asking
// ohgo to perform it.
func TestClassifyUsesTheWholeRequestForMutationIntent(t *testing.T) {
	for _, text := range []string{
		"create a portfolio website",
		"check this project and create the missing files",
		"inspect the project, then fix the failing tests",
		"check this project and git commit the changes",
		"check this project and also create a website",
		"explain the bug and fix it",
	} {
		if got := Classify(text); got != IntentExecute {
			t.Errorf("Classify(%q) = %s, want EXECUTE", text, got)
		}
		if contract := BuildIntentContract(text); contract.Kind != ObjectiveMutation {
			t.Errorf("BuildIntentContract(%q).Kind = %s, want mutation", text, contract.Kind)
		}
		route := RouteTurn(Build, text)
		if !route.Allowed() || route.Active != Build {
			t.Errorf("BUILD route for %q = %+v, want execution under BUILD", text, route)
		}
	}
	question := "tell me how to git commit these changes"
	if got := Classify(question); got != IntentRead {
		t.Errorf("informational git request = %s, want READ", got)
	}
	if contract := BuildIntentContract(question); contract.Kind != ObjectiveInformational {
		t.Errorf("BuildIntentContract(%q).Kind = %s, want informational", question, contract.Kind)
	}
}

func TestAmbiguousReadVerbRequiresAWorkspaceChoice(t *testing.T) {
	prediction := ClassifyPrediction("check this project")
	if prediction.Intent != IntentRead || prediction.Strength != StrengthModerate {
		t.Fatalf("prediction = %+v, want a moderate READ signal", prediction)
	}
	route := RouteTurn(Build, "check this project")
	if route.Allowed() || route.Required != Ask {
		t.Errorf("route = %+v, want control-plane choice before ASK", route)
	}
}

func TestGreetingDoesNotRequireAnAuthorityTransition(t *testing.T) {
	for _, active := range []Workspace{Ask, Fuzz, Build} {
		for _, text := range []string{"hi", "hello!", "good morning"} {
			route := RouteTurn(active, text)
			if !route.Allowed() || route.Active != active {
				t.Errorf("RouteTurn(%s, %q) = %+v, want a greeting in the selected workspace", active, text, route)
			}
		}
	}
}

// A question that merely mentions a mutation phrase stays informational. The
// contract must not invent a change request from explanatory language.
func TestClassifyDoesNotPromoteInformationalMutationPhrases(t *testing.T) {
	for _, text := range []string{
		"Explain how to make sure the server times out.",
		"What does tidy up mean here?",
		"How do I fix this?",
		"Why does clean up fail?",
		"Show me how to update the config.",
	} {
		if got := Classify(text); got != IntentRead {
			t.Errorf("Classify(%q) = %s, want %s", text, got, IntentRead)
		}
	}
}

// RequestsMutation is the completion contract, so it is narrower than routing:
// running tests is execution, not a repository change.
func TestRequestsMutationIsFiniteAndClauseAware(t *testing.T) {
	yes := []string{
		"Fix the parser.",
		"Tidy up the imports.",
		"Make sure the server times out.",
		"Ensure the server times out.",
		"Inspect the server and then add a timeout.",
		"Check the imports, then tidy them up.",
		"Review the module and fix the leak.",
		"Can you clean up the dead code?",
	}
	for _, text := range yes {
		if !RequestsMutation(text) {
			t.Errorf("RequestsMutation(%q) = false, want true", text)
		}
	}

	no := []string{
		"Run the tests.",
		"Explain how to make sure the server times out.",
		"What does tidy up mean here?",
		"How do I fix this?",
		"Try to break the parser.",
		"Summarize the change.",
		"",
	}
	for _, text := range no {
		if RequestsMutation(text) {
			t.Errorf("RequestsMutation(%q) = true, want false", text)
		}
	}
}

func TestIntentHandlerMapsToAuthority(t *testing.T) {
	cases := map[Intent]Workspace{
		IntentRead:        Ask,
		IntentInvestigate: Fuzz,
		IntentExecute:     Build,
	}
	for intent, want := range cases {
		if got := intent.Handler(); got != want {
			t.Errorf("%s.Handler() = %s, want %s", intent, got, want)
		}
	}
}

func TestRouteTurnTransitionMatrix(t *testing.T) {
	// The transition table is the authority contract (§8). Each row is a
	// matrix cell; the wrong cell is an implicit escalation.
	cases := []struct {
		name     string
		active   Workspace
		text     string
		allowed  bool
		required Workspace
	}{
		// ASK is execution-closed.
		{"ASK+READ", Ask, "explain this", true, ""},
		{"ASK+EXECUTE", Ask, "fix the bug", false, Build},
		{"ASK+INVESTIGATE", Ask, "try to break this", false, Fuzz},

		// BUILD must explicitly transition for informational or investigative
		// work; a request never narrows its authority behind the UI.
		{"BUILD+READ", Build, "why does this work?", false, Ask},
		{"BUILD+EXECUTE", Build, "implement the fix", true, ""},
		{"BUILD+INVESTIGATE", Build, "reproduce the failure", false, Fuzz},

		// FUZZ remains the selected no-mutation investigation workspace.
		{"FUZZ+READ", Fuzz, "explain this", false, Ask},
		{"FUZZ+INVESTIGATE", Fuzz, "try to break this", true, ""},
		{"FUZZ+EXECUTE", Fuzz, "fix the bug", false, Build},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			route := RouteTurn(tc.active, tc.text)
			if route.Allowed() != tc.allowed {
				t.Errorf("allowed = %v, want %v", route.Allowed(), tc.allowed)
			}
			if route.Required != tc.required {
				t.Errorf("required = %s, want %s", route.Required, tc.required)
			}
		})
	}
}

func TestRouteTurnNeverChangesTheActiveWorkspace(t *testing.T) {
	// Classification and refusal never move the workspace; only an explicit
	// switch can change the active authority.
	for _, active := range []Workspace{Ask, Fuzz, Build} {
		for _, text := range []string{"why does this work?", "fix it", "try to break it"} {
			route := RouteTurn(active, text)
			if route.Active != active {
				t.Errorf("RouteTurn(%s, %q).Active = %s, want %s", active, text, route.Active, active)
			}
		}
	}
}

func TestRouteTurnDeniesAnUnresolvedWorkspace(t *testing.T) {
	// A workspace that was never resolved must be denied, not defaulted to one
	// of the three authorities.
	route := RouteTurn(Workspace(""), "explain this")
	if route.Allowed() {
		t.Fatal("an unresolved workspace admitted a request")
	}
	if route.Message() == "" {
		t.Error("the refusal has no user-facing message")
	}
}

// A comma is a clause boundary. Without preserving it, "check the project,
// write README.md" collapsed into one read-headed clause and the mutation was
// invisible to the contract.
func TestCommaSeparatedMutationClauseIsVisible(t *testing.T) {
	text := "Check the project, write README.md."
	if !RequestsMutation(text) {
		t.Fatal("comma-separated mutation clause was not recognized")
	}
	if intent := BuildIntentContract(text); intent.Kind != ObjectiveMutation {
		t.Fatalf("contract kind = %s, want mutation", intent.Kind)
	}
}

func TestRouteKeepsSelectedWorkspaceAndRefusesOnlyEscalation(t *testing.T) {
	for _, active := range []Workspace{Ask, Fuzz, Build} {
		for _, text := range []string{"why does this work?", "reproduce the failure", "implement the fix"} {
			route := RouteTurn(active, text)
			if route.Active != active {
				t.Errorf("RouteTurn(%s, %q) changed authority: %+v", active, text, route)
			}
		}
	}
	refused := RouteTurn(Fuzz, "fix it")
	if refused.Allowed() || refused.Required != Build || refused.Message() == "" {
		t.Errorf("FUZZ mutation was not refused with a BUILD boundary: %+v", refused)
	}
}
