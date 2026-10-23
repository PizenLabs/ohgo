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
		handler  Workspace
		allowed  bool
		required Workspace
	}{
		// ASK is execution-closed.
		{"ASK+READ", Ask, "explain this", Ask, true, ""},
		{"ASK+EXECUTE", Ask, "fix the bug", Ask, false, Build},
		{"ASK+INVESTIGATE", Ask, "try to break this", Ask, false, Fuzz},

		// BUILD delegates informational turns and keeps its authority.
		{"BUILD+READ", Build, "why does this work?", Ask, true, ""},
		{"BUILD+EXECUTE", Build, "implement the fix", Build, true, ""},
		{"BUILD+INVESTIGATE", Build, "reproduce the failure", Fuzz, true, ""},

		// FUZZ delegates informational turns and never becomes BUILD.
		{"FUZZ+READ", Fuzz, "explain this", Ask, true, ""},
		{"FUZZ+INVESTIGATE", Fuzz, "try to break this", Fuzz, true, ""},
		{"FUZZ+EXECUTE", Fuzz, "fix the bug", Fuzz, false, Build},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			route := RouteTurn(tc.active, tc.text)
			if route.Handler != tc.handler {
				t.Errorf("handler = %s, want %s", route.Handler, tc.handler)
			}
			if route.Allowed() != tc.allowed {
				t.Errorf("allowed = %v, want %v", route.Allowed(), tc.allowed)
			}
			if route.Required != tc.required {
				t.Errorf("required = %s, want %s", route.Required, tc.required)
			}
			// Delegation may only narrow authority, never widen it.
			if route.Allowed() && !route.Handler.Permits(OpRead) && route.Active.Permits(OpRead) {
				t.Errorf("handler %s is not a valid workspace", route.Handler)
			}
		})
	}
}

func TestRouteTurnNeverChangesTheActiveWorkspace(t *testing.T) {
	// Invariant G: a temporary handler must not permanently move the active
	// workspace.
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

func TestRouteEffectiveAuthorityNeverExceedsTheWorkspace(t *testing.T) {
	// Effective authority is the workspace constrained by the turn intent.
	// For an allowed turn it is the handler; for a refused turn nothing runs,
	// so it stays the active workspace (I3).
	cases := []struct {
		name   string
		active Workspace
		text   string
		want   Workspace
	}{
		{"ASK+READ", Ask, "explain this", Ask},
		{"ASK+EXECUTE", Ask, "fix the bug", Ask},
		{"ASK+INVESTIGATE", Ask, "try to break this", Ask},
		{"BUILD+READ", Build, "why does this work?", Ask},
		{"BUILD+EXECUTE", Build, "implement the fix", Build},
		{"BUILD+INVESTIGATE", Build, "reproduce the failure", Fuzz},
		{"FUZZ+READ", Fuzz, "explain this", Ask},
		{"FUZZ+INVESTIGATE", Fuzz, "try to break this", Fuzz},
		{"FUZZ+EXECUTE", Fuzz, "fix the bug", Fuzz},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			route := RouteTurn(tc.active, tc.text)
			if got := route.Effective(); got != tc.want {
				t.Errorf("Effective() = %s, want %s", got, tc.want)
			}
			// A route may never grant an authority the active workspace could
			// not itself exercise for the intent.
			if route.Effective().Permits(OpWrite) && !tc.active.Permits(OpWrite) {
				t.Errorf("Effective() = %s escalated mutation above %s", route.Effective(), tc.active)
			}
		})
	}
}

func TestRouteIndicatorNamesWorkspaceThenTurn(t *testing.T) {
	// The indicator keeps the persistent workspace first and the ephemeral
	// handler second, so it can never read as a workspace switch (§12).
	if got := RouteTurn(Build, "why does this work?").Indicator(); got != "BUILD · ASK / READ" {
		t.Errorf("indicator = %q, want %q", got, "BUILD · ASK / READ")
	}
	if got := RouteTurn(Build, "reproduce the failure").Indicator(); got != "BUILD · FUZZ / INVESTIGATE" {
		t.Errorf("indicator = %q, want %q", got, "BUILD · FUZZ / INVESTIGATE")
	}
	// A direct or refused turn adds no indicator: the workspace already names
	// the authority in force.
	if got := RouteTurn(Build, "implement the fix").Indicator(); got != "" {
		t.Errorf("direct turn indicator = %q, want empty", got)
	}
	if got := RouteTurn(Ask, "fix the bug").Indicator(); got != "" {
		t.Errorf("refused turn indicator = %q, want empty", got)
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

func TestRouteDelegatedAndNotice(t *testing.T) {
	// A delegated turn is reported truthfully; an undeligated one adds no
	// noise.
	delegated := RouteTurn(Build, "why does this work?")
	if !delegated.Delegated() {
		t.Fatal("BUILD+READ was not reported as delegated")
	}
	if delegated.Notice() == "" {
		t.Error("a delegated turn has no indicator")
	}
	if direct := RouteTurn(Build, "fix it"); direct.Delegated() || direct.Notice() != "" {
		t.Errorf("BUILD+EXECUTE reported delegation: %+v", direct)
	}
	refused := RouteTurn(Fuzz, "fix it")
	if refused.Delegated() {
		t.Error("a refused route reported delegation")
	}
	if refused.Message() == "" {
		t.Error("a refused route has no message")
	}
}
