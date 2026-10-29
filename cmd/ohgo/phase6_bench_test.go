package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/execution/tools"
)

// phase6BenchTask is one discovery question. The static benchmark uses the
// prompt only as documentation; it executes the minimal correct tool plan for
// each track so the measured difference is the available information, not a
// scripted model's prose.
type phase6BenchTask struct {
	ID             string
	Category       string
	Prompt         string
	Kind           string
	Package        string
	Depth          int
	ExpectedStatus string
	ExpectedPkgs   []string
	ExpectedEdges  [][2]string
	CheckEdges     bool
	Target         *phase6BenchTarget
}

type phase6BenchTarget struct {
	Path      string
	Symbol    string
	StartLine int
	EndLine   int
}

type phase6BenchAnswer struct {
	Status   string
	Packages []string
	Edges    [][2]string
	Target   *phase6BenchTarget
}

type phase6BenchRun struct {
	TaskID          string      `json:"task_id"`
	Category        string      `json:"category"`
	Track           string      `json:"track"`
	Success         bool        `json:"success"`
	Outcome         string      `json:"outcome"`
	Status          string      `json:"status"`
	Turns           int         `json:"turns"`
	ModelCalls      int         `json:"model_calls"`
	ToolCalls       int         `json:"tool_calls"`
	GraphQueries    int         `json:"graph_queries"`
	ContextBytes    int         `json:"context_bytes"`
	ResultLines     int         `json:"result_lines"`
	FilesOpened     int         `json:"files_opened"`
	InputTokens     int         `json:"input_tokens"`
	OutputTokens    int         `json:"output_tokens"`
	AnswerFormat    string      `json:"answer_format,omitempty"`
	FinalText       string      `json:"final_text,omitempty"`
	Error           string      `json:"error,omitempty"`
	DurationMS      int64       `json:"duration_ms"`
	Packages        []string    `json:"packages"`
	Edges           [][2]string `json:"edges"`
	MissingPackages []string    `json:"missing_packages,omitempty"`
	ExtraPackages   []string    `json:"extra_packages,omitempty"`
	MissingEdges    []string    `json:"missing_edges,omitempty"`
	ExtraEdges      []string    `json:"extra_edges,omitempty"`
	TargetMatched   bool        `json:"target_matched,omitempty"`
}

type phase6BenchResult struct {
	Metadata struct {
		BenchmarkRevision string    `json:"benchmark_revision"`
		Mode              string    `json:"mode"`
		Model             string    `json:"model"`
		GeneratedAt       time.Time `json:"generated_at"`
		FixtureFiles      int       `json:"fixture_files"`
		FixtureGoFiles    int       `json:"fixture_go_files"`
		TaskHash          string    `json:"task_hash"`
		StaleProbe        bool      `json:"stale_probe"`
		Notes             string    `json:"notes"`
	} `json:"metadata"`
	Tasks []phase6BenchTask `json:"tasks"`
	Runs  []phase6BenchRun  `json:"runs"`
}

const phase6Module = "example.com/bench"

