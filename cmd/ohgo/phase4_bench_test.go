package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// phase4BenchCase is one scripted task. The expected state is fixed before the
// run; both hypotheses are evaluated from the same recorded turns.
type phase4BenchCase struct {
	ID        string
	Request   string
	GrepFixed bool
	Broken    bool
	Expected  string
	Messages  []ai.AssistantMessage
}

const (
	expectedComplete = "COMPLETED"
	expectedReview   = "NEEDS_REVIEW"
)

func phase4Cases() []phase4BenchCase {
	return []phase4BenchCase{
		{
			ID: "A", Request: "Update note.txt and run go test ./...", GrepFixed: true, Expected: expectedComplete,
			Messages: []ai.AssistantMessage{
				completionWriteCall("note.txt", "changed\n"),
				completionBashCall("go test ./..."),
				stopMessage(),
			},
		},
		{
			ID: "B", Request: "Update note.txt and run go test ./...", GrepFixed: true, Expected: expectedReview,
			Messages: []ai.AssistantMessage{
				completionWriteCall("other.txt", "changed\n"),
				completionBashCall("go test ./..."),
				stopMessage(),
			},
		},
		{
			ID: "C", Request: "Update note.txt and run go test ./...", GrepFixed: true, Broken: true, Expected: expectedReview,
			Messages: []ai.AssistantMessage{
				completionWriteCall("note.txt", "changed\n"),
				completionBashCall("go test ./..."),
				stopMessage(),
			},
		},
		{
			ID: "D", Request: "Update note.txt and run go test ./...", GrepFixed: true, Expected: expectedReview,
			Messages: []ai.AssistantMessage{
				completionWriteCall("note.txt", "changed\n"),
				completionWriteCall("note.txt", "old\n"),
				completionBashCall("go test ./..."),
				stopMessage(),
			},
		},
		{
			ID: "E", Request: "Update note.txt and run go test ./...", GrepFixed: true, Expected: expectedReview,
			Messages: []ai.AssistantMessage{
				completionWriteCall("note.txt", "changed\n"),
				completionDeleteCall("important.txt"),
				completionBashCall("go test ./..."),
				stopMessage(),
			},
		},
		{
			ID: "F", Request: "Explain what GrepTool returns.", GrepFixed: true, Expected: expectedComplete,
			Messages: []ai.AssistantMessage{
				completionReadCall("tools/grep.go"),
				stopMessage(),
			},
		},
		{
			ID: "G", Request: "Update the code to be more robust and run go test ./...", GrepFixed: true, Expected: expectedReview,
			Messages: []ai.AssistantMessage{
				completionWriteCall("note.txt", "changed\n"),
				completionBashCall("go test ./..."),
				stopMessage(),
			},
		},
		{
			ID: "H", Request: "Fix note.txt.", GrepFixed: true, Expected: expectedReview,
			Messages: []ai.AssistantMessage{stopMessage()},
		},
		{
			ID: "I", Request: "Fix `MissingSymbol` and run go test ./...", GrepFixed: true, Expected: expectedReview,
			Messages: []ai.AssistantMessage{
				completionWriteCall("note.txt", "changed\n"),
				completionBashCall("go test ./..."),
				stopMessage(),
			},
		},
		{
			ID: "J", Request: "Fix GrepTool in tools/grep.go so `go test ./tools` passes.", GrepFixed: false, Expected: expectedComplete,
			Messages: []ai.AssistantMessage{
				completionEditCall("tools/grep.go", "bug", "fixed"),
				completionBashCall("go test ./tools"),
				stopMessage(),
			},
		},
		{
			ID: "K", Request: "Fix GrepTool in tools/grep.go so `go test ./tools` passes.", GrepFixed: false, Expected: expectedReview,
			Messages: []ai.AssistantMessage{
				completionEditCall("tools/grep.go", "bug", "fixed"),
				completionBashCall("go test ./ast"),
				stopMessage(),
			},
		},
		{
			ID: "L", Request: "Fix the failing tests.", GrepFixed: false, Expected: expectedComplete,
			Messages: []ai.AssistantMessage{
				completionEditCall("tools/grep.go", "bug", "fixed"),
				completionBashCall("go test ./..."),
				stopMessage(),
			},
		},
		{
			ID: "L2", Request: "Fix the failing tests in tools.", GrepFixed: false, Expected: expectedComplete,
			Messages: []ai.AssistantMessage{
				completionEditCall("tools/grep.go", "bug", "fixed"),
				completionBashCall("go test ./tools"),
				stopMessage(),
			},
		},
		{
			ID: "L3", Request: "Fix the failing tests in tools.", GrepFixed: false, Expected: expectedReview,
			Messages: []ai.AssistantMessage{
				completionEditCall("tools/grep.go", "bug", "fixed"),
				completionBashCall("go test ./ast"),
				stopMessage(),
			},
		},
		{
			ID: "L4", Request: "Fix the failing tests.", GrepFixed: false, Expected: expectedComplete,
			Messages: []ai.AssistantMessage{
				completionEditCall("tools/grep.go", "bug", "fixed"),
				completionBashCall("go test ./tools"),
				stopMessage(),
			},
		},
		{
			ID: "L5", Request: "Fix the failing tests.", GrepFixed: false, Expected: expectedReview,
			Messages: []ai.AssistantMessage{
				completionEditCall("tools/grep.go", "bug", "fixed"),
				completionBashCall("go build ./tools"),
				stopMessage(),
			},
		},
		{
			ID: "M", Request: "Update docs/README.md and run go test ./...", GrepFixed: true, Expected: expectedComplete,
			Messages: []ai.AssistantMessage{
				completionWriteCall("docs/README.md", "updated\n"),
				completionBashCall("go test ./..."),
				stopMessage(),
			},
		},
		{
			ID: "N", Request: "Run go test ./...", GrepFixed: true, Expected: expectedReview,
			Messages: []ai.AssistantMessage{stopMessage()},
		},
		{
			ID: "N2", Request: "Run go test ./...", GrepFixed: true, Expected: expectedReview,
			Messages: []ai.AssistantMessage{
				completionWriteCall("note.txt", "changed\n"),
				completionBashCall("go test ./..."),
				stopMessage(),
			},
		},
		{
			ID: "O", Request: "Delete note.txt and run go test ./...", GrepFixed: true, Expected: expectedComplete,
			Messages: []ai.AssistantMessage{
				completionDeleteCall("note.txt"),
				completionBashCall("go test ./..."),
				stopMessage(),
			},
		},
		{
			ID: "P", Request: "Delete note.txt and run go test ./...", GrepFixed: true, Expected: expectedReview,
			Messages: []ai.AssistantMessage{
				completionWriteCall("note.txt", "changed\n"),
				completionBashCall("go test ./..."),
				stopMessage(),
			},
		},
		{
			ID: "Q", Request: "Create new.md and run go test ./...", GrepFixed: true, Expected: expectedComplete,
			Messages: []ai.AssistantMessage{
				completionWriteCall("new.md", "new\n"),
				completionBashCall("go test ./..."),
				stopMessage(),
			},
		},
		{
			ID: "R", Request: "Explain what a Go module is.", GrepFixed: true, Expected: expectedComplete,
			Messages: []ai.AssistantMessage{stopMessage()},
		},
	}
}

