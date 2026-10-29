package workspace

import "testing"

// TestClassifyPredictionReportsStrength pins the evidence model: a leading
// verb is strong, a corroborating phrase is moderate, and the READ fallback is
// ambiguous rather than a reading of the request (§7).
func TestClassifyPredictionReportsStrength(t *testing.T) {
	cases := []struct {
		name       string
		text       string
		wantIntent Intent
		want       Strength
	}{
		{"leading question word", "Why does this function return nil here?", IntentRead, StrengthStrong},
		{"leading investigate verb", "reproduce the panic", IntentInvestigate, StrengthStrong},
		{"leading execute verb", "implement the fix", IntentExecute, StrengthStrong},
		{"phrase-only investigation", "find edge cases", IntentInvestigate, StrengthModerate},
		{"no signal at all", "Take a look at this.", IntentRead, StrengthAmbiguous},
		{"empty request", "", IntentRead, StrengthAmbiguous},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyPrediction(tc.text)
			if got.Intent != tc.wantIntent {
				t.Errorf("Intent = %s, want %s", got.Intent, tc.wantIntent)
			}
			if got.Strength != tc.want {
				t.Errorf("Strength = %s, want %s", got.Strength, tc.want)
			}
		})
	}
}

// TestClassifyPredictionMarksContestedPredictionsAmbiguous is the ambiguity
// contract: a stronger rival signal makes the winner unsafe to act on, even
// though the intent itself is unchanged.
func TestClassifyPredictionMarksContestedPredictionsAmbiguous(t *testing.T) {
	// The winner is INVESTIGATE by phrase, but EXECUTE matched by leading
	// verb — stronger evidence for the losing intent.
	got := ClassifyPrediction("fix it and then try to break it again")
	if got.Intent != IntentInvestigate {
		t.Fatalf("Intent = %s, want INVESTIGATE (the intent contract must not move)", got.Intent)
	}
	if got.Strength != StrengthAmbiguous {
		t.Errorf("Strength = %s, want AMBIGUOUS for a contested prediction", got.Strength)
	}
	if got.Competing != IntentExecute {
		t.Errorf("Competing = %q, want EXECUTE", got.Competing)
	}
}

// TestClassifyIsUnchangedByPrediction proves the richer classifier preserves
// the original enum contract.
func TestClassifyIsUnchangedByPrediction(t *testing.T) {
	for _, text := range []string{
		"explain this", "why does this work?", "fix the bug",
		"reproduce the panic", "try to break this", "Take a look",
		"", "hi",
	} {
		if got, want := Classify(text), ClassifyPrediction(text).Intent; got != want {
			t.Errorf("Classify(%q) = %s, ClassifyPrediction().Intent = %s", text, got, want)
		}
	}
}

// TestStrengthAtLeastPinsTheOrdering keeps the threshold testable: a policy
// that cannot order its levels cannot state an explicit floor (§7).
func TestStrengthAtLeastPinsTheOrdering(t *testing.T) {
	cases := []struct {
		s     Strength
		floor Strength
		want  bool
	}{
		{StrengthStrong, StrengthStrong, true},
		{StrengthStrong, StrengthModerate, true},
		{StrengthStrong, StrengthAmbiguous, true},
		{StrengthModerate, StrengthStrong, false},
		{StrengthAmbiguous, StrengthModerate, false},
		{Strength("invented"), StrengthAmbiguous, true},
		{Strength("invented"), StrengthStrong, false},
	}
	for _, tc := range cases {
		if got := tc.s.AtLeast(tc.floor); got != tc.want {
			t.Errorf("%s.AtLeast(%s) = %v, want %v", tc.s, tc.floor, got, tc.want)
		}
	}
}

// TestProposeTransitionMatrix is the policy table from §6, cell by cell.
func TestProposeTransitionMatrix(t *testing.T) {
	cases := []struct {
		name     string
		active   Workspace
		text     string
		proposed Workspace
		want     bool
	}{
		{"ASK+READ", Ask, "explain this", "", false},
		{"ASK+INVESTIGATE", Ask, "try to break this", "", false},
		{"ASK+EXECUTE", Ask, "fix the bug", "", false},

		{"BUILD+READ", Build, "why does this work?", Ask, true},
		{"BUILD+INVESTIGATE", Build, "reproduce the failure", Fuzz, true},
		{"BUILD+EXECUTE", Build, "implement the fix", "", false},

		{"FUZZ+READ", Fuzz, "explain this", Ask, true},
		{"FUZZ+INVESTIGATE", Fuzz, "try to break this", "", false},
		{"FUZZ+EXECUTE", Fuzz, "fix the bug", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			proposal, ok := ProposeTransition(tc.active, ClassifyPrediction(tc.text))
			if ok != tc.want {
				t.Fatalf("ProposeTransition(%s, %q) = %v, want %v", tc.active, tc.text, ok, tc.want)
			}
			if !ok {
				return
			}
			if proposal.Current != tc.active {
				t.Errorf("Current = %s, want %s", proposal.Current, tc.active)
			}
			if proposal.Proposed != tc.proposed {
				t.Errorf("Proposed = %s, want %s", proposal.Proposed, tc.proposed)
			}
			if proposal.Reason == "" {
				t.Error("a proposal has no user-facing reason")
			}
			if proposal.Question() == "" {
				t.Error("a proposal has no question")
			}
		})
	}
}