func phase6BenchTasks() []phase6BenchTask {
	return []phase6BenchTask{
		{
			ID: "A-direct-symbol", Category: "A_DIRECT_SYMBOL",
			Prompt: "Find the declaration of NewAPI in package internal/api. Report its path, symbol, and line range.",
			Kind:   "symbol", Package: "api", Depth: 1, ExpectedStatus: "ok",
			Target: &phase6BenchTarget{Path: "internal/api/api.go", Symbol: "NewAPI"},
		},
		{
			ID: "B-direct-imports", Category: "B_DEPENDENCY",
			Prompt: "Which packages does internal/api import directly inside this module?",
			Kind:   "imports", Package: "internal/api", Depth: 2, ExpectedStatus: "ok",
			ExpectedPkgs:  []string{"internal/model", "internal/store"},
			ExpectedEdges: [][2]string{{"internal/api", "internal/model"}, {"internal/api", "internal/store"}},
			CheckEdges:    true,
		},
		{
			ID: "C-reverse-imports", Category: "C_REVERSE_DEPENDENCY",
			Prompt: "Which packages import internal/model directly?",
			Kind:   "imported_by", Package: "internal/model", Depth: 1, ExpectedStatus: "ok",
			ExpectedPkgs: []string{"internal/api", "internal/legacy", "internal/store"},
			ExpectedEdges: [][2]string{
				{"internal/api", "internal/model"},
				{"internal/legacy", "internal/model"},
				{"internal/store", "internal/model"},
			},
			CheckEdges: true,
		},
		{
			ID: "D-change-impact", Category: "D_CHANGE_IMPACT",
			Prompt: "If internal/model changes, which packages are candidate affected areas through transitive imports?",
			Kind:   "impact", Package: "internal/model", Depth: 4, ExpectedStatus: "ok",
			ExpectedPkgs: []string{
				"cmd/app", "internal/api", "internal/legacy", "internal/store", "internal/worker",
			},
			ExpectedEdges: [][2]string{
				{"cmd/app", "internal/api"},
				{"cmd/app", "internal/store"},
				{"internal/api", "internal/model"},
				{"internal/api", "internal/store"},
				{"internal/legacy", "internal/model"},
				{"internal/store", "internal/model"},
				{"internal/worker", "internal/api"},
			},
		},
		{
			ID: "E-verification-targets", Category: "E_VERIFICATION_TARGETING",
			Prompt: "After a change to internal/model, which package scopes should be considered for verification?",
			Kind:   "verify", Package: "internal/model", Depth: 4, ExpectedStatus: "ok",
			ExpectedPkgs: []string{
				"cmd/app", "internal/api", "internal/legacy", "internal/store", "internal/worker",
			},
			ExpectedEdges: [][2]string{
				{"cmd/app", "internal/api"},
				{"cmd/app", "internal/store"},
				{"internal/api", "internal/model"},
				{"internal/api", "internal/store"},
				{"internal/legacy", "internal/model"},
				{"internal/store", "internal/model"},
				{"internal/worker", "internal/api"},
			},
		},
		{
			ID: "F-multihop", Category: "F_MULTIHOP",
			Prompt: "Starting at cmd/app, which packages are reachable within two imports and which edges connect them?",
			Kind:   "reach", Package: "cmd/app", Depth: 2, ExpectedStatus: "ok",
			ExpectedPkgs: []string{"internal/api", "internal/model", "internal/store"},
			ExpectedEdges: [][2]string{
				{"cmd/app", "internal/api"},
				{"cmd/app", "internal/store"},
				{"internal/api", "internal/model"},
				{"internal/api", "internal/store"},
				{"internal/store", "internal/model"},
			},
			CheckEdges: true,
		},
		{
			ID: "G-negative-relationship", Category: "G_NEGATIVE",
			Prompt: "Does internal/isolated import internal/model? Report the package relationship only if it exists.",
			Kind:   "negative", Package: "internal/isolated", Depth: 1, ExpectedStatus: "ok",
			ExpectedPkgs: []string{},
		},
		{
			ID: "H-ambiguous-package", Category: "H_AMBIGUOUS",
			Prompt: "Resolve the package named same. If more than one directory has that package name, preserve the ambiguity.",
			Kind:   "ambiguous", Package: "same", Depth: 1, ExpectedStatus: "ambiguous",
			ExpectedPkgs: []string{"dup/one", "dup/two"},
		},
	}
}

