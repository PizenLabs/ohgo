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
