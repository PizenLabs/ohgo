// Command phase4 runs the Phase 4 acceptance pipeline against a real
// repository: structure, survey, drift, investigation, and repository checks.
//
// It is deterministic at the tool layer. It reports tool wall time and the
// bytes those tools would add to context; it does not invent model token usage
// because no model call is made.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/execution/tools"
	"github.com/PizenLabs/ohgo/internal/intelligence/ast"
	"github.com/PizenLabs/ohgo/internal/intelligence/evidence"
	"github.com/PizenLabs/ohgo/internal/intelligence/investigate"
	"github.com/PizenLabs/ohgo/internal/intelligence/planner"
	"github.com/PizenLabs/ohgo/internal/intelligence/retrieval"
	"github.com/PizenLabs/ohgo/internal/intelligence/structural"
)

type source struct{ index *ast.Index }

func (s *source) Inventory(ctx context.Context, q ast.InventoryQuery) (ast.Inventory, error) {
	return s.index.Inventory(ctx, q)
}

func (s *source) Dependencies(ctx context.Context, q ast.DependencyQuery) (ast.DependencyResult, error) {
	return s.index.Dependencies(ctx, q)
}

func (s *source) Impact(ctx context.Context, q ast.ImpactQuery) (ast.ImpactResult, error) {
	return s.index.Impact(ctx, q)
}

type driftFinding struct {
	Code     string
	Severity string
	Document string
	Line     int
	Target   string
	Exists   bool
}

type round struct {
	Duration time.Duration

	Files, Parsed, Pending, Stale, Packages int
	Modules, Explored, Samples              int
	Unexplored                              []string
	Hits                                    []string

	DriftFindings                       int
	DriftTrue, DriftFalse, DriftUnknown int
	Drift                               []driftFinding

	Claims, ClaimsObserved int
	FactTotal              int
	FactCurrent            int
	FactStale              int
	FactUnverified         int
	Investigations         []string

	CheckCommands int
	CheckScripted int
	CheckRefused  int
	CheckStatus   string
	CheckSummary  string
	CheckVerifies bool
	CheckScope    string
	CheckExit     int

	OutputBytes int
	Stages      []stage
}

type stage struct {
	Name string
	Time time.Duration
	Err  error
}

func main() {
	root := flag.String("root", "/Users/anvndev/Documents/Project/Golang/ecommerce", "repository root")
	rounds := flag.Int("rounds", 2, "reliability rounds")
	checks := flag.String("checks", "test,static", "check kinds to run")
	timeout := flag.Duration("timeout", 2*time.Minute, "per-check timeout")
	out := flag.String("out", "", "write the markdown report to this path")
	flag.Parse()

	if *rounds < 1 {
		*rounds = 1
	}
	var results []round
	for i := 0; i < *rounds; i++ {
		measured, err := runRound(*root, splitKinds(*checks), *timeout)
		if err != nil {
			fmt.Fprintf(os.Stderr, "round %d: %v\n", i+1, err)
			os.Exit(1)
		}
		results = append(results, measured)
	}
	report := render(*root, results)
	fmt.Print(report)
	if *out != "" {
		if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "write report: %v\n", err)
			os.Exit(1)
		}
		if err := os.WriteFile(*out, []byte(report), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "write report: %v\n", err)
			os.Exit(1)
		}
	}
}

