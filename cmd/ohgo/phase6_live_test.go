package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/ai"
	"github.com/PizenLabs/ohgo/budget"
	"github.com/PizenLabs/ohgo/workspace"
)

const phase6LivePrompt = `You are answering a repository-discovery benchmark about a read-only Go module.
The repository is not in your prior knowledge: you MUST call at least one provided tool
before answering. Use only the tools provided. Do not modify files, run commands, or use the network.

When you have the answer, end with exactly one line:
FINAL: {"status":"ok|ambiguous|not_found","packages":["internal/x"],"edges":[["internal/a","internal/b"]],"targets":[{"path":"repository/relative/path","symbol":"Name","start_line":1,"end_line":2}]}

Rules:
- package paths are module-relative directories, excluding the focal package.
- For a direct import question, report the direct imported packages and the edges from the focal package.
- For a reverse-import question, report the importing packages and the edges into the focal package.
- For an impact or verification question, report the transitive importing packages.
- For an ambiguous package name, use status "ambiguous" and list every candidate directory in packages.
- For a missing package or symbol, use status "not_found".
- For a negative relationship, use status "ok" with empty packages and edges.
- Report only relationships you verified. Do not guess.`

// phase6RecordingTool records one model-initiated tool call through the same
// runner used by the static benchmark.
type phase6RecordingTool struct {
	agent.Tool
	runner *phase6Runner
}

func (t *phase6RecordingTool) Execute(ctx agent.Context, args map[string]any) agent.Result {
	return t.runner.executeCtx(ctx.Ctx, t.Tool.Name(), args)
}

// phase6LiveToolSet exposes the tools in a stable order for both tracks.
func (r *phase6Runner) liveToolSet() []agent.Tool {
	names := []string{"read", "glob", "grep", "ls", "symbols"}
	if r.graph != nil {
		names = append(names, "graph")
	}
	out := make([]agent.Tool, 0, len(names))
	for _, name := range names {
		if tool, ok := r.tools[name]; ok {
			out = append(out, &phase6RecordingTool{Tool: tool, runner: r})
		}
	}
	return out
}

func TestPhase6LiveBenchmark(t *testing.T) {
	if os.Getenv("OHGO_PHASE6_LIVE") != "1" {
		t.Skip("set OHGO_PHASE6_LIVE=1 to run the live Phase 6 discovery benchmark")
	}

	root := phase6BenchFixture(t)
	before, err := phase6FixtureDigest(root)
	if err != nil {
		t.Fatal(err)
	}
	tasks := phase6SelectLiveTasks(t, phase6BenchTasks())
	repeats := 1
	if raw := os.Getenv("OHGO_PHASE6_REPEATS"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			repeats = parsed
		}
	}

	model, stream, modelName := phase6LiveModel(t)
	h0 := newPhase6Runner(root, false)
	h1 := newPhase6Runner(root, true)
	for _, runner := range []*phase6Runner{h0, h1} {
		runner.execute("symbols", map[string]any{"symbol": "NewAPI", "package": "api"})
		runner.resetMetrics()
	}

	var runs []phase6BenchRun
	for repeat := 0; repeat < repeats; repeat++ {
		for _, task := range tasks {
			runs = append(runs, phase6LiveRunTask(t, root, model, stream, h0, task, "H0-live"))
			runs = append(runs, phase6LiveRunTask(t, root, model, stream, h1, task, "H1-live"))
		}
	}

	after, err := phase6FixtureDigest(root)
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Errorf("the read-only benchmark mutated the fixture")
	}

	result := phase6BenchResult{Tasks: tasks, Runs: runs}
	result.Metadata.BenchmarkRevision = "phase6-live-v1"
	result.Metadata.Mode = "live-agent-discovery"
	result.Metadata.Model = modelName
	result.Metadata.GeneratedAt = time.Now().UTC()
	result.Metadata.TaskHash = phase6TaskHash(tasks)
	result.Metadata.StaleProbe = true
	result.Metadata.FixtureFiles, result.Metadata.FixtureGoFiles = phase6CountFixture(t, root)
	result.Metadata.Notes = "Same model, system prompt, task order, turn/tool budgets, and read-only authority for both tracks. " +
		"H0 tools: read/glob/grep/ls/symbols. H1 adds graph."

	path := filepath.Join("..", "..", "docs", "benchmarks", "results", "phase6-graph-live.json")
	if err := writePhase6JSON(path, result); err != nil {
		t.Fatalf("write result: %v", err)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "%-30s %-10s %-6s %-5s %-5s %-6s %-8s %-8s\n",
		"task", "track", "ok", "turn", "calls", "graph", "ctx", "answer")
	for _, run := range runs {
		fmt.Fprintf(&sb, "%-30s %-10s %-6v %-5d %-5d %-6d %-8d %-8s\n",
			run.TaskID, run.Track, run.Success, run.Turns, run.ToolCalls, run.GraphQueries, run.ContextBytes, run.Status)
	}
	t.Logf("wrote %s\n%s", path, sb.String())
}