func TestPhase6Benchmark(t *testing.T) {
	if os.Getenv("OHGO_PHASE6_BENCH") != "1" {
		t.Skip("set OHGO_PHASE6_BENCH=1 to run the Phase 6 discovery benchmark")
	}
	root := phase6BenchFixture(t)
	tasks := phase6BenchTasks()

	h0 := newPhase6Runner(root, false)
	h1 := newPhase6Runner(root, true)
	// Warm both structural sources so the measured discovery work is not a
	// one-time index build. Cold index cost is measured separately.
	for _, runner := range []*phase6Runner{h0, h1} {
		runner.execute("symbols", map[string]any{"symbol": "NewAPI", "package": "api"})
		runner.resetMetrics()
	}

	var runs []phase6BenchRun
	for _, task := range tasks {
		run0 := phase6RunTask(t, h0, task, "H0-existing")
		run1 := phase6RunTask(t, h1, task, "H1-graph")
		runs = append(runs, run0, run1)
	}

	stale := phase6StaleProbe(t, root)

	result := phase6BenchResult{Tasks: tasks, Runs: runs}
	result.Metadata.BenchmarkRevision = "phase6-v1"
	result.Metadata.Mode = "deterministic-static-tool-probes"
	result.Metadata.Model = "none (tool-level capability probe; no provider calls)"
	result.Metadata.GeneratedAt = time.Now().UTC()
	result.Metadata.TaskHash = phase6TaskHash(tasks)
	result.Metadata.StaleProbe = stale
	files, goFiles := phase6CountFixture(t, root)
	result.Metadata.FixtureFiles = files
	result.Metadata.FixtureGoFiles = goFiles
	result.Metadata.Notes = "H0 uses read/glob/grep/ls/symbols; H1 uses the same tools plus graph. " +
		"Both plans are the minimal correct query plan for the task, not a model transcript. " +
		"Lexical decoys prove whether a plan reports relationships it cannot prove."

	path := filepath.Join("..", "..", "docs", "benchmarks", "results", "phase6-graph.json")
	if err := writePhase6JSON(path, result); err != nil {
		t.Fatalf("write result: %v", err)
	}

	// H1 must answer every task. H0 must fail at least the negative task:
	// a string literal that mentions a module path is not an import edge.
	for _, run := range runs {
		if run.Track == "H1-graph" && !run.Success {
			t.Errorf("%s H1 failed: %s", run.TaskID, run.Outcome)
		}
		if run.TaskID == "G-negative-relationship" && run.Track == "H0-existing" && run.Success {
			t.Errorf("H0 reported a relationship from a lexical decoy")
		}
	}
	// H1 must not invent an edge the graph cannot prove.
	for _, run := range runs {
		if run.Track == "H1-graph" && len(run.ExtraEdges) > 0 {
			t.Errorf("%s H1 invented edges: %v", run.TaskID, run.ExtraEdges)
		}
	}
	// The transitive impact probes should cost less context through the graph
	// than through a full lexical import scan.
	d0 := findRun(runs, "D-change-impact", "H0-existing")
	d1 := findRun(runs, "D-change-impact", "H1-graph")
	if d0.ContextBytes <= d1.ContextBytes {
		t.Errorf("impact context: H0=%d H1=%d; graph did not reduce the lexical scan", d0.ContextBytes, d1.ContextBytes)
	}
	if !stale {
		t.Error("the stale-edge probe failed")
	}
	t.Logf("wrote %s (%d runs)", path, len(runs))
}