func splitKinds(value string) []string {
	var out []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

func runRound(root string, checks []string, timeout time.Duration) (round, error) {
	ctx := context.Background()
	result := round{}
	started := time.Now()
	measure := func(name string, fn func() (int, error)) {
		begin := time.Now()
		bytes, err := fn()
		result.Stages = append(result.Stages, stage{Name: name, Time: time.Since(begin), Err: err})
		result.OutputBytes += bytes
	}
	index, err := ast.NewIndexWithOptions(root, ast.IndexOptions{Adaptive: true})
	if err != nil {
		return round{}, err
	}
	source := &source{index: index}
	analyzer := structural.NewAnalyzer(source, nil)

	var project, architecture, drift structural.Analysis
	measure("structure: project", func() (int, error) {
		var err error
		project, err = analyzer.Analyze(ctx, structural.Request{Intent: structural.IntentProject, MaxFiles: 4000})
		return analysisBytes(project), err
	})
	measure("structure: architecture", func() (int, error) {
		var err error
		architecture, err = analyzer.Analyze(ctx, structural.Request{Intent: structural.IntentArchitecture, MaxFiles: 4000})
		return analysisBytes(architecture), err
	})
	measure("structure: drift", func() (int, error) {
		var err error
		drift, err = analyzer.Analyze(ctx, structural.Request{Intent: structural.IntentDrift, MaxFiles: 4000})
		return analysisBytes(drift), err
	})
	info, err := index.Snapshot(ctx)
	if err != nil {
		return round{}, err
	}
	result.Files = info.Files
	result.Parsed = info.ParsedFiles
	result.Pending = info.PendingFiles
	result.Stale = info.StaleFiles
	result.Packages = info.Packages

	survey := architecture.Survey
	if survey == nil {
		survey = project.Survey
	}
	if survey != nil {
		result.Modules = len(survey.Modules)
		result.Explored = survey.Explored
		result.Unexplored = append([]string(nil), survey.Unexplored...)
	}
	result.Samples = len(architecture.Samples)
	if result.Samples == 0 {
		result.Samples = len(project.Samples)
	}
	if drift.Drift != nil {
		for _, finding := range drift.Drift.Findings {
			exists := driftTargetExists(root, finding.Target)
			entry := driftFinding{
				Code: finding.Code, Severity: string(finding.Severity),
				Document: finding.Document, Line: finding.Line, Target: finding.Target,
				Exists: exists,
			}
			result.Drift = append(result.Drift, entry)
			if finding.Code == "documented_path_unverified" {
				result.DriftUnknown++
			} else if exists {
				result.DriftFalse++
			} else {
				result.DriftTrue++
			}
		}
		result.DriftFindings = len(result.Drift)
	}

	workspace := retrieval.NewWorkspace(root, "", retrieval.DefaultOptions())
	var hits []retrieval.Hit
	seenHit := map[string]bool{}
	for _, term := range []string{"checkout", "payment", "fulfillment"} {
		search, _, err := workspace.Search(ctx, index, retrieval.Query{Text: term, Limit: 8})
		if err != nil {
			return round{}, err
		}
		for _, hit := range search.Hits {
			if seenHit[hit.Path] {
				continue
			}
			seenHit[hit.Path] = true
			hits = append(hits, hit)
		}
	}
	for i, hit := range hits {
		if i >= 8 {
			break
		}
		result.Hits = append(result.Hits, fmt.Sprintf("%s (%.2f)", hit.Path, hit.Score))
	}
	focus := ""
	for _, keyword := range []string{"checkout", "payment", "fulfillment"} {
		for _, hit := range hits {
			if hit.Test || !strings.HasSuffix(strings.ToLower(hit.Path), ".go") {
				continue
			}
			if strings.Contains(strings.ToLower(hit.Path), "/"+keyword+"/") {
				focus = hit.Path
				break
			}
		}
		if focus != "" {
			break
		}
	}
	if focus == "" {
		for _, keyword := range []string{"checkout", "payment", "fulfillment"} {
			for _, hit := range hits {
				if hit.Test || !strings.HasSuffix(strings.ToLower(hit.Path), ".go") {
					continue
				}
				if strings.Contains(strings.ToLower(hit.Path), keyword) {
					focus = hit.Path
					break
				}
			}
			if focus != "" {
				break
			}
		}
	}
	if focus == "" {
		for _, hit := range hits {
			if hit.Test || !strings.HasSuffix(strings.ToLower(hit.Path), ".go") {
				continue
			}
			focus = hit.Path
			break
		}
	}
	engine := investigate.New(&investigate.StructuralProvider{Source: source, Root: root})
	flowFocus := focus
	if focus != "" {
		dir := filepath.Dir(focus) + "/"
		if symbols, err := index.Symbols(); err == nil {
			bestScore := -1
			for _, symbol := range symbols {
				if !strings.HasPrefix(symbol.Path, dir) || symbol.Test {
					continue
				}
				if symbol.Kind != ast.KindFunc && symbol.Kind != ast.KindMethod {
					continue
				}
				if strings.HasPrefix(symbol.Name, "Test") || strings.HasPrefix(symbol.Name, "Benchmark") {
					continue
				}
				lowerPath := strings.ToLower(symbol.Path)
				lowerName := strings.ToLower(symbol.Name)
				score := 0
				switch {
				case strings.Contains(lowerPath, "/app/"):
					score += 4
				case strings.Contains(lowerPath, "/domain/"):
					score += 2
				}
				switch {
				case strings.Contains(lowerPath, "/adapter/"):
					score -= 10
				case strings.Contains(lowerPath, "bounded_context"):
					score -= 5
				}
				for _, keyword := range []string{"checkout", "place", "payment", "fulfill", "order"} {
					if strings.Contains(lowerName, keyword) {
						score += 3
					}
				}
				if symbol.Kind == ast.KindMethod {
					score++
				}
				if score > bestScore {
					bestScore = score
					flowFocus = symbol.Name
				}
			}
		}
	}
	runInvestigation := func(name, question string, intent planner.Intent, focus string) {
		measure("investigate: "+name, func() (int, error) {
			inv, err := engine.Run(ctx, investigate.Request{
				Question: question, Intent: intent, Focus: focus,
				Budget: investigate.Budget{MaxSteps: 4, MaxDuration: 30 * time.Second},
			})
			if err != nil {
				return 0, err
			}
			result.Investigations = append(result.Investigations,
				fmt.Sprintf("%s: focus %q, %s, confidence %.2f, facts %d, records %d, %s",
					name, focus, inv.Status, inv.Confidence, len(inv.Facts), inv.Records, inv.StopReason))
			for _, fact := range inv.Facts {
				result.FactTotal++
				switch fact.Freshness {
				case evidence.FreshnessCurrent:
					result.FactCurrent++
				case evidence.FreshnessStale:
					result.FactStale++
				default:
					result.FactUnverified++
				}
			}
			for _, claim := range inv.Claims {
				result.Claims++
				if claim.State.Rank() >= evidence.Observed.Rank() {
					result.ClaimsObserved++
				}
			}
			return investigationBytes(inv), nil
		})
	}
	runInvestigation("architecture", "explain the architecture and the module boundaries", planner.IntentArchitecture, "")
	runInvestigation("flow", "trace the checkout to payment to fulfillment flow, including event propagation and outbox/inbox", planner.IntentFlow, flowFocus)
	runInvestigation("drift", "compare documentation with the implementation", planner.IntentDrift, "")

	checkSource := tools.NewStructuralSource(root)
	checkTool := tools.NewCheckTool(checkSource)
	checkCtx := agent.Context{Ctx: ctx}
	discovery := checkTool.Execute(checkCtx, map[string]any{"mode": "discover"})
	result.OutputBytes += len(discovery.Text)
	if discovery.IsError {
		return round{}, fmt.Errorf("check discovery: %s", discovery.Text)
	}
	result.CheckCommands, result.CheckScripted, result.CheckRefused = checkDiscoveryCounts(discovery.Details)
	scope := "."
	if focus != "" {
		scope = filepath.Dir(focus)
	}
	checksArg := make([]any, 0, len(checks))
	for _, kind := range checks {
		checksArg = append(checksArg, kind)
	}
	measure("check: run", func() (int, error) {
		run := checkTool.Execute(checkCtx, map[string]any{
			"mode": "run", "checks": checksArg, "path": scope,
			"timeout_ms": float64(timeout.Milliseconds()),
		})
		result.CheckStatus, result.CheckSummary, result.CheckVerifies, result.CheckScope, result.CheckExit = checkRunSummary(run.Details)
		if run.IsError && result.CheckStatus == "" {
			return len(run.Text), fmt.Errorf("check run: %s", run.Text)
		}
		return len(run.Text), nil
	})
	result.Duration = time.Since(started)
	return result, nil
}

func analysisBytes(analysis structural.Analysis) int {
	encoded, err := json.Marshal(analysis)
	if err != nil {
		return 0
	}
	return len(encoded)
}

func investigationBytes(result investigate.Result) int {
	var total int
	for _, fact := range result.Facts {
		total += len(fact.Subject) + len(fact.Detail) + len(fact.Provider) + len(fact.Source) + 16
	}
	return total
}

func driftTargetExists(root, target string) bool {
	if target == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(root, filepath.FromSlash(target)))
	return err == nil
}