// TestProposeTransitionRefusesWeakAndAmbiguousEvidence covers the confidence
// half of the policy: real but weak evidence, and no evidence at all, propose
// nothing (§7, §16).
func TestProposeTransitionRefusesWeakAndAmbiguousEvidence(t *testing.T) {
	cases := []struct {
		name   string
		text   string
		active Workspace
	}{
		{"fallback READ", "Take a look at this.", Build},
		{"contested investigate", "fix it and then try to break it again", Build},
		{"contested investigate in FUZZ", "fix it and then try to break it again", Fuzz},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prediction := ClassifyPrediction(tc.text)
			if prediction.Strength == StrengthStrong {
				t.Fatalf("test premise broken: %q classified as STRONG", tc.text)
			}
			if _, ok := ProposeTransition(tc.active, prediction); ok {
				t.Errorf("ProposeTransition(%s, %q) proposed on %s evidence", tc.active, tc.text, prediction.Strength)
			}
		})
	}
}

// TestProposalThresholdRequiresStrongEvidence pins the policy floor: a real
// but phrase-level (moderate) match is not enough to interrupt a turn. This is
// the mutation guard for proposalThreshold — raising the floor's tolerance to
// moderate must fail here.
func TestProposalThresholdRequiresStrongEvidence(t *testing.T) {
	prediction := ClassifyPrediction("find edge cases")
	if prediction.Intent != IntentInvestigate || prediction.Strength != StrengthModerate {
		t.Fatalf("test premise broken: %q = %s/%s", "find edge cases", prediction.Intent, prediction.Strength)
	}
	if _, ok := ProposeTransition(Build, prediction); ok {
		t.Error("moderate evidence proposed a transition; the threshold is strong")
	}
	// The same intent, carried by a leading verb, does propose — so the test
	// distinguishes the floor from the table, not from a missing row.
	strong := ClassifyPrediction("reproduce the failure")
	if _, ok := ProposeTransition(Build, strong); !ok {
		t.Error("strong investigative evidence in BUILD proposed nothing")
	}
}

// TestProposeNeverEscalates is the negative authority contract (I8, I9): no
// classifier result may propose moving to a workspace the active one does not
// encompass, even if the table were wrong.
func TestProposeNeverEscalates(t *testing.T) {
	// Every intent, against every workspace, over strong inputs.
	strong := map[Intent]string{
		IntentRead:        "why does this work?",
		IntentInvestigate: "reproduce the failure",
		IntentExecute:     "implement the fix",
	}
	for _, active := range []Workspace{Ask, Fuzz, Build} {
		for intent, text := range strong {
			prediction := ClassifyPrediction(text)
			if prediction.Intent != intent || prediction.Strength != StrengthStrong {
				continue
			}
			proposal, ok := ProposeTransition(active, prediction)
			if !ok {
				continue
			}
			if !active.Encompasses(proposal.Proposed) {
				t.Errorf("%s proposed %s, which it does not encompass", active, proposal.Proposed)
			}
			if authorityRank(proposal.Proposed) > authorityRank(active) {
				t.Errorf("%s proposed a stronger workspace %s", active, proposal.Proposed)
			}
		}
	}
}

// TestProposeTransitionRefusesAnUnresolvedWorkspace keeps the zero value
// denied, matching the rest of the package.
func TestProposeTransitionRefusesAnUnresolvedWorkspace(t *testing.T) {
	if _, ok := ProposeTransition(Workspace(""), ClassifyPrediction("why does this work?")); ok {
		t.Fatal("an unresolved workspace produced a proposal")
	}
}

// TestEncompassesIsAStrictChain pins the ordering the policy relies on.
func TestEncompassesIsAStrictChain(t *testing.T) {
	cases := []struct {
		from, to Workspace
		want     bool
	}{
		{Build, Build, true}, {Build, Fuzz, true}, {Build, Ask, true},
		{Fuzz, Fuzz, true}, {Fuzz, Ask, true}, {Fuzz, Build, false},
		{Ask, Ask, true}, {Ask, Fuzz, false}, {Ask, Build, false},
		{Workspace(""), Build, false}, {Build, Workspace(""), false},
	}
	for _, tc := range cases {
		if got := tc.from.Encompasses(tc.to); got != tc.want {
			t.Errorf("%q.Encompasses(%q) = %v, want %v", tc.from, tc.to, got, tc.want)
		}
	}
}

// TestTransitionProposalNeverCarriesAProbability guards the truthfulness
// contract (I12): the only confidence the type exposes is a level, so no
// surface can render a fabricated percentage.
func TestTransitionProposalNeverCarriesAProbability(t *testing.T) {
	proposal, ok := ProposeTransition(Build, ClassifyPrediction("why does this work?"))
	if !ok {
		t.Fatal("expected a proposal")
	}
	if proposal.Strength != StrengthStrong {
		t.Errorf("Strength = %s, want STRONG", proposal.Strength)
	}
	for _, s := range []Strength{StrengthAmbiguous, StrengthModerate, StrengthStrong} {
		if want := map[Strength]string{
			StrengthAmbiguous: "ambiguous",
			StrengthModerate:  "moderate",
			StrengthStrong:    "strong",
		}[s]; string(s) != want {
			t.Errorf("Strength %v wires to %q, want the word %q", s, string(s), want)
		}
	}
}