// phase6BenchFixture writes a module with a known relationship graph, test
// imports, an unresolved import, and lexical decoys that a grep-only plan
// cannot distinguish from real import declarations.
func phase6BenchFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"go.mod": "module " + phase6Module + "\n\ngo 1.21\n",
		"cmd/app/app.go": `package main

import (
	"example.com/bench/internal/api"
	"example.com/bench/internal/store"
)

func main() {
	_ = api.NewAPI()
	_ = store.NewStore()
}
`,
		"cmd/app/app_test.go": `package main

import "example.com/bench/internal/api"

var _ = api.NewAPI
`,
		"internal/api/api.go": `package api

import (
	"example.com/bench/internal/model"
	"example.com/bench/internal/store"
)

func NewAPI() *model.Model {
	_ = store.NewStore()
	return nil
}
`,
		"internal/api/api_test.go": `package api

import "example.com/bench/internal/store"

var _ = store.NewStore
`,
		"internal/store/store.go": `package store

import "example.com/bench/internal/model"

func NewStore() *model.Model { return nil }
`,
		"internal/model/model.go": `package model

type Model struct{}
`,
		"internal/worker/worker.go": `package worker

import "example.com/bench/internal/api"

func Work() { _ = api.NewAPI() }
`,
		"internal/legacy/legacy.go": `package legacy

import "example.com/bench/internal/model"

func Old() *model.Model { return nil }
`,
		"internal/isolated/iso.go": `package isolated

// This string is not an import. A lexical scan that matches the module path
// will report a relationship that does not exist.
const decoy = "example.com/bench/internal/model"

func Isolated() {}
`,
		"internal/ghost/ghost.go": `package ghost

import "example.com/bench/internal/missing"
`,
		"dup/one/one.go": `package same

func One() {}
`,
		"dup/two/two.go": `package same

func Two() {}
`,
	}
	// Filler packages make a full import scan materially larger than the
	// neighbourhood a graph query returns.
	for i := 0; i < 40; i++ {
		next := ""
		if i+1 < 40 {
			next = fmt.Sprintf("import \"%s/filler/p%03d\"\n", phase6Module, i+1)
		}
		files[fmt.Sprintf("filler/p%03d/f.go", i)] = fmt.Sprintf("package p%03d\n\n%s\nfunc F%d() {}\n", i, next, i)
	}
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func phase6CountFixture(t *testing.T, root string) (int, int) {
	t.Helper()
	total, goFiles := 0, 0
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		total++
		if strings.HasSuffix(path, ".go") {
			goFiles++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return total, goFiles
}

// phase6Runner owns one track's tools and records the model-visible cost.
type phase6Runner struct {
	root  string
	tools map[string]agent.Tool
	// graph is nil for H0.
	graph *tools.GraphTool

	calls       int
	graphCalls  int
	bytes       int
	lines       int
	filesOpened int
	start       time.Time
}

func newPhase6Runner(root string, withGraph bool) *phase6Runner {
	r := &phase6Runner{root: root, tools: map[string]agent.Tool{}}
	set := []agent.Tool{
		tools.NewReadTool(root),
		tools.NewGlobTool(root),
		tools.NewGrepTool(root),
		tools.NewLsTool(root),
	}
	if withGraph {
		source := tools.NewStructuralSource(root)
		set = append(set, tools.NewSymbolsToolWithSource(source), tools.NewGraphTool(source))
		r.graph = set[len(set)-1].(*tools.GraphTool)
	} else {
		set = append(set, tools.NewSymbolsTool(root))
	}
	for _, tool := range set {
		r.tools[tool.Name()] = tool
	}
	return r
}

func (r *phase6Runner) resetMetrics() {
	r.calls, r.graphCalls, r.bytes, r.lines, r.filesOpened = 0, 0, 0, 0, 0
	r.start = time.Now()
}

func (r *phase6Runner) execute(name string, args map[string]any) agent.Result {
	return r.executeCtx(context.Background(), name, args)
}

func (r *phase6Runner) executeCtx(ctx context.Context, name string, args map[string]any) agent.Result {
	if r.start.IsZero() {
		r.start = time.Now()
	}
	r.calls++
	if name == "graph" {
		r.graphCalls++
	}
	if name == "read" {
		r.filesOpened++
	}
	result := r.tools[name].Execute(agent.Context{Ctx: ctx}, args)
	r.bytes += len(result.Text)
	r.lines += strings.Count(result.Text, "\n") + 1
	return result
}

func phase6RunTask(t *testing.T, runner *phase6Runner, task phase6BenchTask, track string) phase6BenchRun {
	t.Helper()
	runner.resetMetrics()
	var answer phase6BenchAnswer
	if track == "H0-existing" {
		answer = phase6H0Answer(t, runner, task)
	} else {
		answer = phase6H1Answer(t, runner, task)
	}
	run := phase6BenchRun{
		TaskID:       task.ID,
		Category:     task.Category,
		Track:        track,
		Status:       answer.Status,
		ToolCalls:    runner.calls,
		GraphQueries: runner.graphCalls,
		ContextBytes: runner.bytes,
		ResultLines:  runner.lines,
		FilesOpened:  runner.filesOpened,
		DurationMS:   time.Since(runner.start).Milliseconds(),
		Packages:     answer.Packages,
		Edges:        answer.Edges,
	}
	if answer.Target != nil {
		run.TargetMatched = phase6TargetMatches(task.Target, answer.Target)
	}
	phase6ScoreAnswer(&run, task, answer)
	return run
}

func phase6H0Answer(t *testing.T, runner *phase6Runner, task phase6BenchTask) phase6BenchAnswer {
	t.Helper()
	switch task.Kind {
	case "symbol":
		result := runner.execute("symbols", map[string]any{"symbol": "NewAPI", "package": "api"})
		return phase6AnswerFromSymbols(t, result, "NewAPI")
	case "imports":
		result := runner.execute("grep", map[string]any{
			"pattern": regexp.QuoteMeta(phase6Module + "/"),
			"path":    task.Package,
		})
		edges := phase6ParseGrepImports(result.Text, task.Package)
		return phase6AnswerFromEdges(edges, task.Package, "from")
	case "imported_by":
		result := runner.execute("grep", map[string]any{
			"pattern": regexp.QuoteMeta(phase6Module + "/" + task.Package),
			"path":    ".",
		})
		edges := phase6ParseGrepImporters(result.Text, task.Package)
		return phase6AnswerFromEdges(edges, task.Package, "to")
	case "impact", "verify":
		result := runner.execute("grep", map[string]any{
			"pattern": regexp.QuoteMeta(phase6Module + "/"),
			"path":    ".",
			"limit":   100.0,
		})
		edges := phase6ParseGrepImports(result.Text, ".")
		return phase6AnswerFromClosure(edges, task.Package, task.Depth, true)
	case "reach":
		result := runner.execute("grep", map[string]any{
			"pattern": regexp.QuoteMeta(phase6Module + "/"),
			"path":    ".",
			"limit":   100.0,
		})
		edges := phase6ParseGrepImports(result.Text, ".")
		return phase6AnswerFromClosure(edges, task.Package, task.Depth, false)
	case "negative":
		result := runner.execute("grep", map[string]any{
			"pattern": regexp.QuoteMeta(phase6Module + "/internal/model"),
			"path":    task.Package,
		})
		edges := phase6ParseGrepImporters(result.Text, "internal/model")
		return phase6AnswerFromEdges(edges, "", "to")
	case "ambiguous":
		result := runner.execute("symbols", map[string]any{"package": "same"})
		details, _ := result.Details.(map[string]any)
		coords, _ := details["coordinates"].([]map[string]any)
		dirs := map[string]bool{}
		for _, coord := range coords {
			if path, ok := coord["path"].(string); ok {
				dirs[filepath.ToSlash(filepath.Dir(path))] = true
			}
		}
		packages := sortedStringSet(dirs)
		status := "ok"
		if len(packages) > 1 {
			status = "ambiguous"
		}
		return phase6BenchAnswer{Status: status, Packages: packages}
	}
	t.Fatalf("unknown H0 task kind %q", task.Kind)
	return phase6BenchAnswer{}
}

func phase6H1Answer(t *testing.T, runner *phase6Runner, task phase6BenchTask) phase6BenchAnswer {
	t.Helper()
	switch task.Kind {
	case "symbol":
		result := runner.execute("symbols", map[string]any{"symbol": "NewAPI", "package": "api"})
		return phase6AnswerFromSymbols(t, result, "NewAPI")
	case "imports":
		return phase6AnswerFromGraph(t, runner, "path", task.Package, "imports", 2, "from")
	case "imported_by":
		return phase6AnswerFromGraph(t, runner, "path", task.Package, "imported_by", 1, "to")
	case "impact", "verify":
		return phase6AnswerFromGraph(t, runner, "path", task.Package, "imported_by", task.Depth, "")
	case "reach":
		return phase6AnswerFromGraph(t, runner, "path", task.Package, "imports", task.Depth, "")
	case "negative":
		return phase6AnswerFromGraph(t, runner, "path", task.Package, "imports", 1, "")
	case "ambiguous":
		return phase6AnswerFromGraph(t, runner, "package", task.Package, "imports", 1, "")
	}
	t.Fatalf("unknown H1 task kind %q", task.Kind)
	return phase6BenchAnswer{}
}

func phase6AnswerFromGraph(t *testing.T, runner *phase6Runner, selector, pkg, relation string, depth int, focalEdgeSide string) phase6BenchAnswer {
	t.Helper()
	args := map[string]any{
		"relation": relation,
		"depth":    float64(depth),
		"limit":    100.0,
	}
	args[selector] = pkg
	result := runner.execute("graph", args)
	if result.IsError {
		return phase6BenchAnswer{Status: "error"}
	}
	details, _ := result.Details.(map[string]any)
	status, _ := details["status"].(string)
	if status == "ambiguous" {
		candidates, _ := details["candidates"].([]string)
		return phase6BenchAnswer{Status: "ambiguous", Packages: candidates}
	}
	if status == "not_found" {
		return phase6BenchAnswer{Status: "not_found"}
	}
	nodes, _ := details["nodes"].([]map[string]any)
	packages := make([]string, 0, len(nodes))
	for _, node := range nodes {
		if path, ok := node["path"].(string); ok && path != pkg {
			packages = append(packages, path)
		}
	}
	edgeMaps, _ := details["edges"].([]map[string]any)
	var edges [][2]string
	for _, edge := range edgeMaps {
		from, _ := edge["from"].(string)
		to, _ := edge["to"].(string)
		switch focalEdgeSide {
		case "from":
			if from != pkg {
				continue
			}
		case "to":
			if to != pkg {
				continue
			}
		}
		edges = append(edges, [2]string{from, to})
	}
	return phase6BenchAnswer{
		Status:   status,
		Packages: sortedStringSet(sliceSet(packages)),
		Edges:    sortedEdges(edges),
	}
}

func phase6AnswerFromSymbols(t *testing.T, result agent.Result, symbol string) phase6BenchAnswer {
	t.Helper()
	if result.IsError {
		return phase6BenchAnswer{Status: "error"}
	}
	details, _ := result.Details.(map[string]any)
	coords, _ := details["coordinates"].([]map[string]any)
	for _, coord := range coords {
		name, _ := coord["symbol"].(string)
		if name != symbol {
			continue
		}
		path, _ := coord["path"].(string)
		start, _ := coord["start_line"].(int)
		end, _ := coord["end_line"].(int)
		return phase6BenchAnswer{
			Status: "ok",
			Target: &phase6BenchTarget{Path: path, Symbol: name, StartLine: start, EndLine: end},
		}
	}
	return phase6BenchAnswer{Status: "not_found"}
}

// phase6ParseGrepImports reads grep output and returns real-looking import
// edges. It deliberately does not distinguish a string literal from an import
// declaration: that is the lexical baseline's actual limitation.
func phase6ParseGrepImports(text, scopedDir string) [][2]string {
	matches := phase6ParseGrepMatches(text)
	edges := map[string]bool{}
	for _, match := range matches {
		from := filepath.ToSlash(filepath.Dir(match.file))
		if from == "." && scopedDir != "." {
			continue
		}
		for _, importPath := range phase6QuotedStrings(match.text) {
			if !strings.HasPrefix(importPath, phase6Module+"/") {
				continue
			}
			to := strings.TrimPrefix(importPath, phase6Module+"/")
			edges[from+"\x00"+to] = true
		}
	}
	return phase6EdgePairs(edges)
}

// phase6ParseGrepImporters treats any line containing the import path as a
// consumer. A string literal or comment therefore produces a false edge.
func phase6ParseGrepImporters(text, target string) [][2]string {
	matches := phase6ParseGrepMatches(text)
	edges := map[string]bool{}
	for _, match := range matches {
		from := filepath.ToSlash(filepath.Dir(match.file))
		edges[from+"\x00"+target] = true
	}
	return phase6EdgePairs(edges)
}

type phase6GrepMatch struct {
	file string
	line int
	text string
}

func phase6ParseGrepMatches(text string) []phase6GrepMatch {
	var out []phase6GrepMatch
	current := ""
	for _, line := range strings.Split(text, "\n") {
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, " ") && strings.HasSuffix(line, ":") {
			current = strings.TrimSuffix(line, ":")
			continue
		}
		if current == "" || !strings.HasPrefix(line, "  ") {
			continue
		}
		rest := strings.TrimPrefix(line, "  ")
		colon := strings.Index(rest, ": ")
		if colon < 0 {
			continue
		}
		var number int
		if _, err := fmt.Sscanf(rest[:colon], "%d", &number); err != nil {
			continue
		}
		out = append(out, phase6GrepMatch{file: current, line: number, text: rest[colon+2:]})
	}
	return out
}