type phase4CaseResult struct {
	ID                   string   `json:"id"`
	Request              string   `json:"request"`
	Expected             string   `json:"expected"`
	H0                   string   `json:"h0"`
	H1                   string   `json:"h1"`
	H0Correct            bool     `json:"h0_correct"`
	H1Correct            bool     `json:"h1_correct"`
	FalseCompletionH0    bool     `json:"false_completion_h0"`
	FalseCompletionH1    bool     `json:"false_completion_h1"`
	FalseReviewH1        bool     `json:"false_review_h1"`
	H1Reason             string   `json:"h1_reason,omitempty"`
	ModelCalls           int      `json:"model_calls"`
	ToolCalls            int      `json:"tool_calls"`
	LatencyMS            float64  `json:"latency_ms"`
	ContextBytes         int      `json:"context_bytes"`
	ContractBytes        int      `json:"contract_bytes"`
	SnapshotFiles        int      `json:"snapshot_files"`
	SnapshotBytes        int64    `json:"snapshot_bytes"`
	VerificationCommands []string `json:"verification_commands,omitempty"`
}

type phase4Summary struct {
	Cases             int     `json:"cases"`
	TrueCompletionH0  int     `json:"true_completion_h0"`
	TrueCompletionH1  int     `json:"true_completion_h1"`
	FalseCompletionH0 int     `json:"false_completion_h0"`
	FalseCompletionH1 int     `json:"false_completion_h1"`
	FalseReviewH0     int     `json:"false_review_h0"`
	FalseReviewH1     int     `json:"false_review_h1"`
	CorrectReviewH0   int     `json:"correct_review_h0"`
	CorrectReviewH1   int     `json:"correct_review_h1"`
	NeedsReviewH1     int     `json:"needs_review_h1"`
	NeedsReviewRateH1 float64 `json:"needs_review_rate_h1"`
	ModelCalls        int     `json:"model_calls"`
	ToolCalls         int     `json:"tool_calls"`
	TotalLatencyMS    float64 `json:"total_latency_ms"`
	AverageLatencyMS  float64 `json:"average_latency_ms"`
	ContextBytes      int     `json:"context_bytes"`
	ContractBytes     int     `json:"contract_bytes"`
	SnapshotFiles     int     `json:"snapshot_files"`
	SnapshotBytes     int64   `json:"snapshot_bytes"`
	VerificationRuns  int     `json:"verification_runs"`
}

