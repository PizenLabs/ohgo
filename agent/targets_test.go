package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/intelligence/ast"
	"github.com/PizenLabs/ohgo/internal/security"
)

func writeTarget(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func removeTarget(t *testing.T, root, rel string) {
	t.Helper()
	if err := os.Remove(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
		t.Fatal(err)
	}
}

type indexResolver struct{ idx *ast.Index }

func newIndexResolver(t testing.TB, root string) indexResolver {
	t.Helper()
	idx, err := ast.NewIndex(root)
	if err != nil {
		t.Fatal(err)
	}
	return indexResolver{idx: idx}
}

func (r indexResolver) LocateSymbol(name string) ([]SymbolLocation, error) {
	matches, err := r.idx.Lookup(name)
	if err != nil {
		return nil, err
	}
	return projectMatches(matches), nil
}

func (r indexResolver) LocatePackage(name string) ([]SymbolLocation, error) {
	matches, err := r.idx.Package(name)
	if err != nil {
		return nil, err
	}
	return projectMatches(matches), nil
}

func projectMatches(matches []ast.Match) []SymbolLocation {
	out := make([]SymbolLocation, 0, len(matches))
	for _, match := range matches {
		out = append(out, SymbolLocation{
			Path:          match.Path,
			Symbol:        match.Name,
			QualifiedName: match.QualifiedName,
			Line:          match.Line,
			EndLine:       match.EndLine,
		})
	}
	return out
}

func pathContract(path string, op contract.ChangeOp, verification ...contract.VerificationRequirement) contract.IntentContract {
	target := contract.Target{Kind: contract.TargetPath, Value: path}
	return contract.IntentContract{
		Objective:       "update " + path,
		Kind:            contract.ObjectiveMutation,
		Targets:         []contract.Target{target},
		ExpectedChanges: []contract.ExpectedChange{{Target: target, Operation: op}},
		Verification:    verification,
	}
}

func symbolContract(symbol string, op contract.ChangeOp, verification ...contract.VerificationRequirement) contract.IntentContract {
	target := contract.Target{Kind: contract.TargetSymbol, Value: symbol}
	return contract.IntentContract{
		Objective:       "fix " + symbol,
		Kind:            contract.ObjectiveMutation,
		Targets:         []contract.Target{target},
		ExpectedChanges: []contract.ExpectedChange{{Target: target, Operation: op}},
		Verification:    verification,
	}
}

func mutationOutcome(root, path string) ToolOutcome {
	return ToolOutcome{
		ToolName: "write",
		Result: Result{
			Text:    "wrote",
			Mutates: true,
			Details: map[string]any{"path": path},
		},
		Authorization: &security.Decision{
			Capability: security.CapabilityWrite,
			Mutates:    true,
			Scope:      security.NormalizeScope(root, path),
		},
	}
}

func verificationOutcome(scopes ...VerificationScope) ToolOutcome {
	return verificationOutcomeWithCheck(contract.CheckAny, scopes...)
}

func verificationOutcomeWithCheck(check contract.VerificationCheck, scopes ...VerificationScope) ToolOutcome {
	return ToolOutcome{
		ToolName: "bash",
		Result: Result{
			Text:         "ok",
			Verifies:     true,
			Verification: &VerificationEvidence{Check: check, Scopes: scopes},
		},
	}
}

// Path targets are checked against the final tree, not against the tool's
// claim: create, modify, delete, revert, and the wrong direction are distinct.
func TestTargetSnapshotChecksPathDirections(t *testing.T) {
	cases := []struct {
		name   string
		before string
		after  string
		remove bool
		op     contract.ChangeOp
		want   TargetStatus
	}{
		{"create", "", "new\n", false, contract.ChangeCreate, TargetSatisfied},
		{"modify", "old\n", "new\n", false, contract.ChangeModify, TargetSatisfied},
		{"delete", "old\n", "", true, contract.ChangeDelete, TargetSatisfied},
		{"revert", "old\n", "old\n", false, contract.ChangeModify, TargetUnchanged},
		{"delete-still-present", "old\n", "new\n", false, contract.ChangeDelete, TargetWrongOperation},
		{"modify-removed", "old\n", "", true, contract.ChangeModify, TargetWrongOperation},
		{"modify-absent", "", "new\n", false, contract.ChangeModify, TargetMissing},
		{"create-existing", "old\n", "new\n", false, contract.ChangeCreate, TargetWrongOperation},
		{"any-change", "old\n", "new\n", false, contract.ChangeAny, TargetSatisfied},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if tc.before != "" {
				writeTarget(t, root, "note.txt", tc.before)
			}
			intent := pathContract("note.txt", tc.op)
			snapshot := CaptureTargets(root, intent, nil)
			if tc.after != "" {
				writeTarget(t, root, "note.txt", tc.after)
			}
			if tc.remove {
				removeTarget(t, root, "note.txt")
			}
			evidence := CheckTargets(snapshot, nil)
			if len(evidence.Checks) != 1 {
				t.Fatalf("checks = %d, want 1", len(evidence.Checks))
			}
			if evidence.Checks[0].Status != tc.want {
				t.Fatalf("status = %s, want %s (%s)", evidence.Checks[0].Status, tc.want, evidence.Checks[0].Detail)
			}
		})
	}
}

