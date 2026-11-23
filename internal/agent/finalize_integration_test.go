package agent_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/cognition"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
	"github.com/PizenLabs/ohgo/internal/execution/tools"
	"github.com/PizenLabs/ohgo/internal/jsonschema"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// A model that answers before the repository frontier closes is corrected
// internally. The first answer is a draft, not a result; the correction
// reopens the action surface, the missing evidence is acquired, and only the
// answer after that is committed.
func TestPrematureInformationalAnswerIsCorrectedBeforeCommit(t *testing.T) {
	root := t.TempDir()
	for name, body := range map[string]string{
		"go.mod":    "module example.com/ohgo\n\ngo 1.24\n",
		"README.md": "# ohgo\nA coding agent with a controlled runtime.\n",
		"main.go":   "package main\nfunc main() {}\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	prompt := "check this project and analyze what this project is about?"
	intent := workspace.BuildIntentContract(prompt)
	if intent.Kind != contract.ObjectiveInformational {
		t.Fatalf("intent kind = %s, want informational", intent.Kind)
	}
	facts := cognition.ProbeFacts(root)
	shape := cognition.BuildShape(intent, cognition.ShapeParams{Authority: "ask", Facts: facts})
	projector := cognition.NewProjector(shape, cognition.NewEvidenceState(), facts)

	script := []ai.AssistantMessage{
		{Content: []ai.ContentBlock{ai.TextContent{Text: "It is a Go project."}}},
		{Content: overviewCall("turn-2"), StopReason: ai.StopToolUse},
		{Content: []ai.ContentBlock{ai.TextContent{Text: "ohgo is a Go coding agent with a controlled runtime."}}},
	}
	var requests atomic.Int64
	var lastReport agent.ProjectionReport
	haveReport := false
	loop, err := agent.New(agent.Config{
		Model: ai.Model{ID: "m", Provider: "p"},
		Stream: func(context.Context, ai.Request) (*ai.Stream, error) {
			index := int(requests.Add(1)) - 1
			if index >= len(script) {
				t.Fatalf("provider request %d exceeded the script", index+1)
			}
			return ai.NewCompletedStream(script[index]), nil
		},
		Tools:                   []agent.Tool{evidenceReadTool{}, evidenceListTool{}},
		Project:                 projector.Project,
		Authorize:               allowRead{},
		MaxTurns:                8,
		CompletionReviewRequest: "Runtime completion review: {{reason}}. Acquire the missing evidence or state the limitation.",
		OnProjection: func(report agent.ProjectionReport) {
			lastReport = report
			haveReport = true
		},
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}

	provenance := func() agent.EvidenceProvenance {
		if haveReport {
			return lastReport.Provenance
		}
		return agent.EvidenceProvenance{}
	}
	loop.SetInformationalReviews(intent, provenance)
	loop.SetEvidenceGap(func() string {
		if haveReport {
			return lastReport.EvidenceReview()
		}
		return ""
	})
	loop.SetAnswerFinalizer(func(turns []agent.Turn, answer, gap string) agent.FinalizedAnswer {
		return agent.FinalizeAnswer(intent, turns, answer, provenance(), gap)
	})

	turns, err := loop.Run(context.Background(), []agent.Message{agent.UserMessage{Text: prompt}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if requests.Load() != 3 {
		t.Fatalf("provider requests = %d, want 3 (premature answer, evidence, final answer)", requests.Load())
	}
	if len(turns) != 3 {
		t.Fatalf("turns = %d, want 3", len(turns))
	}
	if !turns[0].Suppressed {
		t.Fatal("the premature answer was not suppressed")
	}
	if got := turns[len(turns)-1].Assistant.Text(); !strings.Contains(got, "controlled runtime") {
		t.Fatalf("final answer = %q, want the evidence-backed answer", got)
	}
	audit := loop.LastResponseAudit()
	if audit.State != agent.ResponseCommitted {
		t.Fatalf("response state = %s, want COMMITTED", audit.State)
	}
	if audit.Rejected != 1 {
		t.Fatalf("rejected drafts = %d, want 1", audit.Rejected)
	}
	if audit.Answer.UnsupportedClaims != 0 {
		t.Fatalf("committed answer has %d unsupported claims", audit.Answer.UnsupportedClaims)
	}
	if !haveReport {
		t.Fatal("no projection report was recorded")
	}
	completion := agent.EvaluateTaskCompletion(agent.CompletionInput{
		Turns:          turns,
		Contract:       intent,
		EvidenceReview: lastReport.EvidenceReview(),
		Provenance:     lastReport.Provenance,
	})
	if !completion.Complete() {
		t.Fatalf("completion = %s (%s), want COMPLETED", completion.State, completion.Reason)
	}
}

// Two valid strategies with different call counts both complete; a shorter
// strategy that misses required evidence does not. The runtime evaluates the
// frontier, not the tool count.
func TestCompletionDependsOnEvidenceNotToolCount(t *testing.T) {
	const answer = "ohgo is a coding agent with a controlled runtime."
	cases := []struct {
		name   string
		script []ai.AssistantMessage
		want   bool
	}{
		{
			name: "separate-read-strategy",
			script: []ai.AssistantMessage{
				{Content: []ai.ContentBlock{ai.ToolCall{ID: "r1", Name: "read", Arguments: map[string]any{"path": "README.md"}}}, StopReason: ai.StopToolUse},
				{Content: []ai.ContentBlock{ai.ToolCall{ID: "r2", Name: "read", Arguments: map[string]any{"path": "go.mod"}}}, StopReason: ai.StopToolUse},
				{Content: []ai.ContentBlock{ai.ToolCall{ID: "r3", Name: "read", Arguments: map[string]any{"path": "main.go"}}}, StopReason: ai.StopToolUse},
				{Content: []ai.ContentBlock{ai.TextContent{Text: answer}}},
			},
			want: true,
		},
		{
			name: "one-batch-strategy",
			script: []ai.AssistantMessage{
				{Content: overviewCall("b1"), StopReason: ai.StopToolUse},
				{Content: []ai.ContentBlock{ai.TextContent{Text: answer}}},
			},
			want: true,
		},
		{
			name: "short-strategy-missing-evidence",
			script: []ai.AssistantMessage{
				{Content: readCall("s1"), StopReason: ai.StopToolUse},
				{Content: []ai.ContentBlock{ai.TextContent{Text: answer}}},
			},
			want: false,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			result := runInformational(t, test.script)
			if result.err != nil {
				t.Fatalf("Run: %v", result.err)
			}
			verdict := agent.EvaluateTaskCompletion(agent.CompletionInput{
				Turns:          result.turns,
				Contract:       result.intent,
				EvidenceReview: result.report.EvidenceReview(),
			})
			if verdict.Complete() != test.want {
				t.Fatalf("completion = %s (%s), want complete=%v; evidence review=%q",
					verdict.State, verdict.Reason, test.want, result.report.EvidenceReview())
			}
		})
	}
}

// A source-matched repository grep is implementation evidence. A detailed
// answer that names the technology the grep observed must pass the
// pre-publication review without spending a correction, even though the term
// also appears in the README. The evidence model used to classify the search
// by its root, so the runtime demanded another survey it could never satisfy.
func TestSourceGrepPreventsAProvenanceCorrection(t *testing.T) {
	root := t.TempDir()
	for name, body := range map[string]string{
		"go.mod":    "module example.com/gocommerce\n\ngo 1.24\n",
		"README.md": "# go-ecommerce\nGoCommerce is a Go + HTMX e-commerce application.\n",
		"main.go":   "package main\n\n// htmx request handler\nfunc main() {}\n",
		"app.go":    "package main\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	prompt := "check this project and analyze what this project is about?"
	intent := workspace.BuildIntentContract(prompt)
	facts := cognition.ProbeFacts(root)
	shape := cognition.BuildShape(intent, cognition.ShapeParams{Authority: "ask", Facts: facts})
	projector := cognition.NewProjector(shape, cognition.NewEvidenceState(), facts)

	const answer = "GoCommerce is a Go + HTMX e-commerce application. The source handles htmx requests."
	script := []ai.AssistantMessage{
		{
			Content: []ai.ContentBlock{
				ai.ToolCall{ID: "structure-1", Name: "structure", Arguments: map[string]any{"view": "overview"}},
				ai.ToolCall{ID: "read-readme", Name: "read", Arguments: map[string]any{"path": "README.md"}},
				ai.ToolCall{ID: "read-main", Name: "read", Arguments: map[string]any{"path": "main.go"}},
				ai.ToolCall{ID: "grep-1", Name: "grep", Arguments: map[string]any{"pattern": "HTMX|htmx"}},
			},
			StopReason: ai.StopToolUse,
		},
		{Content: []ai.ContentBlock{ai.TextContent{Text: answer}}},
	}
	var requests atomic.Int64
	var lastReport agent.ProjectionReport
	haveReport := false
	loop, err := agent.New(agent.Config{
		Model: ai.Model{ID: "m", Provider: "p"},
		Stream: func(context.Context, ai.Request) (*ai.Stream, error) {
			index := int(requests.Add(1)) - 1
			if index >= len(script) {
				t.Fatalf("provider request %d exceeded the script; the review forced a correction", index+1)
			}
			return ai.NewCompletedStream(script[index]), nil
		},
		Tools: []agent.Tool{
			evidenceReadTool{},
			tools.NewGrepTool(root),
			sourceOverviewTool{},
		},
		Project:                 projector.Project,
		Authorize:               allowRead{},
		MaxTurns:                8,
		CompletionReviewRequest: "Runtime completion review: {{reason}}. Acquire the missing evidence.",
		OnProjection: func(report agent.ProjectionReport) {
			lastReport = report
			haveReport = true
		},
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	provenance := func() agent.EvidenceProvenance {
		if haveReport {
			return lastReport.Provenance
		}
		return agent.EvidenceProvenance{}
	}
	loop.SetInformationalReviews(intent, provenance)
	loop.SetEvidenceGap(func() string {
		if haveReport {
			return lastReport.EvidenceReview()
		}
		return ""
	})
	loop.SetAnswerFinalizer(func(turns []agent.Turn, answer, gap string) agent.FinalizedAnswer {
		return agent.FinalizeAnswer(intent, turns, answer, provenance(), gap)
	})

	turns, err := loop.Run(context.Background(), []agent.Message{agent.UserMessage{Text: prompt}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if requests.Load() != 2 {
		t.Fatalf("provider requests = %d, want the observation and the answer (2)", requests.Load())
	}
	if len(turns) != 2 {
		t.Fatalf("turns = %d, want the observation and the answer", len(turns))
	}
	if turns[0].Suppressed || turns[1].Suppressed {
		t.Fatalf("turns = %+v, want no rejected candidate", turns)
	}
	if got := turns[len(turns)-1].Assistant.Text(); got != answer {
		t.Fatalf("final answer was rewritten:\n%s", got)
	}
	audit := loop.LastResponseAudit()
	if audit.State != agent.ResponseCommitted {
		t.Fatalf("response state = %s, want COMMITTED", audit.State)
	}
	if audit.Answer.UnsupportedClaims != 0 {
		t.Fatalf("committed answer audit = %+v", audit.Answer)
	}
	if audit.Rejected != 0 {
		t.Fatalf("rejected candidates = %d, want none", audit.Rejected)
	}
}

// sourceOverviewTool reports a measured three-file source tree. It is the
// producer accounting the survey requirement reads, not a content observation.
type sourceOverviewTool struct{}

func (sourceOverviewTool) Name() string        { return "structure" }
func (sourceOverviewTool) Description() string { return "structural overview" }
func (sourceOverviewTool) Parameters() *jsonschema.Schema {
	return jsonschema.MustCompile([]byte(`{"type":"object"}`))
}
func (sourceOverviewTool) Approval() agent.Approval      { return agent.Approval{Tier: agent.TierRead} }
func (sourceOverviewTool) Exclusive(map[string]any) bool { return false }
func (sourceOverviewTool) Execute(agent.Context, map[string]any) agent.Result {
	return agent.Result{
		Text: "view: overview\ninventory: COMPLETE\nstructural: COMPLETE\nsource_files: 3\n",
		Details: map[string]any{
			"view": "overview", "source_files": 3, "files": 3,
			"complete": true, "inventory_complete": true,
			"scope_complete": true, "structural_complete": true,
		},
	}
}