func checkDiscoveryCounts(details any) (commands, scripted, refused int) {
	mapped, _ := details.(map[string]any)
	items, _ := mapped["commands"].([]map[string]any)
	for _, item := range items {
		commands++
		if script, _ := item["script"].(string); script != "" {
			scripted++
		}
	}
	notes, _ := mapped["notes"].([]string)
	for _, note := range notes {
		if strings.Contains(note, "not planned") {
			refused++
		}
	}
	return commands, scripted, refused
}

func checkRunSummary(details any) (status string, summary string, verifies bool, scope string, exit int) {
	mapped, _ := details.(map[string]any)
	status, _ = mapped["status"].(string)
	verifies, _ = mapped["verifies"].(bool)
	scope, _ = mapped["scope"].(string)
	checks, _ := mapped["checks"].([]map[string]any)
	for _, item := range checks {
		if value, ok := item["exit_code"].(int); ok {
			exit = value
		}
		counts, _ := item["test_counts"].(map[string]any)
		if known, _ := counts["known"].(bool); known {
			summary = fmt.Sprintf("%d passed, %d failed, %d skipped, %d not run",
				counts["passed"], counts["failed"], counts["skipped"], counts["not_run"])
		}
	}
	return status, summary, verifies, scope, exit
}

func render(root string, results []round) string {
	if len(results) == 0 {
		return ""
	}
	current := results[len(results)-1]
	var out strings.Builder
	fmt.Fprintf(&out, "# Phase 4 acceptance — Go ecommerce\n\n")
	fmt.Fprintf(&out, "Repository: `%s`\n\n", root)
	if commit := gitCommit(root); commit != "" {
		fmt.Fprintf(&out, "Commit: `%s`\n\n", commit)
	}
	fmt.Fprintf(&out, "Task: investigate checkout → payment → fulfillment, survey modules, trace execution, "+
		"compare documentation, run checks, and disclose unverified coverage.\n\n")
	fmt.Fprintf(&out, "Rounds: %d · stable: %t\n\n", len(results), stable(results))

	fmt.Fprintf(&out, "| Metric | Value |\n|---|---:|\n")
	fmt.Fprintf(&out, "| Files / packages | %d / %d |\n", current.Files, current.Packages)
	fmt.Fprintf(&out, "| Parsed / pending / stale | %d / %d / %d |\n", current.Parsed, current.Pending, current.Stale)
	fmt.Fprintf(&out, "| Modules discovered / represented | %d / %d |\n", current.Modules, current.Explored)
	fmt.Fprintf(&out, "| Samples selected | %d |\n", current.Samples)
	fmt.Fprintf(&out, "| Drift findings (true / false / unverified) | %d / %d / %d |\n",
		current.DriftTrue, current.DriftFalse, current.DriftUnknown)
	fmt.Fprintf(&out, "| Evidence claims observed / total | %d / %d |\n", current.ClaimsObserved, current.Claims)
	fmt.Fprintf(&out, "| Facts current / stale / unverified | %d / %d / %d |\n",
		current.FactCurrent, current.FactStale, current.FactUnverified)
	fmt.Fprintf(&out, "| Check plan / scripted / refused | %d / %d / %d |\n",
		current.CheckCommands, current.CheckScripted, current.CheckRefused)
	fmt.Fprintf(&out, "| Check result | %s, verifies=%t, scope=%s, exit=%d |\n",
		current.CheckStatus, current.CheckVerifies, current.CheckScope, current.CheckExit)
	if current.CheckSummary != "" {
		fmt.Fprintf(&out, "| Test counts | %s |\n", current.CheckSummary)
	}
	fmt.Fprintf(&out, "| Wall time | %s |\n", current.Duration.Round(time.Millisecond))
	fmt.Fprintf(&out, "| Tool output bytes (estimated tokens) | %d (~%d) |\n",
		current.OutputBytes, current.OutputBytes/4)
	fmt.Fprintf(&out, "\n")

	fmt.Fprintf(&out, "## Stages\n\n| Stage | Time | Result |\n|---|---:|---|\n")
	for _, item := range current.Stages {
		status := "ok"
		if item.Err != nil {
			status = item.Err.Error()
		}
		fmt.Fprintf(&out, "| %s | %s | %s |\n", item.Name, item.Time.Round(time.Millisecond), status)
	}
	fmt.Fprintf(&out, "\n")

	fmt.Fprintf(&out, "## Investigations\n\n")
	for _, line := range current.Investigations {
		fmt.Fprintf(&out, "- %s\n", line)
	}
	fmt.Fprintf(&out, "\n")

	fmt.Fprintf(&out, "## Retrieval candidates\n\n")
	if len(current.Hits) == 0 {
		out.WriteString("None.\n\n")
	} else {
		for _, hit := range current.Hits {
			fmt.Fprintf(&out, "- `%s`\n", hit)
		}
		out.WriteString("\n")
	}

	fmt.Fprintf(&out, "## Unexplored modules\n\n")
	if len(current.Unexplored) == 0 {
		out.WriteString("None.\n\n")
	} else {
		for i, module := range current.Unexplored {
			if i >= 20 {
				fmt.Fprintf(&out, "- … %d more\n", len(current.Unexplored)-20)
				break
			}
			fmt.Fprintf(&out, "- `%s`\n", module)
		}
		out.WriteString("\n")
	}

	fmt.Fprintf(&out, "## Drift findings\n\n")
	if len(current.Drift) == 0 {
		out.WriteString("None.\n\n")
	} else {
		fmt.Fprintf(&out, "| Document | Line | Target | Code | Severity | Target exists |\n|---|---:|---|---|---|---|\n")
		for _, finding := range current.Drift {
			fmt.Fprintf(&out, "| `%s` | %d | `%s` | %s | %s | %t |\n",
				finding.Document, finding.Line, finding.Target, finding.Code, finding.Severity, finding.Exists)
		}
		out.WriteString("\n")
	}

	fmt.Fprintf(&out, "## Notes\n\n")
	out.WriteString("- Deterministic tool run; no model call was made. Token figures are estimates from output bytes.\n")
	out.WriteString("- `Target exists` is an independent `os.Stat` check: a `false` on a missing-code finding is a true positive.\n")
	out.WriteString("- A check result does not widen to modules outside its scope; unstaged modules stay listed above.\n")
	out.WriteString("- Structural coverage is partial when the build has no adapter for an observed source extension; facts in that state remain `UNVERIFIED`, not `CURRENT`.\n")
	out.WriteString("- A single-module repository is surveyed at its direct package children (bounded contexts), not as one opaque container.\n")
	out.WriteString("- Check execution boundaries are enforced by `check`: argv-only commands, workspace-contained working directories, credential-shaped environment variables removed, and unsafe package scripts refused before planning.\n")
	return out.String()
}

func stable(results []round) bool {
	if len(results) < 2 {
		return true
	}
	first := signature(results[0])
	for _, result := range results[1:] {
		if signature(result) != first {
			return false
		}
	}
	return true
}

func signature(result round) string {
	parts := []string{
		fmt.Sprintf("%d/%d/%d/%d", result.Files, result.Parsed, result.Pending, result.Stale),
		fmt.Sprintf("%d/%d/%d", result.Modules, result.Explored, result.Samples),
		fmt.Sprintf("%d/%d/%d/%d", result.DriftTrue, result.DriftFalse, result.DriftUnknown, len(result.Drift)),
		fmt.Sprintf("%d/%d", result.ClaimsObserved, result.Claims),
		fmt.Sprintf("%d/%d/%d", result.CheckCommands, result.CheckScripted, result.CheckRefused),
		result.CheckStatus,
		fmt.Sprintf("%t", result.CheckVerifies),
	}
	sort.Strings(parts)
	return strings.Join(parts, "|")
}

func gitCommit(root string) string {
	command := exec.Command("git", "-C", root, "rev-parse", "--short", "HEAD")
	output, err := command.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}
