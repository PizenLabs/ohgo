package ast_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/PizenLabs/ohgo/internal/execution/tools"
	"github.com/PizenLabs/ohgo/internal/intelligence/ast"
)

// TestAuditTargetSnapshot is an opt-in, read-only measurement against a real
// repository. It is skipped unless OHGO_AUDIT_TARGET is set, so the ordinary
// suite never depends on a checkout outside this module. It writes nothing:
// the structural source keeps its index in memory.
func TestAuditTargetSnapshot(t *testing.T) {
	target := os.Getenv("OHGO_AUDIT_TARGET")
	if target == "" {
		t.Skip("set OHGO_AUDIT_TARGET to a repository root to measure its structural snapshot")
	}
	source := tools.NewStructuralSource(target)
	idx, err := source.Index()
	if err != nil {
		t.Fatalf("index %s: %v", target, err)
	}
	symbol := os.Getenv("OHGO_AUDIT_SYMBOL")
	var refs ast.RefResult
	var impact ast.ImpactResult
	var tests ast.TestResult
	var haveRefs, haveImpact, haveTests bool
	if symbol != "" {
		ctx := context.Background()
		refs, err = idx.References(ctx, ast.RefQuery{Symbol: symbol})
		haveRefs = err == nil
		impact, err = idx.Impact(ctx, ast.ImpactQuery{Symbol: symbol, Depth: 2, Limit: 50})
		haveImpact = err == nil
		tests, err = idx.Tests(ctx, ast.TestQuery{Symbol: symbol, Limit: 50})
		haveTests = err == nil
	}
	info := idx.Info()
	report := map[string]any{
		"root":                info.Root,
		"module":              info.Module,
		"module_root":         info.ModuleRoot,
		"module_ambiguous":    info.ModuleAmbiguous,
		"module_candidates":   info.ModuleCandidates,
		"files":               info.Files,
		"source_files":        info.SourceFiles,
		"parsed_files":        info.ParsedFiles,
		"unsupported":         info.Unsupported,
		"languages":           info.Languages,
		"packages":            info.Packages,
		"symbols":             info.Symbols,
		"references":          info.References,
		"calls":               info.Calls,
		"test_files":          info.TestFiles,
		"test_symbols":        info.Tests,
		"entrypoints":         info.Entrypoints,
		"scope_complete":      info.ScopeComplete,
		"inventory_complete":  info.InventoryComplete,
		"structural_complete": info.StructuralComplete,
	}
	if haveRefs {
		report["refs"] = refs
	}
	if haveImpact {
		report["impact"] = impact
	}
	if haveTests {
		report["tests"] = tests
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("structural snapshot:\n%s", encoded)
}