// A symbol target is checked by its declaration range, not by the whole file.
// Editing another function in the same file must not satisfy a request to
// modify this symbol.
func TestTargetSnapshotChecksSymbolDefinition(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "tools/grep.go", "package tools\n\nfunc GrepTool() string { return \"bug\" }\n\nfunc Other() string { return \"a\" }\n")
	resolver := newIndexResolver(t, root)
	intent := symbolContract("GrepTool", contract.ChangeModify)
	snapshot := CaptureTargets(root, intent, resolver)
	if len(snapshot.Entries) != 1 || !snapshot.Entries[0].Resolved {
		t.Fatalf("snapshot = %+v, want a resolved symbol", snapshot.Entries)
	}

	writeTarget(t, root, "tools/grep.go", "package tools\n\nfunc GrepTool() string { return \"bug\" }\n\nfunc Other() string { return \"b\" }\n")
	evidence := CheckTargets(snapshot, resolver)
	if evidence.Checks[0].Status != TargetUnchanged {
		t.Fatalf("status = %s, want unchanged after editing a different declaration", evidence.Checks[0].Status)
	}

	writeTarget(t, root, "tools/grep.go", "package tools\n\nfunc GrepTool() string { return \"fixed\" }\n\nfunc Other() string { return \"b\" }\n")
	evidence = CheckTargets(snapshot, resolver)
	if evidence.Checks[0].Status != TargetSatisfied {
		t.Fatalf("status = %s, want satisfied after editing the symbol", evidence.Checks[0].Status)
	}
}

// An absent symbol is unresolved evidence, not "unchanged"; the evaluator must
// not treat it as a satisfied target.
func TestTargetSnapshotLeavesUnknownSymbolUnresolved(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "note.txt", "old\n")
	intent := symbolContract("MissingSymbol", contract.ChangeModify)
	snapshot := CaptureTargets(root, intent, newIndexResolver(t, root))
	if snapshot.Entries[0].Resolved {
		t.Fatalf("missing symbol resolved: %+v", snapshot.Entries[0])
	}
	evidence := CheckTargets(snapshot, newIndexResolver(t, root))
	if evidence.Checks[0].Status != TargetUnresolved {
		t.Fatalf("status = %s, want unresolved", evidence.Checks[0].Status)
	}
}

// Cancellation is observed before a target capture starts; a cancelled run
// does not extend itself by reading verification evidence.
func TestCaptureTargetsHonoursCancellation(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "note.txt", "old\n")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	snapshot := CaptureTargetsContext(ctx, root, pathContract("note.txt", contract.ChangeModify), nil)
	if len(snapshot.Issues) == 0 {
		t.Fatal("a cancelled capture reported no issue")
	}
	if !strings.Contains(snapshot.Issues[0], "cancelled") {
		t.Fatalf("issue = %q, want cancellation", snapshot.Issues[0])
	}
	if len(snapshot.Entries) != 0 {
		t.Fatalf("entries = %+v, want none after cancellation", snapshot.Entries)
	}
}

// Command attribution is token-boundary aware: a target named in a shell
// command is not confused with a longer path that happens to contain it.
func TestCommandMentionsAWholePath(t *testing.T) {
	cases := []struct {
		command string
		value   string
		want    bool
	}{
		{"gofmt -w tools/grep.go", "tools/grep.go", true},
		{"go fmt ./tools", "tools", true},
		{"go fmt ./tools2", "tools", false},
		{"echo new > note.txt", "note.txt", true},
		{"echo new > other-note.txt", "note.txt", false},
	}
	for _, tc := range cases {
		if got := commandMentions(tc.command, tc.value); got != tc.want {
			t.Errorf("commandMentions(%q, %q) = %v, want %v", tc.command, tc.value, got, tc.want)
		}
	}
}

// The verification read is bounded. An oversized target is reviewable instead
// of being silently skipped or read without limit.
func TestTargetSnapshotBoundsLargeFiles(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "big.txt", strings.Repeat("x", MaxTargetFileBytes+1))
	intent := pathContract("big.txt", contract.ChangeModify)
	snapshot := CaptureTargets(root, intent, nil)
	if snapshot.Entries[0].Resolved {
		t.Fatal("an oversized target was resolved")
	}
	if !strings.Contains(snapshot.Entries[0].Issue, "bound") {
		t.Fatalf("issue = %q, want a bound explanation", snapshot.Entries[0].Issue)
	}
}

// The total read is bounded too: many individually acceptable files cannot
// make one completion check read an unbounded amount of the repository.
func TestTargetSnapshotBoundsTotalBytes(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "note.txt", "x")
	cache := map[string]targetFile{}
	reads := &targetReads{bytes: MaxTargetBytes}
	if _, err := readTargetFile(root, "note.txt", cache, reads); err == nil {
		t.Fatal("a read past the total byte bound succeeded")
	} else if !strings.Contains(err.Error(), "total bound") {
		t.Fatalf("err = %v, want the total-byte bound", err)
	}
}
