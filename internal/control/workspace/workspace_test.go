package workspace

import (
	"errors"
	"strings"
	"testing"
)

func TestParseRejectsUnknownWorkspace(t *testing.T) {
	// A typo must not silently resolve to an authority the user did not pick.
	if _, err := Parse("bulid"); err == nil {
		t.Fatal("expected an unknown workspace to be rejected")
	}
	for _, want := range All() {
		got, err := Parse(strings.ToUpper(string(want)))
		if err != nil {
			t.Fatalf("Parse(%q): %v", want, err)
		}
		if got != want {
			t.Errorf("Parse(%q) = %q, want %q", want, got, want)
		}
	}
}

func TestPermitsMutationOnlyInBuild(t *testing.T) {
	mutations := []Operation{OpWrite, OpPatch, OpDelete, OpMove}
	for _, w := range All() {
		for _, op := range mutations {
			want := w == Build
			if got := w.Permits(op); got != want {
				t.Errorf("%s.Permits(%s) = %v, want %v", w, op, got, want)
			}
		}
	}
}

func TestPermitsExecutionInFuzzAndBuildOnly(t *testing.T) {
	// Execution authority and mutation authority are different concepts; ASK
	// is reasoning only and does not run commands (§4.2, §5.2).
	if Ask.Permits(OpExec) {
		t.Error("ASK must not run commands")
	}
	if !Fuzz.Permits(OpExec) {
		t.Error("FUZZ must be able to run commands")
	}
	if !Build.Permits(OpExec) {
		t.Error("BUILD must be able to run commands")
	}
}

func TestPermitsReadsEverywhere(t *testing.T) {
	for _, w := range All() {
		for _, op := range []Operation{OpRead, OpSearch, OpInspect} {
			if !w.Permits(op) {
				t.Errorf("%s must read", w)
			}
		}
	}
}

func TestInvalidWorkspacePermitsNothing(t *testing.T) {
	// The zero value is invalid on purpose: an unresolved workspace must be
	// denied rather than defaulting to an authority.
	var zero Workspace
	if zero.Valid() {
		t.Fatal("zero workspace reported valid")
	}
	if zero.Permits(OpRead) {
		t.Error("an invalid workspace must not read")
	}
	if zero.Permits(OpWrite) {
		t.Error("an invalid workspace must not write")
	}
}

func TestUnknownOperationIsDenied(t *testing.T) {
	// A capability the policy cannot classify is not one it can grant.
	if Build.Permits(Operation("teleport")) {
		t.Error("an unknown operation must be denied even in BUILD")
	}
}

func TestAdmitRefusalMatchesContractShape(t *testing.T) {
	// The refusal record is the runtime's statement of what it refused:
	// "capability denied workspace=fuzz operation=write
	// reason=mutation_not_permitted" (§5.3).
	err := Admit(Fuzz, OpWrite)
	if err == nil {
		t.Fatal("expected FUZZ write to be denied")
	}
	var denied *Denied
	if !errors.As(err, &denied) {
		t.Fatalf("error type = %T, want *Denied", err)
	}
	if denied.Workspace != Fuzz || denied.Operation != OpWrite {
		t.Errorf("denied = %s/%s, want fuzz/write", denied.Workspace, denied.Operation)
	}
	if denied.Reason != ReasonMutationNotPermitted {
		t.Errorf("reason = %q, want %q", denied.Reason, ReasonMutationNotPermitted)
	}
	msg := err.Error()
	for _, want := range []string{"workspace=fuzz", "operation=write", "reason=mutation_not_permitted"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q missing %q", msg, want)
		}
	}
}

func TestAdmitAllowsWhatPolicyPermits(t *testing.T) {
	if err := Admit(Build, OpWrite); err != nil {
		t.Errorf("BUILD write was refused: %v", err)
	}
	if err := Admit(Fuzz, OpExec); err != nil {
		t.Errorf("FUZZ exec was refused: %v", err)
	}
	if err := Admit(Ask, OpRead); err != nil {
		t.Errorf("ASK read was refused: %v", err)
	}
}

func TestAdmitRefusesExecInAskWithItsOwnReason(t *testing.T) {
	err := Admit(Ask, OpExec)
	var denied *Denied
	if !errors.As(err, &denied) {
		t.Fatalf("error = %v, want *Denied", err)
	}
	if denied.Reason != ReasonExecNotPermitted {
		t.Errorf("reason = %q, want %q", denied.Reason, ReasonExecNotPermitted)
	}
}

func TestStyleIsSemanticAndStable(t *testing.T) {
	// ASK teal, BUILD blue, FUZZ magenta: three distinct accents that do not
	// depend on a theme (§9).
	accents := map[string]Workspace{}
	for _, w := range All() {
		style := w.Style()
		if style.Label != w.Label() {
			t.Errorf("%s label = %q, want %q", w, style.Label, w.Label())
		}
		if style.Accent == "" {
			t.Errorf("%s has no accent", w)
		}
		if other, seen := accents[style.Accent]; seen {
			t.Errorf("%s and %s share accent %q; identity must be distinct", w, other, style.Accent)
		}
		accents[style.Accent] = w
	}
	// The invalid workspace gets the neutral accent, never one of the three.
	var zero Workspace
	if zero.Style().Accent == Ask.Style().Accent {
		t.Error("an invalid workspace must not borrow an authority's accent")
	}
}
