// Command phase9 is the controlled benchmark harness for the cognitive
// efficiency layer.
//
// It runs the same repository fixtures and the same scripted action sequence
// with projection disabled and enabled. Because the action sequence is fixed,
// a measured difference is attributable to the runtime's context and action
// surface rather than to a model choosing differently.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

func main() {
	scenarioID := flag.String("scenario", "all", "scenario id, category, or all")
	suite := flag.String("suite", "phase9", "scenario suite: phase9 or phase10")
	mode := flag.String("projection", "off", "projection mode: off (control) or state (cognitive)")
	outPath := flag.String("out", "", "write the JSON result to this path")
	asJSON := flag.Bool("json", false, "print JSON instead of a table")
	liveRef := flag.String("live-model", "", "run the matrix with a real provider/model reference")
	maxTurns := flag.Int("max-turns", 30, "maximum model turns per live run")
	maxTokens := flag.Int("max-tokens", 8192, "maximum completion tokens per live request")
	temperature := flag.Float64("temperature", 0, "sampling temperature for live runs")
	timeout := flag.Duration("timeout", 15*time.Minute, "wall-clock budget per live run")
	retries := flag.Int("retries", 3, "stream-open retries for transient provider errors")
	minInterval := flag.Duration("min-interval", 0, "minimum delay between provider requests (rate-limit pacing)")
	flag.Parse()

	if *mode != "off" && *mode != "state" {
		fmt.Fprintf(os.Stderr, "phase9: unknown projection mode %q\n", *mode)
		os.Exit(2)
	}
	if *maxTurns < 1 {
		*maxTurns = 1
	}
	var live liveModel
	if *liveRef != "" {
		resolved, err := resolveLive(*liveRef)
		if err != nil {
			fmt.Fprintf(os.Stderr, "phase9: resolve live model %q: %v\n", *liveRef, err)
			os.Exit(2)
		}
		live = resolved
	}
	options := liveOptions{
		MaxTurns:    *maxTurns,
		MaxTokens:   *maxTokens,
		Temperature: *temperature,
		Timeout:     *timeout,
		Retries:     *retries,
		MinInterval: *minInterval,
	}
	selected := matrix()
	switch *suite {
	case "phase9":
	case "phase10":
		selected = phase10Matrix()
	default:
		fmt.Fprintf(os.Stderr, "phase9: unknown suite %q (want phase9 or phase10)\n", *suite)
		os.Exit(2)
	}
	if *scenarioID != "all" {
		var found []scenario
		for _, sc := range selected {
			if sc.ID == *scenarioID || sc.Category == *scenarioID {
				found = append(found, sc)
			}
		}
		selected = found
		if len(selected) == 0 {
			fmt.Fprintf(os.Stderr, "phase9: no scenario matches %q\n", *scenarioID)
			os.Exit(2)
		}
	}

	results := make([]runMetrics, 0, len(selected))
	failures := 0
	for _, sc := range selected {
		var metrics runMetrics
		var err error
		if *liveRef != "" {
			metrics, err = runLiveScenarioWithOptions(context.Background(), sc, live, *mode, options)
		} else {
			metrics, err = runScenario(context.Background(), sc, *mode)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "phase9: %s: %v\n", sc.ID, err)
			failures++
			continue
		}
		// A live model's failure is benchmark data, not a harness error. The
		// scripted matrix is the controlled contract and must meet every
		// expectation.
		if *liveRef == "" && !expectationMet(sc.Expect, metrics) {
			failures++
		}
		results = append(results, metrics)
	}

	if *outPath != "" {
		encoded, err := json.MarshalIndent(results, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "phase9: encode results: %v\n", err)
			os.Exit(1)
		}
		if err := os.WriteFile(*outPath, append(encoded, '\n'), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "phase9: write %s: %v\n", *outPath, err)
			os.Exit(1)
		}
	}
	if *asJSON {
		encoded, _ := json.MarshalIndent(results, "", "  ")
		fmt.Println(string(encoded))
	} else if *liveRef != "" {
		for _, result := range results {
			fmt.Println(liveSummary(result))
		}
	} else {
		printTable(results)
	}
	if failures > 0 {
		fmt.Fprintf(os.Stderr, "phase9: %d expectation(s) not met\n", failures)
		os.Exit(1)
	}
}

// expectationMet reports whether one result satisfies the scenario's contract.
func expectationMet(expect expectation, metrics runMetrics) bool {
	if expect.Completed && metrics.ObjectiveStatus != "COMPLETED" {
		return false
	}
	if !expect.Completed && metrics.ObjectiveStatus == "COMPLETED" {
		// A negative control may still fail for a reason the matrix did not
		// predict; the report names it. The hard rule is that a task expected
		// not to complete must not claim completion.
		return false
	}
	if expect.ReadOnly && metrics.Mutated {
		return false
	}
	if expect.Mutation && !metrics.Mutated {
		return false
	}
	if expect.Verified && !metrics.Verified {
		return false
	}
	if expect.Mutation && !expect.AllowUnexpected && len(metrics.Unexpected) > 0 {
		return false
	}
	return true
}

// printTable renders the comparison in a form a terminal reader can scan.
func printTable(results []runMetrics) {
	fmt.Printf("%-28s %-16s %8s %8s %7s %7s %7s %9s %9s\n",
		"SCENARIO", "STATUS", "REQS", "TOOLS", "IN_TOK", "CONTEXT", "REPEAT", "FIRST", "LAST")
	for _, result := range results {
		fmt.Printf("%-28s %-16s %8d %8d %7d %7d %7d %9d %9d\n",
			result.Scenario,
			result.ObjectiveStatus,
			result.RequestCount,
			result.ToolCallCount,
			result.InputTokens,
			result.ContextBytes,
			result.RepeatedBytes,
			result.FirstRequestBytes,
			result.LastRequestBytes,
		)
		if result.CompletionReason != "" {
			fmt.Printf("  reason: %s\n", truncateLine(result.CompletionReason, 160))
		}
		if len(result.Unexpected) > 0 {
			fmt.Printf("  unexpected: %s\n", strings.Join(result.Unexpected, ", "))
		}
	}
}

func truncateLine(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
}
