package workspace

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ArtifactKind names what an artifact carries across workspaces.
//
// Cross-workspace communication is artifact-based, not conversation-based
// (§23, I12): a handoff references an artifact identity rather than replaying a
// transcript, which keeps context small and keeps authority in the destination
// workspace.
type ArtifactKind string

// Artifact kinds. Each workspace produces one primary kind.
const (
	// KindPlan is produced by ASK: intended work, without authority to do it.
	KindPlan ArtifactKind = "plan"
	// KindFinding is produced by FUZZ: reproduced evidence of a defect.
	KindFinding ArtifactKind = "finding"
	// KindChange is produced by BUILD: what was changed.
	KindChange ArtifactKind = "change"
	// KindVerification is produced by BUILD: how a change was verified.
	KindVerification ArtifactKind = "verification"
)

// Valid reports whether the kind is one of the four known kinds.
func (k ArtifactKind) Valid() bool {
	switch k {
	case KindPlan, KindFinding, KindChange, KindVerification:
		return true
	}
	return false
}

// ErrInvalidArtifact means an artifact is missing a field required for a
// trustworthy handoff.
var ErrInvalidArtifact = errors.New("workspace: invalid artifact")

// Artifact is a structured handoff between workspaces.
//
// It is deliberately plain data: the runtime owns its identity and the caller
// owns its contents. An artifact carries evidence, never authority — BUILD
// receives a Finding but does not inherit any capability from FUZZ (§24, I13).
type Artifact struct {
	// ID is the stable artifact identity, e.g. "finding-8f31".
	ID string
	// Kind is what the artifact carries.
	Kind ArtifactKind
	// Origin is the workspace that produced it.
	Origin Workspace
	// Target names the subject, e.g. "internal/runtime.go".
	Target string
	// Status is the artifact's own state, e.g. "reproduced".
	Status string
	// Expected is what should have happened.
	Expected string
	// Observed is what actually happened.
	Observed string
	// Reproduction is how to trigger it, e.g. "go test -race ./...".
	Reproduction string
	// Evidence is the captured output backing the claim.
	Evidence string
	// CreatedAt is when the artifact was produced.
	CreatedAt time.Time
}

// Validate reports whether an artifact is complete enough to hand off.
//
// A finding without a target, a reproduction, or evidence is a claim, not a
// handoff; refusing it keeps BUILD from acting on an unsubstantiated report.
func (a Artifact) Validate() error {
	if a.ID == "" {
		return fmt.Errorf("%w: missing id", ErrInvalidArtifact)
	}
	if !a.Kind.Valid() {
		return fmt.Errorf("%w: unknown kind %q", ErrInvalidArtifact, a.Kind)
	}
	if !a.Origin.Valid() {
		return fmt.Errorf("%w: unknown origin %q", ErrInvalidArtifact, a.Origin)
	}
	if strings.TrimSpace(a.Target) == "" {
		return fmt.Errorf("%w: missing target", ErrInvalidArtifact)
	}
	if a.Kind == KindFinding {
		if strings.TrimSpace(a.Reproduction) == "" {
			return fmt.Errorf("%w: finding without reproduction", ErrInvalidArtifact)
		}
		if strings.TrimSpace(a.Evidence) == "" {
			return fmt.Errorf("%w: finding without evidence", ErrInvalidArtifact)
		}
	}
	return nil
}

// NewArtifactID mints a stable artifact identity from its kind.
//
// The identity is random rather than derived from content: two runs that
// discover the same defect are still two distinct observations, and an
// artifact id must never be guessed from a path or a summary.
func NewArtifactID(kind ArtifactKind) string {
	var buf [2]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// crypto/rand does not fail in practice; a monotonic fallback keeps
		// identity minting total rather than making every caller handle it.
		return string(kind) + "-0000"
	}
	return string(kind) + "-" + hex.EncodeToString(buf[:])
}

// Context is a workspace-bound authority context.
//
// A transition produces a new Context for the destination workspace. It does
// not carry capabilities forward: the destination's policy is the only source
// of authority (§33).
type Context struct {
	// Workspace is the active authority.
	Workspace Workspace
	// Model is the resolved effective model for this context, "provider/id".
	Model string
}

// Transition returns the context for a destination workspace.
//
// Capabilities are not a parameter: they are derived from dest alone, so a
// FUZZ→BUILD transition yields BUILD capabilities, never FUZZ+BUILD (§33).
func Transition(dest Workspace, model string) Context {
	return Context{Workspace: dest, Model: model}
}

// Handoff carries artifacts to a destination workspace.
//
// It is the explicit transition from §27: artifact → explicit workspace
// transition → new authority policy. The Handoff grants no authority of its
// own; the destination Context does.
type Handoff struct {
	// To is the destination workspace.
	To Workspace
	// Artifacts are the structured evidence being carried over.
	Artifacts []Artifact
}

// Validate reports whether the handoff is well formed: a valid destination and
// only complete artifacts.
func (h Handoff) Validate() error {
	if !h.To.Valid() {
		return fmt.Errorf("%w: unknown destination %q", ErrInvalidArtifact, h.To)
	}
	for i, a := range h.Artifacts {
		if err := a.Validate(); err != nil {
			return fmt.Errorf("artifact %d: %w", i, err)
		}
	}
	return nil
}