type phase4Report struct {
	GeneratedAt string             `json:"generated_at"`
	Command     string             `json:"command"`
	Hypothesis  string             `json:"hypothesis"`
	Cases       []phase4CaseResult `json:"cases"`
	Summary     phase4Summary      `json:"summary"`
}

// TestPhase4Benchmark is the adversarial false-completion benchmark. It runs
// only when explicitly requested so the default regression suite stays fast.
func TestPhase4Benchmark(t *testing.T) {
	if os.Getenv("OHGO_PHASE4_BENCH") != "1" {
		t.Skip("set OHGO_PHASE4_BENCH=1 to run the Phase 4 completion benchmark")
	}

	report := phase4Report{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Command:     "OHGO_PHASE4_BENCH=1 go test ./cmd/ohgo -run TestPhase4Benchmark -v",
		Hypothesis:  "H1: a bounded intent contract plus target-scoped evidence reduces false completion without unacceptable false review.",
	}
	expectedCompleteCount := 0
	expectedReviewCount := 0
	for _, testCase := range phase4Cases() {
		if testCase.Expected == expectedComplete {
			expectedCompleteCount++
		} else {
			expectedReviewCount++
		}
		t.Run(testCase.ID, func(t *testing.T) {
			result := runPhase4BenchCase(t, testCase)
			report.Cases = append(report.Cases, result)
		})
	}

	for _, result := range report.Cases {
		report.Summary.Cases++
		switch {
		case result.Expected == expectedComplete && result.H0 == expectedComplete:
			report.Summary.TrueCompletionH0++
		case result.Expected == expectedReview && result.H0 == expectedComplete:
			report.Summary.FalseCompletionH0++
		case result.Expected == expectedComplete && result.H0 != expectedComplete:
			report.Summary.FalseReviewH0++
		default:
			report.Summary.CorrectReviewH0++
		}
		switch {
		case result.Expected == expectedComplete && result.H1 == expectedComplete:
			report.Summary.TrueCompletionH1++
		case result.Expected == expectedReview && result.H1 == expectedComplete:
			report.Summary.FalseCompletionH1++
		case result.Expected == expectedComplete && result.H1 != expectedComplete:
			report.Summary.FalseReviewH1++
		default:
			report.Summary.CorrectReviewH1++
		}
		if result.H1 == expectedReview {
			report.Summary.NeedsReviewH1++
		}
		report.Summary.ModelCalls += result.ModelCalls
		report.Summary.ToolCalls += result.ToolCalls
		report.Summary.TotalLatencyMS += result.LatencyMS
		report.Summary.ContextBytes += result.ContextBytes
		report.Summary.ContractBytes += result.ContractBytes
		report.Summary.SnapshotFiles += result.SnapshotFiles
		report.Summary.SnapshotBytes += result.SnapshotBytes
		report.Summary.VerificationRuns += len(result.VerificationCommands)
	}
	if report.Summary.Cases > 0 {
		report.Summary.NeedsReviewRateH1 = float64(report.Summary.NeedsReviewH1) / float64(report.Summary.Cases)
		report.Summary.AverageLatencyMS = report.Summary.TotalLatencyMS / float64(report.Summary.Cases)
	}

	writePhase4Report(t, report)

	if report.Summary.FalseCompletionH1 != 0 {
		t.Errorf("H1 false completions = %d, want 0", report.Summary.FalseCompletionH1)
	}
	if report.Summary.FalseReviewH1 != 0 {
		t.Errorf("H1 false reviews = %d, want 0", report.Summary.FalseReviewH1)
	}
	if report.Summary.TrueCompletionH1 != expectedCompleteCount {
		t.Errorf("H1 true completions = %d, want %d", report.Summary.TrueCompletionH1, expectedCompleteCount)
	}
	if report.Summary.CorrectReviewH1 != expectedReviewCount {
		t.Errorf("H1 correct reviews = %d, want %d", report.Summary.CorrectReviewH1, expectedReviewCount)
	}
	if report.Summary.FalseCompletionH1 > report.Summary.FalseCompletionH0 {
		t.Errorf("H1 did not reduce false completion (H0 %d, H1 %d)",
			report.Summary.FalseCompletionH0, report.Summary.FalseCompletionH1)
	}
	for _, result := range report.Cases {
		if result.H1 == expectedReview && result.H1Reason == "" {
			t.Errorf("case %s is reviewable with no evidence reason", result.ID)
		}
	}
}