func phase6QuotedStrings(text string) []string {
	var out []string
	for {
		start := strings.IndexByte(text, '"')
		if start < 0 {
			break
		}
		rest := text[start+1:]
		end := strings.IndexByte(rest, '"')
		if end < 0 {
			break
		}
		out = append(out, rest[:end])
		text = rest[end+1:]
	}
	return out
}

func phase6AnswerFromEdges(edges [][2]string, focal, side string) phase6BenchAnswer {
	packages := map[string]bool{}
	for _, edge := range edges {
		switch side {
		case "from":
			if edge[0] != focal {
				continue
			}
			packages[edge[1]] = true
		case "to":
			if edge[1] != focal && focal != "" {
				continue
			}
			packages[edge[0]] = true
		default:
			packages[edge[0]] = true
			packages[edge[1]] = true
		}
	}
	return phase6BenchAnswer{Status: "ok", Packages: sortedStringSet(packages), Edges: sortedEdges(edges)}
}

func phase6AnswerFromClosure(edges [][2]string, start string, depth int, reverse bool) phase6BenchAnswer {
	forward := map[string][]string{}
	backward := map[string][]string{}
	for _, edge := range edges {
		forward[edge[0]] = append(forward[edge[0]], edge[1])
		backward[edge[1]] = append(backward[edge[1]], edge[0])
	}
	next := forward
	if reverse {
		next = backward
	}
	visited := map[string]bool{start: true}
	depthOf := map[string]int{start: 0}
	queue := []string{start}
	for len(queue) > 0 {
		from := queue[0]
		queue = queue[1:]
		if depthOf[from] >= depth {
			continue
		}
		for _, to := range next[from] {
			if visited[to] {
				continue
			}
			visited[to] = true
			depthOf[to] = depthOf[from] + 1
			queue = append(queue, to)
		}
	}
	var pkgs []string
	for pkg := range visited {
		if pkg != start {
			pkgs = append(pkgs, pkg)
		}
	}
	sort.Strings(pkgs)
	var kept [][2]string
	for _, edge := range edges {
		if visited[edge[0]] && visited[edge[1]] {
			kept = append(kept, edge)
		}
	}
	return phase6BenchAnswer{
		Status:   "ok",
		Packages: pkgs,
		Edges:    sortedEdges(kept),
	}
}