func phase6LiveModel(t *testing.T) (ai.Model, ai.StreamFn, string) {
	t.Helper()
	provider := phase6Env("OHGO_PHASE6_PROVIDER", "ollama")
	id := phase6Env("OHGO_PHASE6_MODEL", "qwen2.5-coder:7b")
	apiName := phase6Env("OHGO_PHASE6_API", string(ai.APIOllamaChat))
	baseURL := phase6Env("OHGO_PHASE6_BASE_URL", "http://localhost:11434")
	model := ai.Model{
		ID:              id,
		Name:            id,
		Provider:        provider,
		API:             ai.API(apiName),
		BaseURL:         baseURL,
		ContextWindow:   32768,
		MaxOutputTokens: 1024,
	}
	registry := ai.NewRegistry()
	if keyEnv := strings.TrimSpace(os.Getenv("OHGO_PHASE6_KEY_ENV")); keyEnv != "" {
		if key := strings.TrimSpace(os.Getenv(keyEnv)); key != "" {
			registry.SetAPIKey(provider, key)
		}
	}
	base := registry.StreamFn()
	zero := 0.0
	stream := func(ctx context.Context, req ai.Request) (*ai.Stream, error) {
		req.Temperature = &zero
		return base(ctx, req)
	}
	return model, stream, provider + "/" + id
}

func phase6Env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func phase6SelectLiveTasks(t *testing.T, tasks []phase6BenchTask) []phase6BenchTask {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv("OHGO_PHASE6_TASKS"))
	if raw == "" {
		return tasks
	}
	wanted := map[string]bool{}
	for _, id := range strings.Split(raw, ",") {
		wanted[strings.TrimSpace(id)] = true
	}
	var out []phase6BenchTask
	for _, task := range tasks {
		if wanted[task.ID] {
			out = append(out, task)
			delete(wanted, task.ID)
		}
	}
	if len(wanted) > 0 {
		var missing []string
		for id := range wanted {
			missing = append(missing, id)
		}
		sort.Strings(missing)
		t.Fatalf("unknown Phase 6 task ids: %s", strings.Join(missing, ", "))
	}
	return out
}

