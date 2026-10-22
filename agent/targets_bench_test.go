package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/PizenLabs/ohgo/contract"
)

func benchmarkPathContract() contract.IntentContract {
	target := contract.Target{Kind: contract.TargetPath, Value: "note.txt"}
	return contract.IntentContract{
		Objective:       "update note.txt",
		Kind:            contract.ObjectiveMutation,
		Targets:         []contract.Target{target},
		ExpectedChanges: []contract.ExpectedChange{{Target: target, Operation: contract.ChangeModify}},
		Verification: []contract.VerificationRequirement{{
			Kind: contract.VerifyScope, Scope: "*", Check: contract.CheckTest,
		}},
	}
}

// BenchmarkCaptureTargetsPath measures the pre-run evidence cost for the common
// single-file target. It reads only the named file.
func BenchmarkCaptureTargetsPath(b *testing.B) {
	root := b.TempDir()
	path := filepath.Join(root, "note.txt")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		b.Fatal(err)
	}
	intent := benchmarkPathContract()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		snapshot := CaptureTargets(root, intent, nil)
		if len(snapshot.Entries) != 1 || !snapshot.Entries[0].Resolved {
			b.Fatal("target was not captured")
		}
	}
}

// BenchmarkCaptureTargetsSymbol measures the structural lookup and declaration
// hashing cost. The index is built once in setup; the measured loop is the
// per-completion freshness walk and range read.
func BenchmarkCaptureTargetsSymbol(b *testing.B) {
	root := b.TempDir()
	for i := 0; i < 40; i++ {
		body := "package tools\n\nfunc Fn" + itoaBench(i) + "() int { return " + itoaBench(i) + " }\n"
		if err := os.WriteFile(filepath.Join(root, "f"+itoaBench(i)+".go"), []byte(body), 0o644); err != nil {
			b.Fatal(err)
		}
	}
	resolver := newIndexResolver(b, root)
	target := contract.Target{Kind: contract.TargetSymbol, Value: "Fn0"}
	intent := contract.IntentContract{
		Objective:       "fix Fn0",
		Kind:            contract.ObjectiveMutation,
		Targets:         []contract.Target{target},
		ExpectedChanges: []contract.ExpectedChange{{Target: target, Operation: contract.ChangeModify}},
		Verification: []contract.VerificationRequirement{{
			Kind: contract.VerifyScope, Scope: "*", Check: contract.CheckTest,
		}},
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		snapshot := CaptureTargets(root, intent, resolver)
		if len(snapshot.Entries) != 1 || !snapshot.Entries[0].Resolved {
			b.Fatal("symbol target was not captured")
		}
	}
}

func itoaBench(value int) string {
	if value == 0 {
		return "0"
	}
	var digits []byte
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}

// BenchmarkEvaluateTaskCompletionPath measures the post-run check and semantic
// decision for a single-file target. The model/tool loop is not included.
func BenchmarkEvaluateTaskCompletionPath(b *testing.B) {
	root := b.TempDir()
	path := filepath.Join(root, "note.txt")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		b.Fatal(err)
	}
	intent := benchmarkPathContract()
	snapshot := CaptureTargets(root, intent, nil)
	if err := os.WriteFile(path, []byte("new\n"), 0o644); err != nil {
		b.Fatal(err)
	}
	turns := []Turn{
		{Results: []ToolOutcome{{
			ToolName: "write",
			Result:   Result{Text: "wrote", Mutates: true, Details: map[string]any{"path": "note.txt"}},
		}}},
		{Results: []ToolOutcome{{
			ToolName: "bash",
			Result: Result{
				Text:         "ok",
				Verifies:     true,
				Verification: &VerificationEvidence{Check: contract.CheckTest, Scopes: []VerificationScope{{All: true}}},
			},
		}}},
		{},
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		verdict := EvaluateTaskCompletion(CompletionInput{
			Turns:    turns,
			Contract: intent,
			Snapshot: snapshot,
			Root:     root,
		})
		if !verdict.Complete() {
			b.Fatalf("verdict = %s (%s)", verdict.State, verdict.Reason)
		}
	}
}