func phase6ScoreAnswer(run *phase6BenchRun, task phase6BenchTask, answer phase6BenchAnswer) {
	statusOK := run.Status == task.ExpectedStatus
	if task.ExpectedStatus == "ok" && (run.Status == "ok" || run.Status == "truncated") {
		statusOK = true
	}
	if task.Kind == "symbol" {
		run.Success = statusOK && run.TargetMatched
		if !run.TargetMatched {
			run.Outcome = "wrong_target"
		} else if !statusOK {
			run.Outcome = "status_mismatch"
		} else {
			run.Outcome = "correct"
		}
		return
	}

	expected := sliceSet(task.ExpectedPkgs)
	actual := sliceSet(answer.Packages)
	for pkg := range expected {
		if !actual[pkg] {
			run.MissingPackages = append(run.MissingPackages, pkg)
		}
	}
	for pkg := range actual {
		if !expected[pkg] {
			run.ExtraPackages = append(run.ExtraPackages, pkg)
		}
	}
	sort.Strings(run.MissingPackages)
	sort.Strings(run.ExtraPackages)

	expectedEdge := map[string]bool{}
	for _, edge := range task.ExpectedEdges {
		expectedEdge[edge[0]+"\x00"+edge[1]] = true
	}
	actualEdge := map[string]bool{}
	for _, edge := range answer.Edges {
		actualEdge[edge[0]+"\x00"+edge[1]] = true
	}
	for edge := range expectedEdge {
		if !actualEdge[edge] {
			run.MissingEdges = append(run.MissingEdges, strings.ReplaceAll(edge, "\x00", "->"))
		}
	}
	for edge := range actualEdge {
		if !expectedEdge[edge] {
			run.ExtraEdges = append(run.ExtraEdges, strings.ReplaceAll(edge, "\x00", "->"))
		}
	}
	sort.Strings(run.MissingEdges)
	sort.Strings(run.ExtraEdges)

	switch {
	case !statusOK:
		run.Outcome = "status_mismatch"
	case len(run.MissingPackages) > 0 && len(run.ExtraPackages) > 0:
		run.Outcome = "wrong_packages"
	case len(run.MissingPackages) > 0:
		run.Outcome = "false_negative"
	case len(run.ExtraPackages) > 0:
		run.Outcome = "false_positive"
	case task.CheckEdges && (len(run.MissingEdges) > 0 || len(run.ExtraEdges) > 0):
		run.Outcome = "wrong_edges"
	default:
		run.Outcome = "correct"
		run.Success = true
	}
}