func phase6LiveRunTask(t *testing.T, root string, model ai.Model, stream ai.StreamFn, runner *phase6Runner, task phase6BenchTask, track string) phase6BenchRun {
	t.Helper()
	runner.resetMetrics()
	loop, err := agent.New(agent.Config{
		Model:     model,
		Stream:    stream,
		System:    []ai.SystemPromptBlock{{Text: phase6LivePrompt, Cache: true}},
		Tools:     runner.liveToolSet(),
		MaxTokens: 1024,
		MaxTurns:  8,
		Budget:    budget.New(budget.Limits{Turns: 8, ModelCalls: 9, ToolCalls: 24}),
		Authorize: phase6Gate(root, workspace.Ask),
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	start := time.Now()
	turns, runErr := loop.Run(ctx, []agent.Message{agent.UserMessage{Text: task.Prompt}})
	duration := time.Since(start)

	finalText := phase6TurnsText(turns)
	answer, format := phase6ParseLiveAnswer(finalText)
	run := phase6BenchRun{
		TaskID:       task.ID,
		Category:     task.Category,
		Track:        track,
		Status:       answer.Status,
		Turns:        len(turns),
		ModelCalls:   len(turns),
		ToolCalls:    runner.calls,
		GraphQueries: runner.graphCalls,
		ContextBytes: runner.bytes,
		ResultLines:  runner.lines,
		FilesOpened:  runner.filesOpened,
		AnswerFormat: format,
		FinalText:    finalText,
		DurationMS:   duration.Milliseconds(),
		Packages:     answer.Packages,
		Edges:        answer.Edges,
	}
	for _, turn := range turns {
		run.InputTokens += turn.Assistant.Usage.Input
		run.OutputTokens += turn.Assistant.Usage.Output
	}
	if answer.Target != nil {
		run.TargetMatched = phase6TargetMatches(task.Target, answer.Target)
	}
	if runErr != nil {
		run.Error = runErr.Error()
	}
	phase6ScoreAnswer(&run, task, answer)
	return run
}

func phase6TurnsText(turns []agent.Turn) string {
	var sb strings.Builder
	for _, turn := range turns {
		for _, block := range turn.Assistant.Content {
			if text, ok := block.(ai.TextContent); ok && text.Text != "" {
				sb.WriteString(text.Text)
				sb.WriteByte('\n')
			}
		}
	}
	return strings.TrimSpace(sb.String())
}

type phase6LiveJSON struct {
	Status   string             `json:"status"`
	Packages []string           `json:"packages"`
	Edges    [][2]string        `json:"edges"`
	Targets  []phase6LiveTarget `json:"targets"`
}

type phase6LiveTarget struct {
	Path      string `json:"path"`
	File      string `json:"file"`
	Symbol    string `json:"symbol"`
	Name      string `json:"name"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Line      int    `json:"line"`
}

func phase6ParseLiveAnswer(text string) (phase6BenchAnswer, string) {
	lower := strings.ToLower(text)
	var candidates []string
	if idx := strings.LastIndex(lower, "final:"); idx >= 0 {
		candidates = append(candidates, text[idx+len("final:"):])
	}
	if idx := strings.Index(text, "{"); idx >= 0 {
		candidates = append(candidates, text[idx:])
	}
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		start := strings.Index(candidate, "{")
		if start < 0 {
			continue
		}
		var raw phase6LiveJSON
		decoder := json.NewDecoder(strings.NewReader(candidate[start:]))
		if err := decoder.Decode(&raw); err != nil {
			continue
		}
		answer := phase6BenchAnswer{
			Status:   phase6NormalizeLiveStatus(raw.Status),
			Packages: phase6NormalizePackages(raw.Packages),
			Edges:    phase6NormalizeEdges(raw.Edges),
		}
		if len(raw.Targets) > 0 {
			target := raw.Targets[0]
			path := target.Path
			if path == "" {
				path = target.File
			}
			symbol := target.Symbol
			if symbol == "" {
				symbol = target.Name
			}
			startLine := target.StartLine
			if startLine == 0 {
				startLine = target.Line
			}
			answer.Target = &phase6BenchTarget{
				Path:      phase6NormalizePackagePath(path),
				Symbol:    strings.TrimSpace(symbol),
				StartLine: startLine,
				EndLine:   target.EndLine,
			}
		}
		return answer, "json"
	}
	return phase6BenchAnswer{Status: "missing"}, "missing"
}

func phase6NormalizeLiveStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "ok", "exact", "found", "success":
		return "ok"
	case "ambiguous", "multiple":
		return "ambiguous"
	case "not_found", "not found", "missing":
		return "not_found"
	default:
		return strings.ToLower(strings.TrimSpace(status))
	}
}

func phase6NormalizePackages(values []string) []string {
	set := map[string]bool{}
	for _, value := range values {
		value = phase6NormalizePackagePath(value)
		if value != "" {
			set[value] = true
		}
	}
	return sortedStringSet(set)
}

func phase6NormalizeEdges(values [][2]string) [][2]string {
	var out [][2]string
	for _, edge := range values {
		from := phase6NormalizePackagePath(edge[0])
		to := phase6NormalizePackagePath(edge[1])
		if from == "" || to == "" {
			continue
		}
		out = append(out, [2]string{from, to})
	}
	return sortedEdges(out)
}

func phase6NormalizePackagePath(value string) string {
	value = strings.TrimSpace(strings.Trim(value, "`'\""))
	value = strings.TrimPrefix(value, "./")
	value = strings.TrimSuffix(value, "/")
	if value == "." {
		return ""
	}
	return value
}

func phase6FixtureDigest(root string) (string, error) {
	var files []string
	if err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			files = append(files, path)
		}
		return nil
	}); err != nil {
		return "", err
	}
	sort.Strings(files)
	hash := sha256.New()
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		hash.Write([]byte(path))
		hash.Write([]byte{0})
		hash.Write(data)
		hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
