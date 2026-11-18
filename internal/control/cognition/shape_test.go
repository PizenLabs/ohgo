package cognition

import (
	"testing"

	"github.com/PizenLabs/ohgo/internal/control/contract"
)

// A quoted "read-only" deliverable is content, not a whole-change limit. The
// shape must not withhold mutation tools from the BUILD task that writes it.
func TestShapeDoesNotWithholdMutationForQuotedReadOnlyContent(t *testing.T) {
	const request = `Add a short "Validation note" section at the end of README.md containing exactly one sentence: "This repository was used for a read-only Ohgo validation." Do not change anything else. Verify the file content after the edit.`
	shape := BuildShape(contract.IntentContract{
		Objective: request,
		Kind:      contract.ObjectiveMutation,
	}, ShapeParams{Authority: "build"})
	if hasConstraint(shape.Constraints, "no_mutation") {
		t.Fatalf("constraints = %v, want no whole-change prohibition", shape.Constraints)
	}
}
