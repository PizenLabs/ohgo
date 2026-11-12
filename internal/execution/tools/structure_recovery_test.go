package tools

import (
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
)

// A selector the selected view does not accept is a request-level correction,
// not a capability outage. The guidance must name a registered view and the
// exact selector reuse, and the corrected invocation must run immediately.
func TestStructureSelectorRejectionIsCorrectable(t *testing.T) {
	root := structureFixture(t, map[string]string{
		"backend/cmd/api/main.go": "package main\n\nfunc main() {}\n",
		"backend/internal/api.go": "package api\n\nfunc Handler() {}\n",
	})
	tool := NewStructureTool(NewStructuralSource(root))
	ctx := testContext()

	overview := tool.Execute(ctx, map[string]any{"view": "overview"})
	if overview.IsError {
		t.Fatalf("overview: %s", overview.Text)
	}
	if !strings.Contains(overview.Text, "snapshot_id:") {
		t.Fatalf("overview lost snapshot provenance:\n%s", overview.Text)
	}

	rejected := tool.Execute(ctx, map[string]any{"path": "backend"})
	if !rejected.IsError {
		t.Fatal("overview accepted a path selector")
	}
	if rejected.FailureClass != agent.ToolFailureInvalidSelector {
		t.Fatalf("failure class = %q, want %q", rejected.FailureClass, agent.ToolFailureInvalidSelector)
	}
	if !rejected.Rejected() {
		t.Fatal("selector rejection was not marked request-level")
	}
	for _, want := range []string{
		`view "files"`, `path "backend"`, "Valid views:", "nothing executed",
	} {
		if !strings.Contains(rejected.Text, want) {
			t.Fatalf("correction missing %q:\n%s", want, rejected.Text)
		}
	}
	if strings.Contains(rejected.Text, "remote_tool_error") ||
		strings.Contains(rejected.Text, "already failed") {
		t.Fatalf("request rejection was described as a capability outage:\n%s", rejected.Text)
	}

	corrected := tool.Execute(ctx, map[string]any{"view": "files", "path": "backend"})
	if corrected.IsError {
		t.Fatalf("corrected invocation was rejected: %s", corrected.Text)
	}
	if !strings.Contains(corrected.Text, "view: files") {
		t.Fatalf("corrected invocation returned the wrong view:\n%s", corrected.Text)
	}
}

// View and argument vocabulary in the correction is the registered schema, not
// a guessed list. A malformed view still leaves the tool available.
func TestStructureInvocationCorrectionsUseTheRegisteredVocabulary(t *testing.T) {
	tool := NewStructureTool(NewStructuralSource(structureFixture(t, map[string]string{
		"main.go": "package main\n\nfunc main() {}\n",
	})))
	ctx := testContext()

	badView := tool.Execute(ctx, map[string]any{"view": "backend"})
	if badView.FailureClass != agent.ToolFailureInvalidSelector {
		t.Fatalf("bad view class = %q", badView.FailureClass)
	}
	for _, view := range structureViews {
		if !strings.Contains(badView.Text, view) {
			t.Fatalf("correction omitted registered view %q:\n%s", view, badView.Text)
		}
	}

	unknownArg := tool.Execute(ctx, map[string]any{"backend": true})
	if unknownArg.FailureClass != agent.ToolFailureInvalidArgument {
		t.Fatalf("unknown argument class = %q", unknownArg.FailureClass)
	}
	for _, arg := range structureArgs {
		if !strings.Contains(unknownArg.Text, arg) {
			t.Fatalf("correction omitted registered argument %q:\n%s", arg, unknownArg.Text)
		}
	}

	semantic := tool.Execute(ctx, map[string]any{"view": "overview", "activation": "semantic"})
	if semantic.FailureClass != agent.ToolFailureUnsupported {
		t.Fatalf("semantic activation class = %q", semantic.FailureClass)
	}
	if !semantic.Rejected() {
		t.Fatal("unsupported activation was not marked request-level")
	}
}