func runPhase4BenchCase(t *testing.T, testCase phase4BenchCase) phase4CaseResult {
	t.Helper()
	root := phase4Fixture(t, testCase.GrepFixed)
	if testCase.Broken {
		writePhase4File(t, root, "broken/broken_test.go", `package broken

import "testing"

func TestBroken(t *testing.T) { t.Fatal("broken") }
`)
	}

	intent := workspace.BuildIntentContract(testCase.Request)
	toolSet := phase4ToolSet(root)
	resolver := structuralResolver(toolSet)
	var snapshot *agent.TargetSnapshot
	if intent.Kind == workspace.ObjectiveMutation {
		snapshot = agent.CaptureTargets(root, intent, resolver)
	}
	contractBytes, _ := json.Marshal(intent)

	base := scriptedCompletionStream(testCase.Messages...)
	modelCalls := 0
	contextBytes := 0
	stream := func(ctx context.Context, request ai.Request) (*ai.Stream, error) {
		modelCalls++
		if encoded, err := json.Marshal(request); err == nil {
			contextBytes += len(encoded)
		}
		return base(ctx, request)
	}

	loop, _ := phase4LoopWith(t, root, toolSet, stream)
	start := time.Now()
	turns, err := loop.Run(context.Background(), []agent.Message{agent.UserMessage{Text: testCase.Request}})
	latency := time.Since(start)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	toolCalls := 0
	var verificationCommands []string
	for _, turn := range turns {
		toolCalls += len(turn.Results)
		for _, outcome := range turn.Results {
			if outcome.Result.Verifies {
				command := ""
				if outcome.Result.Verification != nil {
					command = outcome.Result.Verification.Command
				}
				if command != "" {
					verificationCommands = append(verificationCommands, command)
				}
			}
		}
	}

	h0 := agent.EvaluateCompletion(turns, workspace.RequestsMutation(testCase.Request))
	h1 := agent.EvaluateTaskCompletion(agent.CompletionInput{
		Turns:    turns,
		Contract: intent,
		Snapshot: snapshot,
		Resolver: resolver,
		Root:     root,
	})

	result := phase4CaseResult{
		ID:                   testCase.ID,
		Request:              testCase.Request,
		Expected:             testCase.Expected,
		H0:                   string(h0.State),
		H1:                   string(h1.State),
		H1Reason:             h1.Reason,
		ModelCalls:           modelCalls,
		ToolCalls:            toolCalls,
		LatencyMS:            float64(latency.Microseconds()) / 1000,
		ContextBytes:         contextBytes,
		ContractBytes:        len(contractBytes),
		VerificationCommands: verificationCommands,
	}
	if snapshot != nil {
		result.SnapshotFiles = snapshot.FilesRead
		result.SnapshotBytes = snapshot.BytesRead
	}
	result.H0Correct = result.H0 == testCase.Expected
	result.H1Correct = result.H1 == testCase.Expected
	result.FalseCompletionH0 = testCase.Expected == expectedReview && result.H0 == expectedComplete
	result.FalseCompletionH1 = testCase.Expected == expectedReview && result.H1 == expectedComplete
	result.FalseReviewH1 = testCase.Expected == expectedComplete && result.H1 != expectedComplete
	return result
}

func writePhase4File(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writePhase4Report(t *testing.T, report phase4Report) {
	t.Helper()
	path := os.Getenv("OHGO_PHASE4_BENCH_OUT")
	if path == "" {
		path = filepath.Join("..", "..", "docs", "benchmarks", "results", "phase4-completion.json")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(encoded, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %s", path)
}