func phase6TargetMatches(expected *phase6BenchTarget, actual *phase6BenchTarget) bool {
	if expected == nil || actual == nil {
		return false
	}
	if expected.Path != actual.Path || expected.Symbol != actual.Symbol {
		return false
	}
	if expected.StartLine > 0 && (actual.StartLine < expected.StartLine-2 || actual.StartLine > expected.StartLine+2) {
		return false
	}
	return true
}

func phase6StaleProbe(t *testing.T, root string) bool {
	t.Helper()
	source := tools.NewStructuralSource(root)
	graph := tools.NewGraphTool(source)
	before := graph.Execute(agent.Context{Ctx: context.Background()}, map[string]any{
		"path": "internal/worker", "relation": "imports", "depth": 1.0,
	})
	if before.IsError || !strings.Contains(before.Text, "to: internal/api") {
		return false
	}
	path := filepath.Join(root, "internal", "worker", "worker.go")
	if err := os.WriteFile(path, []byte("package worker\n\nimport \"example.com/bench/internal/model\"\n"), 0o644); err != nil {
		return false
	}
	after := graph.Execute(agent.Context{Ctx: context.Background()}, map[string]any{
		"path": "internal/worker", "relation": "imports", "depth": 1.0,
	})
	if after.IsError {
		return false
	}
	return strings.Contains(after.Text, "to: internal/model") && !strings.Contains(after.Text, "to: internal/api")
}

func phase6TaskHash(tasks []phase6BenchTask) string {
	data, _ := json.Marshal(tasks)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func phase6EdgePairs(edges map[string]bool) [][2]string {
	out := make([][2]string, 0, len(edges))
	for edge := range edges {
		parts := strings.SplitN(edge, "\x00", 2)
		out = append(out, [2]string{parts[0], parts[1]})
	}
	return sortedEdges(out)
}

func sortedEdges(edges [][2]string) [][2]string {
	sort.Slice(edges, func(i, j int) bool {
		if edges[i][0] != edges[j][0] {
			return edges[i][0] < edges[j][0]
		}
		return edges[i][1] < edges[j][1]
	})
	return edges
}

func sortedStringSet(values map[string]bool) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func sliceSet(values []string) map[string]bool {
	out := map[string]bool{}
	for _, value := range values {
		out[value] = true
	}
	return out
}

func findRun(runs []phase6BenchRun, taskID, track string) phase6BenchRun {
	for _, run := range runs {
		if run.TaskID == taskID && run.Track == track {
			return run
		}
	}
	return phase6BenchRun{}
}

func writePhase6JSON(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}
