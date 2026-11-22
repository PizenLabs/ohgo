package contract

import "testing"

func TestConclusionModeSeparatesAnswersFromDefectVerdicts(t *testing.T) {
	answer := ConclusionModeFor(ObjectiveInformational, "Investigate this project and analyze what it is about")
	if answer != ConclusionAnswer {
		t.Fatalf("plain analysis mode = %q, want %q", answer, ConclusionAnswer)
	}
	if answer.Accepts(VerdictSafe) || answer.Accepts(VerdictBug) {
		t.Fatal("a general analysis admitted a defect verdict")
	}
	if !answer.Accepts(VerdictAnswered) || !answer.Accepts(VerdictPartial) {
		t.Fatal("a general analysis rejected its own outcome vocabulary")
	}

	defect := ConclusionModeFor(ObjectiveInformational, "Investigate the order flow and conclude SAFE, BUG, or UNRESOLVED")
	if defect != ConclusionDefect {
		t.Fatalf("failure investigation mode = %q, want %q", defect, ConclusionDefect)
	}
	if defect.Accepts(VerdictAnswered) || defect.Accepts(VerdictPartial) {
		t.Fatal("a defect investigation admitted an answer verdict")
	}
	if !defect.Accepts(VerdictSafe) || !defect.Accepts(VerdictBug) {
		t.Fatal("a defect investigation rejected its own outcome vocabulary")
	}

	// Both vocabularies retain UNRESOLVED: either objective can honestly fail
	// to reach a terminal finding.
	if !answer.Accepts(VerdictUnresolved) || !defect.Accepts(VerdictUnresolved) {
		t.Fatal("UNRESOLVED was not shared by both vocabularies")
	}
}

func TestConclusionModeIsEmptyOutsideInformationalObjectives(t *testing.T) {
	if mode := ConclusionModeFor(ObjectiveMutation, "fix the parser"); mode != "" {
		t.Fatalf("mutation mode = %q, want empty", mode)
	}
	if mode := ConclusionModeFor(ObjectiveExecution, "run the tests"); mode != "" {
		t.Fatalf("execution mode = %q, want empty", mode)
	}
}

func TestIsProvisionalConclusion(t *testing.T) {
	for _, text := range []string{
		"placeholder, not yet sufficient",
		"Preliminary only; investigation still in progress.",
		"not enough evidence to resolve the bypass question",
		"insufficient evidence",
		"investigation remains open",
	} {
		if !IsProvisionalConclusion(text) {
			t.Errorf("provisional marker was not recognized: %q", text)
		}
	}
	for _, text := range []string{
		"Reserve returns ErrInsufficientStock when stock is insufficient",
		"the handler is not complete but the bypass is reachable",
		"the order remains unresolved in this branch",
		"the implementation is incomplete for refunds",
	} {
		if IsProvisionalConclusion(text) {
			t.Errorf("final finding was misclassified as provisional: %q", text)
		}
	}
}
