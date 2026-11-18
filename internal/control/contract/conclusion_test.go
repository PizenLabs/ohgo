package contract

import "testing"

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
