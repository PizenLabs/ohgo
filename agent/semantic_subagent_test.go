package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/PizenLabs/ohgo/ai"
	"github.com/PizenLabs/ohgo/contract"
	"github.com/PizenLabs/ohgo/jsonschema"
)

// fileEditTool is a minimal writable tool for the subagent completion seam. It
// writes the named target and reports the runtime mutation bit.
type fileEditTool struct {
	root string
	path string
}

func (t *fileEditTool) Name() string                       { return "edit" }
func (t *fileEditTool) Description() string                { return "edit a file" }
func (t *fileEditTool) Parameters() *jsonschema.Schema     { return nil }
func (t *fileEditTool) Approval() Approval                 { return Approval{Tier: TierWrite} }
func (t *fileEditTool) Exclusive(args map[string]any) bool { return true }
func (t *fileEditTool) Execute(ctx Context, args map[string]any) Result {
	path := t.path
	if path == "" {
		path = "note.txt"
	}
	if err := os.WriteFile(filepath.Join(t.root, path), []byte("new\n"), 0o644); err != nil {
		return ErrorResult("%v", err)
	}
	return Result{Text: "edited", Mutates: true, Details: map[string]any{"path": path}}
}

// verificationTool reports a successful check with a root scope.
type verificationTool struct{}

func (t *verificationTool) Name() string                       { return "bash" }
func (t *verificationTool) Description() string                { return "run a check" }
func (t *verificationTool) Parameters() *jsonschema.Schema     { return nil }
func (t *verificationTool) Approval() Approval                 { return Approval{Tier: TierExec} }
func (t *verificationTool) Exclusive(args map[string]any) bool { return true }
func (t *verificationTool) Execute(ctx Context, args map[string]any) Result {
	return Result{
		Text:         "ok",
		Executes:     true,
		Verifies:     true,
		Verification: &VerificationEvidence{Command: "go test .", Scopes: []VerificationScope{{Path: "."}}},
	}
}

func callMessage(id, name string) ai.AssistantMessage {
	return ai.AssistantMessage{
		Content:    []ai.ContentBlock{ai.ToolCall{ID: id, Name: name, Arguments: map[string]any{}}},
		StopReason: ai.StopToolUse,
	}
}

// A delegated child uses the same contract evaluator as the parent. The
// completion verdict and the verification scopes must survive the child
// boundary, or the parent would inherit a reviewable result as success.
func TestRunSubagentUsesTheIntentContract(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := contract.Target{Kind: contract.TargetPath, Value: "note.txt"}
	intent := contract.IntentContract{
		Objective:       "update note.txt",
		Kind:            contract.ObjectiveMutation,
		Targets:         []contract.Target{target},
		ExpectedChanges: []contract.ExpectedChange{{Target: target, Operation: contract.ChangeModify}},
		Verification:    []contract.VerificationRequirement{{Kind: contract.VerifyAny}},
	}
	snapshot := CaptureTargets(root, intent, nil)

	parent := Config{
		Stream: scriptedMessages(
			callMessage("edit-1", "edit"),
			callMessage("check-1", "bash"),
			ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "done"}}, StopReason: ai.StopEndTurn},
		),
		Tools:    []Tool{&fileEditTool{root: root}, &verificationTool{}},
		MaxTurns: 5,
	}
	result, err := RunSubagent(context.Background(), parent, SubagentSpec{
		Task:     "update note.txt and verify",
		System:   []ai.SystemPromptBlock{{Text: "child"}},
		Contract: &intent,
		Snapshot: snapshot,
	})
	if err != nil {
		t.Fatalf("RunSubagent: %v", err)
	}
	if !result.Completion.Complete() {
		t.Fatalf("child completion = %s (%s), want complete", result.Completion.State, result.Completion.Reason)
	}
	if result.Verification == nil || !result.Verification.Covers(".") {
		t.Fatalf("verification = %+v, want the root scope", result.Verification)
	}
}

// A child that changes the wrong file cannot inherit completion from its
// parent's legacy mutation bit. This is the delegation half of the false
// completion contract.
func TestRunSubagentRejectsWrongContractTarget(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := contract.Target{Kind: contract.TargetPath, Value: "note.txt"}
	intent := contract.IntentContract{
		Objective:       "update note.txt",
		Kind:            contract.ObjectiveMutation,
		Targets:         []contract.Target{target},
		ExpectedChanges: []contract.ExpectedChange{{Target: target, Operation: contract.ChangeModify}},
		Verification:    []contract.VerificationRequirement{{Kind: contract.VerifyAny}},
	}
	snapshot := CaptureTargets(root, intent, nil)

	parent := Config{
		Stream: scriptedMessages(
			callMessage("edit-1", "edit"),
			callMessage("check-1", "bash"),
			ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "done"}}, StopReason: ai.StopEndTurn},
		),
		Tools:    []Tool{&fileEditTool{root: root, path: "other.txt"}, &verificationTool{}},
		MaxTurns: 5,
	}
	result, err := RunSubagent(context.Background(), parent, SubagentSpec{
		Task:     "update note.txt and verify",
		System:   []ai.SystemPromptBlock{{Text: "child"}},
		Contract: &intent,
		Snapshot: snapshot,
	})
	if err != nil {
		t.Fatalf("RunSubagent: %v", err)
	}
	if result.Completion.Complete() {
		t.Fatal("a child that changed the wrong target was reported complete")
	}
}

// scriptedMessages serves one completed assistant message per request.
func scriptedMessages(messages ...ai.AssistantMessage) ai.StreamFn {
	index := 0
	return func(context.Context, ai.Request) (*ai.Stream, error) {
		if index >= len(messages) {
			return ai.NewCompletedStream(ai.AssistantMessage{StopReason: ai.StopEndTurn}), nil
		}
		message := messages[index]
		index++
		return ai.NewCompletedStream(message), nil
	}
}
