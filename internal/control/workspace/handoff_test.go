package workspace

import "testing"

// A widening request is proposed only with strong evidence, and only for a
// strictly stronger handler. The proposal never moves authority.
func TestProposeHandoffOnlyWidensWithStrongEvidence(t *testing.T) {
	cases := []struct {
		name   string
		active Workspace
		text   string
		want   Workspace
		ok     bool
	}{
		{"fuzz to build", Fuzz, "implement the fix", Build, true},
		{"ask to build", Ask, "fix the parser", Build, true},
		{"ask to fuzz", Ask, "reproduce the failure", Fuzz, true},
		{"build narrows", Build, "explain the failure", "", false},
		{"fuzz reads", Fuzz, "explain the failure", "", false},
		{"build investigates", Build, "reproduce the failure", "", false},
		{"acknowledgement", Fuzz, "thanks", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prediction := ClassifyPrediction(tc.text)
			proposal, ok := ProposeHandoff(tc.active, prediction)
			if ok != tc.ok {
				t.Fatalf("ProposeHandoff(%s, %q) ok = %v, want %v (prediction %+v)",
					tc.active, tc.text, ok, tc.ok, prediction)
			}
			if ok && proposal.Proposed != tc.want {
				t.Fatalf("proposed = %s, want %s", proposal.Proposed, tc.want)
			}
			if ok && proposal.Current != tc.active {
				t.Fatalf("current = %s, want %s", proposal.Current, tc.active)
			}
			// The policy may ask, never grant: the active workspace's own
			// admission is unchanged by proposing.
			if tc.active == Fuzz {
				if err := Admit(Fuzz, OpWrite); err == nil {
					t.Fatal("a handoff proposal granted FUZZ mutation authority")
				}
			}
			if tc.active == Ask {
				if err := Admit(Ask, OpExec); err == nil {
					t.Fatal("a handoff proposal granted ASK execution authority")
				}
			}
		})
	}
}

// A contest or a weak phrase cannot manufacture a widening prompt.
func TestProposeHandoffRejectsWeakEvidence(t *testing.T) {
	for _, text := range []string{"maybe do something", "what happens if it runs?", "look into it"} {
		if proposal, ok := ProposeHandoff(Ask, ClassifyPrediction(text)); ok {
			t.Fatalf("weak request %q proposed %s -> %s", text, proposal.Current, proposal.Proposed)
		}
	}
}
