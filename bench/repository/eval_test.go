package main

import (
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
)

func TestParseFinalAnswerReadsExplicitJSON(t *testing.T) {
	text := "The declaration is in tools/grep.go.\n\n" +
		`FINAL: {"status":"exact","targets":[{"path":"./tools/grep.go","symbol":"GrepTool.Execute","start_line":126,"end_line":288}]}`

	answer, format := parseFinalAnswer(text, "")
	if format != "json" {
		t.Fatalf("format = %q, want json", format)
	}
	if answer.Status != "exact" || len(answer.Targets) != 1 {
		t.Fatalf("answer = %+v", answer)
	}
	if answer.Targets[0].Path != "tools/grep.go" {
		t.Errorf("path = %q, want tools/grep.go", answer.Targets[0].Path)
	}
	if answer.Targets[0].Symbol != "GrepTool.Execute" {
		t.Errorf("symbol = %q", answer.Targets[0].Symbol)
	}
}

func TestParseFinalAnswerUsesTheLastContractLine(t *testing.T) {
	// A model that restates the contract or drafts an answer before its real
	// answer must not have the draft scored.
	text := `FINAL: {"status":"not_found","targets":[]}` + "\n" +
		`FINAL: {"status":"exact","targets":[{"path":"agent/loop.go","symbol":"ErrNoTools","start_line":16,"end_line":16}]}`

	answer, format := parseFinalAnswer(text, "")
	if format != "json" {
		t.Fatalf("format = %q", format)
	}
	if answer.Status != "exact" || len(answer.Targets) != 1 || answer.Targets[0].Path != "agent/loop.go" {
		t.Fatalf("answer = %+v", answer)
	}
}

func TestParseFinalAnswerAcceptsCommonFieldSpellings(t *testing.T) {
	text := "FINAL: {\"status\":\"ok\",\"targets\":[{\"file\":\"Makefile\",\"line\":49}]}"
	answer, format := parseFinalAnswer(text, "")
	if format != "json" {
		t.Fatalf("format = %q", format)
	}
	if len(answer.Targets) != 1 || answer.Targets[0].Path != "Makefile" {
		t.Fatalf("targets = %+v", answer.Targets)
	}
	if answer.Targets[0].StartLine != 49 || answer.Targets[0].EndLine != 49 {
		t.Fatalf("lines = %d-%d", answer.Targets[0].StartLine, answer.Targets[0].EndLine)
	}
}

func TestParseFinalAnswerAcceptsBareJSON(t *testing.T) {
	text := `{"status":"exact","targets":[{"path":"config/config.go","symbol":"Settings","start_line":93,"end_line":147}]}`
	answer, format := parseFinalAnswer(text, "")
	if format != "json" {
		t.Fatalf("format = %q, want json", format)
	}
	if answer.Status != "exact" || len(answer.Targets) != 1 || answer.Targets[0].Path != "config/config.go" {
		t.Fatalf("answer = %+v", answer)
	}
}

func TestParseFinalAnswerReportsLenientProse(t *testing.T) {
	text := "I found it at `tools/grep.go:126-288`."
	answer, format := parseFinalAnswer(text, "")
	if format != "lenient" {
		t.Fatalf("format = %q, want lenient", format)
	}
	if len(answer.Targets) != 1 || answer.Targets[0].Path != "tools/grep.go" {
		t.Fatalf("answer = %+v", answer)
	}
	if answer.Targets[0].StartLine != 126 || answer.Targets[0].EndLine != 288 {
		t.Fatalf("lines = %d-%d", answer.Targets[0].StartLine, answer.Targets[0].EndLine)
	}
}

func TestScoreNegativeRejectsATarget(t *testing.T) {
	task := Task{ExpectedStatus: "not_found"}
	answer := finalAnswer{Status: "not_found", Targets: []ReportedTarget{{Path: "x.go"}}}
	score := scoreAnswer(task, answer, "")
	if score.success {
		t.Fatal("a not_found answer that lists a target must fail")
	}
	if score.outcome != "false_positive" {
		t.Errorf("outcome = %q, want false_positive", score.outcome)
	}
}

func TestScoreAmbiguousRequiresAmbiguousStatus(t *testing.T) {
	task := Task{
		ExpectedStatus: "ambiguous",
		Targets: []Target{
			{Path: "a.go", Symbol: "Classify", StartLine: 10, EndLine: 12},
			{Path: "b.go", Symbol: "Classify", StartLine: 20, EndLine: 20},
		},
	}
	answer := finalAnswer{
		Status: "exact",
		Targets: []ReportedTarget{
			{Path: "a.go", Symbol: "Classify", StartLine: 10, EndLine: 12},
			{Path: "b.go", Symbol: "Classify", StartLine: 20, EndLine: 20},
		},
	}
	score := scoreAnswer(task, answer, "")
	if score.success {
		t.Fatal("an exact status must not pass an ambiguity-preserving task")
	}
	if score.outcome != "status_mismatch" {
		t.Errorf("outcome = %q, want status_mismatch", score.outcome)
	}
}

