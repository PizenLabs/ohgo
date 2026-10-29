package workspace

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestNewArtifactIDIsKindPrefixed(t *testing.T) {
	id := NewArtifactID(KindFinding)
	if !strings.HasPrefix(id, "finding-") {
		t.Errorf("id = %q, want a finding- prefix", id)
	}
	if NewArtifactID(KindFinding) == id {
		t.Error("two artifacts received the same id")
	}
}

func TestArtifactValidateRequiresFindingEvidence(t *testing.T) {
	// A finding without a reproduction or evidence is a claim, not a handoff,
	// and must be refused before BUILD can act on it (§23).
	base := Artifact{
		ID:     "finding-1",
		Kind:   KindFinding,
		Origin: Fuzz,
		Target: "internal/runtime.go",
	}
	if err := base.Validate(); err == nil {
		t.Fatal("a finding without reproduction or evidence was accepted")
	}
	base.Reproduction = "go test -race ./..."
	if err := base.Validate(); err == nil {
		t.Fatal("a finding without evidence was accepted")
	}
	base.Evidence = "DATA RACE in runtime.go:42"
	if err := base.Validate(); err != nil {
		t.Errorf("a complete finding was refused: %v", err)
	}
}

func TestArtifactValidateRejectsUnknownKindAndOrigin(t *testing.T) {
	good := Artifact{ID: "change-1", Kind: KindChange, Origin: Build, Target: "a.go"}
	if err := good.Validate(); err != nil {
		t.Fatalf("a complete change was refused: %v", err)
	}
	bad := good
	bad.Kind = "rumour"
	if err := bad.Validate(); !errors.Is(err, ErrInvalidArtifact) {
		t.Errorf("unknown kind error = %v, want ErrInvalidArtifact", err)
	}
	bad = good
	bad.Origin = "plan"
	if err := bad.Validate(); err == nil {
		t.Error("an unknown origin was accepted")
	}
}

func TestTransitionDropsCapabilities(t *testing.T) {
	// FUZZ→BUILD yields BUILD capabilities, never FUZZ+BUILD (§33). The
	// context has no capability field precisely so this cannot be violated.
	ctx := Transition(Build, "openrouter/model-x")
	if ctx.Workspace != Build {
		t.Fatalf("workspace = %s, want build", ctx.Workspace)
	}
	if ctx.Model != "openrouter/model-x" {
		t.Errorf("model = %q, want the resolved model", ctx.Model)
	}
	if !ctx.Workspace.Permits(OpWrite) {
		t.Error("the destination context must carry the destination's authority")
	}
	if Transition(Fuzz, "m").Workspace.Permits(OpWrite) {
		t.Error("a FUZZ context must not carry write authority")
	}
}

func TestHandoffValidateRequiresValidDestinationAndArtifacts(t *testing.T) {
	finding := Artifact{
		ID: "finding-1", Kind: KindFinding, Origin: Fuzz, Target: "x.go",
		Reproduction: "go test ./...", Evidence: "fail",
	}
	if err := (Handoff{To: Build, Artifacts: []Artifact{finding}}).Validate(); err != nil {
		t.Errorf("valid handoff refused: %v", err)
	}
	if err := (Handoff{To: "nope", Artifacts: []Artifact{finding}}).Validate(); err == nil {
		t.Error("handoff to an unknown destination was accepted")
	}
	if err := (Handoff{To: Build, Artifacts: []Artifact{{ID: "finding-2", Kind: KindFinding, Origin: Fuzz}}}).Validate(); err == nil {
		t.Error("handoff carrying an incomplete artifact was accepted")
	}
}

func TestArtifactTimestampSurvivesValidate(t *testing.T) {
	a := Artifact{
		ID: "plan-1", Kind: KindPlan, Origin: Ask, Target: "the migration",
		CreatedAt: time.Unix(1, 0),
	}
	if err := a.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if a.CreatedAt.IsZero() {
		t.Error("created-at was lost")
	}
}
