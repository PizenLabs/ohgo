package main

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/execution/tools"
)

// StaticProbe is one deterministic tool-level measurement. It is not an
// agent-level run: it executes the canonical query for a task and records what
// the production tool returned. It exists so every task category has evidence
// even when a model provider is unavailable.
type StaticProbe struct {
	TaskID    string `json:"task_id"`
	Category  string `json:"category"`
	Track     string `json:"track"`
	Query     string `json:"query"`
	Status    string `json:"status"`
	Results   int    `json:"results"`
	Expected  int    `json:"expected"`
	Files     int    `json:"files"`
	Bytes     int    `json:"bytes"`
	LatencyMS int64  `json:"latency_ms"`
	// ExpectedHit reports whether the expected path (or, for a negative task,
	// the absence of a declaration) is reflected in this result.
	ExpectedHit bool   `json:"expected_hit"`
	Text        string `json:"text,omitempty"`
	Error       string `json:"error,omitempty"`
}

// StaticResult is the file written by --static.
type StaticResult struct {
	TaskHash string        `json:"task_hash"`
	Probes   []StaticProbe `json:"probes"`
}

func runStatic(root string, tasks []Task) (StaticResult, error) {
	symbols := tools.NewSymbolsTool(root)
	grep := tools.NewGrepTool(root)
	ctx := agent.Context{Ctx: context.Background()}
	var probes []StaticProbe
	for _, task := range tasks {
		probes = append(probes, structuralProbe(ctx, symbols, task))
		probes = append(probes, lexicalProbe(ctx, grep, task))
	}
	return StaticResult{Probes: probes}, nil
}

func structuralProbe(ctx agent.Context, tool *tools.SymbolsTool, task Task) StaticProbe {
	probe := StaticProbe{TaskID: task.ID, Category: task.Category, Track: "B-structural", Expected: len(task.Targets)}
	args := map[string]any{}
	switch {
	case task.ID == "B3-ambiguous-execute-methods":
		args["package"] = "tools"
		args["limit"] = 200.0
	case task.ExpectedStatus == "not_found":
		args["symbol"] = task.NegativeNames[0]
	case len(task.Targets) > 0 && task.Targets[0].Symbol != "":
		args["symbol"] = task.Targets[0].Symbol
		if len(task.Targets) > 1 {
			args["limit"] = 200.0
		}
	case len(task.Targets) > 0:
		args["path"] = task.Targets[0].Path
	default:
		args["path"] = "."
	}
	data, _ := json.Marshal(args)
	probe.Query = "symbols " + string(data)
	start := time.Now()
	result := tool.Execute(ctx, args)
	probe.LatencyMS = time.Since(start).Milliseconds()
	probe.Bytes = len(result.Text)
	probe.Text = result.Text
	if result.IsError {
		probe.Error = result.Text
		return probe
	}
	details, _ := result.Details.(map[string]any)
	probe.Status, _ = details["status"].(string)
	if n, ok := details["results"].(int); ok {
		probe.Results = n
	}
	if coords, ok := details["coordinates"].([]map[string]any); ok {
		files := map[string]bool{}
		for _, coord := range coords {
			if path, ok := coord["path"].(string); ok {
				files[path] = true
			}
		}
		probe.Files = len(files)
	}
	probe.ExpectedHit = staticHit(task, result.Text)
	return probe
}

func lexicalProbe(ctx agent.Context, tool *tools.GrepTool, task Task) StaticProbe {
	probe := StaticProbe{TaskID: task.ID, Category: task.Category, Track: "A-lexical", Expected: len(task.Targets)}
	pattern := ""
	switch {
	case task.ExpectedStatus == "not_found":
		pattern = `\b` + regexp.QuoteMeta(task.NegativeNames[0]) + `\b`
	case len(task.Targets) > 0 && task.Targets[0].Contains != "":
		pattern = regexp.QuoteMeta(task.Targets[0].Contains)
	case len(task.Targets) > 0 && task.Targets[0].Symbol != "":
		name := task.Targets[0].Symbol
		if idx := strings.LastIndexByte(name, '.'); idx >= 0 {
			name = name[idx+1:]
		}
		pattern = `\b` + regexp.QuoteMeta(name) + `\b`
	default:
		pattern = regexp.QuoteMeta(task.ID)
	}
	probe.Query = "grep " + pattern
	start := time.Now()
	result := tool.Execute(ctx, map[string]any{"pattern": pattern, "limit": 100.0})
	probe.LatencyMS = time.Since(start).Milliseconds()
	probe.Bytes = len(result.Text)
	probe.Text = result.Text
	if result.IsError {
		probe.Error = result.Text
		return probe
	}
	details, _ := result.Details.(map[string]any)
	if n, ok := details["files"].(int); ok {
		probe.Files = n
		probe.Results = n
	}
	probe.Status = "ok"
	if probe.Files == 0 {
		probe.Status = "not_found"
	}
	probe.ExpectedHit = staticHit(task, result.Text)
	return probe
}

func staticHit(task Task, text string) bool {
	if task.ExpectedStatus == "not_found" {
		return strings.Contains(text, "not_found") || strings.Contains(text, "No matches found")
	}
	for _, target := range task.Targets {
		if strings.Contains(text, target.Path) {
			return true
		}
	}
	return false
}

func summarizeStatic(probes []StaticProbe) {
	sort.SliceStable(probes, func(i, j int) bool {
		if probes[i].TaskID != probes[j].TaskID {
			return probes[i].TaskID < probes[j].TaskID
		}
		return probes[i].Track < probes[j].Track
	})
	fmt.Printf("%-42s %-13s %-11s %7s %7s %8s %9s\n",
		"task", "track", "status", "results", "files", "bytes", "ms")
	for _, probe := range probes {
		fmt.Printf("%-42s %-13s %-11s %7d %7d %8d %9d\n",
			probe.TaskID, probe.Track, probe.Status, probe.Results, probe.Files, probe.Bytes, probe.LatencyMS)
	}
}