func TestScoreCrossFileRequiresEveryTarget(t *testing.T) {
	task := Task{
		ExpectedStatus: "exact",
		Targets: []Target{
			{Path: "ast/index.go", Symbol: "Index.Lookup", StartLine: 276, EndLine: 293},
			{Path: "tools/symbols.go", Symbol: "SymbolsTool.Execute", StartLine: 106, EndLine: 323},
		},
	}
	answer := finalAnswer{
		Status: "exact",
		Targets: []ReportedTarget{
			{Path: "ast/index.go", Symbol: "Index.Lookup", StartLine: 276, EndLine: 293},
		},
	}
	score := scoreAnswer(task, answer, "")
	if score.success {
		t.Fatal("one of two requested targets must not pass")
	}
	if score.missing != 1 {
		t.Errorf("missing = %d, want 1", score.missing)
	}
}

func TestScoreAcceptsQualifiedSymbolVariant(t *testing.T) {
	task := Task{
		ExpectedStatus: "exact",
		Targets: []Target{{
			Path: "tools/grep.go", Symbol: "GrepTool.Execute", StartLine: 126, EndLine: 288,
		}},
	}
	answer := finalAnswer{
		Status: "exact",
		Targets: []ReportedTarget{{
			Path: "tools/grep.go", Symbol: "tools.GrepTool.Execute", StartLine: 126, EndLine: 288,
		}},
	}
	if score := scoreAnswer(task, answer, ""); !score.success {
		t.Fatalf("qualified variant should pass: %+v", score)
	}
}

func TestScoreDoesNotAcceptSimilarSymbolName(t *testing.T) {
	task := Task{
		ExpectedStatus: "exact",
		Targets: []Target{{
			Path: "workspace/intent.go", Symbol: "Classify", StartLine: 118, EndLine: 118,
		}},
	}
	answer := finalAnswer{
		Status: "exact",
		Targets: []ReportedTarget{{
			Path: "workspace/intent.go", Symbol: "ClassifyPrediction", StartLine: 126, EndLine: 140,
		}},
	}
	if score := scoreAnswer(task, answer, ""); score.success {
		t.Fatal("a longer, different symbol name must not match")
	}
}

func TestSurfacedFilesReadsSymbolCoordinates(t *testing.T) {
	result := agent.Result{
		Text: "  - path: tools/grep.go\n",
		Details: map[string]any{
			"results": 1,
			"coordinates": []map[string]any{
				{"path": "tools/grep.go"},
			},
		},
	}
	files, total := surfacedFiles("symbols", nil, result, "")
	if total != 1 || len(files) != 1 || files[0] != "tools/grep.go" {
		t.Fatalf("files = %v total = %d", files, total)
	}
}

func TestSurfacedFilesReadsGrepFileHeaders(t *testing.T) {
	result := agent.Result{
		Text: "tools/grep.go:\n  126: func (t *GrepTool) Execute\n\n1 file(s) matched",
		Details: map[string]any{
			"files": 1,
		},
	}
	files, total := surfacedFiles("grep", nil, result, "")
	if total != 1 || len(files) != 1 || files[0] != "tools/grep.go" {
		t.Fatalf("files = %v total = %d", files, total)
	}
}

func TestCountNumberedLinesIgnoresReadNotice(t *testing.T) {
	text := "1\tpackage tools\n2\t\n3\tfunc f() {}\n\n[showing lines 1-3 of 9]"
	if got := countNumberedLines(text); got != 3 {
		t.Fatalf("count = %d, want 3", got)
	}
}

func TestNormalizePathStripsFixtureRoot(t *testing.T) {
	root := "/tmp/fixture"
	if got := normalizePath("/tmp/fixture/tools/grep.go", root); got != "tools/grep.go" {
		t.Fatalf("got %q", got)
	}
	if got := normalizePath("./tools/grep.go", root); got != "tools/grep.go" {
		t.Fatalf("got %q", got)
	}
}

func TestLineOverlapsToleratesSmallCommentOffsets(t *testing.T) {
	expected := Target{StartLine: 126, EndLine: 288}
	if !lineOverlaps(expected, ReportedTarget{StartLine: 124, EndLine: 288}, 2) {
		t.Fatal("a two-line comment offset should overlap")
	}
	if lineOverlaps(expected, ReportedTarget{StartLine: 10, EndLine: 20}, 2) {
		t.Fatal("a distant range must not overlap")
	}
}

func TestTaskSuiteHasEveryCategoryAndUniqueIDs(t *testing.T) {
	tasks := phase2Tasks()
	seen := map[string]bool{}
	categories := map[string]bool{}
	for _, task := range tasks {
		if task.ID == "" || task.Prompt == "" {
			t.Fatalf("task has an empty id or prompt: %+v", task)
		}
		if seen[task.ID] {
			t.Fatalf("duplicate task id %q", task.ID)
		}
		seen[task.ID] = true
		categories[task.Category] = true
	}
	for _, category := range []string{
		"A_EXACT", "B_AMBIGUOUS", "C_PATH_PACKAGE", "D_MODIFICATION",
		"E_CROSS_FILE", "F_NEGATIVE", "G_NOISE", "H_LEXICAL",
	} {
		if !categories[category] {
			t.Errorf("category %s has no task", category)
		}
	}
}

func TestTaskPromptsDoNotLeakToolNames(t *testing.T) {
	// The tool set is the variable under test. A prompt that names grep or
	// symbols would tell one track how to answer and invalidate the comparison.
	for _, task := range phase2Tasks() {
		lower := strings.ToLower(task.Prompt)
		for _, banned := range []string{"use symbols", "use grep", "run grep", "call symbols"} {
			if strings.Contains(lower, banned) {
				t.Errorf("%s recommends a track-specific tool in %q", task.ID, banned)
			}
		}
	}
}
