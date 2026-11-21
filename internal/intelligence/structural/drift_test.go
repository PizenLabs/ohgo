package structural

import (
	"strings"
	"testing"
)

// A repository-wide survey must name modules that no sample represents rather
// than silently omitting them. A module with only test files has no production
// coordinate and stays unexplored.
func TestProjectSurveyNamesUnexploredModule(t *testing.T) {
	root := fixture(t, map[string]string{
		"go.mod":         "module example.com/app\n\ngo 1.21\n",
		"alpha/a.go":     "package alpha\n\nfunc A() {}\n",
		"beta/b_test.go": "package beta\n\nimport \"testing\"\n\nfunc TestB(t *testing.T) {}\n",
	})
	analysis := analyze(t, newIndex(t, root), Request{Intent: IntentProject}, nil)
	if analysis.Survey == nil {
		t.Fatal("project analysis returned no survey")
	}
	unexplored := strings.Join(analysis.Survey.Unexplored, ",")
	if !strings.Contains(unexplored, "beta") {
		t.Fatalf("unexplored = %v, want beta (test-only module)", analysis.Survey.Unexplored)
	}
	if analysis.Survey.Explored == 0 {
		t.Fatalf("survey = %+v, want at least one represented module", analysis.Survey)
	}
	if len(analysis.Samples) == 0 {
		t.Fatal("survey selected no representative coordinate")
	}
	if analysis.Coverage.Pending == 0 {
		t.Fatal("project survey parsed source; it must stay metadata-first")
	}
}

func TestDocumentationDriftFindsMissingDocumentedPath(t *testing.T) {
	// Documentation can claim that a path exists; only the inventory can
	// confirm it. The drift report must cite the document line and the
	// missing target, and it must not treat a fenced example as a claim.
	root := fixture(t, map[string]string{
		"go.mod":          "module example.com/app\n\ngo 1.21\n",
		"cmd/app/main.go": "package main\n\nfunc main() {}\n",
		"docs/guide.md":   "# Guide\n\nSee `../cmd/app/main.go`.\n",
		"README.md": "# App\n\n" +
			"Use `cmd/app/main.go` and `docs/missing.yaml`.\n" +
			"Read the [guide](docs/guide.md).\n\n" +
			"```sh\n" +
			"`cmd/example/main.go` is illustrative only\n" +
			"```\n",
	})
	analysis := analyze(t, newIndex(t, root), Request{Intent: IntentDrift}, nil)
	if analysis.Drift == nil {
		t.Fatal("drift analysis returned no report")
	}
	report := analysis.Drift
	if report.Claims < 3 {
		t.Fatalf("claims = %d, want at least 3", report.Claims)
	}
	if report.Resolved < 2 {
		t.Fatalf("resolved = %d, want at least 2", report.Resolved)
	}
	var missing *DriftFinding
	for i := range report.Findings {
		if report.Findings[i].Target == "docs/missing.yaml" {
			missing = &report.Findings[i]
		}
		if report.Findings[i].Target == "cmd/example/main.go" {
			t.Fatalf("fenced example became a drift finding: %+v", report.Findings[i])
		}
	}
	if missing == nil {
		t.Fatalf("missing documented path was not reported: %+v", report.Findings)
	}
	if missing.Code != "documented_path_missing" || missing.Severity != DriftHigh {
		t.Fatalf("finding = %+v, want high missing path", missing)
	}
	if missing.Document != "README.md" || missing.Line != 3 {
		t.Fatalf("finding source = %s:%d, want README.md:3", missing.Document, missing.Line)
	}
	if missing.Documentation.State != StateObserved || missing.Source.State != StateObserved {
		t.Fatalf("finding evidence = doc %s source %s, want both OBSERVED",
			missing.Documentation.State, missing.Source.State)
	}
	if report.Evidence.State != StateObserved {
		t.Fatalf("report evidence = %s (%s), want OBSERVED", report.Evidence.State, report.Evidence.Reason)
	}
	if analysis.Coverage.State != StateObserved {
		t.Fatalf("coverage = %s (%s), want OBSERVED for a complete inventory",
			analysis.Coverage.State, analysis.Coverage.Reason)
	}
}

func TestDocumentationDriftWithoutDocumentationIsUnknown(t *testing.T) {
	root := fixture(t, map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.21\n",
		"a.go":   "package app\n\nfunc A() {}\n",
	})
	analysis := analyze(t, newIndex(t, root), Request{Intent: IntentDrift}, nil)
	if analysis.Drift == nil {
		t.Fatal("drift analysis returned no report")
	}
	if analysis.Drift.Evidence.State != StateUnknown {
		t.Fatalf("evidence = %s, want UNKNOWN", analysis.Drift.Evidence.State)
	}
	if analysis.Drift.Claims != 0 || len(analysis.Drift.Findings) != 0 {
		t.Fatalf("report = %+v, want no claims or findings", analysis.Drift)
	}
	if analysis.Coverage.State != StateUnknown {
		t.Fatalf("coverage = %s, want UNKNOWN", analysis.Coverage.State)
	}
}
