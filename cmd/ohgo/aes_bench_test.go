package main

// Live Adaptive Experience benchmark harness.
//
// It is gated by OHGO_AES_BENCH=1 so the ordinary test suite never performs a
// provider request. When enabled it drives the production TUI task path without
// the terminal renderer: the same buildLoop, installTaskContinuity, and
// installExperience seams, the same submit/runTurn transition, and the same
// advisor and observer. Presentation is the only omitted layer.
//
// The harness records actual provider usage from agent.Turn.Assistant.Usage and
// the projection audit from the durable task controller. It never estimates
// tokens from transcript size and never forces promotion.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/config"
	"github.com/PizenLabs/ohgo/internal/control/cognition"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
	"github.com/PizenLabs/ohgo/internal/experience"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/provider/catalog"
	"github.com/PizenLabs/ohgo/internal/state"
	"github.com/PizenLabs/ohgo/internal/tui"
	tea "github.com/charmbracelet/bubbletea"
)

type aesBenchUsage struct {
	Input      int `json:"input"`
	Output     int `json:"output"`
	CacheRead  int `json:"cache_read"`
	CacheWrite int `json:"cache_write"`
}

type aesBenchTurn struct {
	Index      int           `json:"index"`
	ToolCalls  []string      `json:"tool_calls,omitempty"`
	Usage      aesBenchUsage `json:"usage"`
	StopReason string        `json:"stop_reason,omitempty"`
	Err        string        `json:"error,omitempty"`
	TextChars  int           `json:"text_chars,omitempty"`
	ElapsedMS  int64         `json:"elapsed_ms,omitempty"`
}

type aesBenchRecord struct {
	ID             string `json:"id"`
	Status         string `json:"status"`
	Scope          string `json:"scope"`
	Kind           string `json:"kind"`
	LessonKind     string `json:"lesson_kind"`
	Statement      string `json:"statement"`
	Hint           string `json:"hint,omitempty"`
	Context        string `json:"context"`
	Observations   int    `json:"observations"`
	Contradictions int    `json:"contradictions"`
	Regression     string `json:"regression,omitempty"`
}

// aesBenchAdvice is one observed advisor invocation. It is the application
// evidence: a promoted record counts as applied only when Matched is non-empty.
type aesBenchAdvice struct {
	Shape       string   `json:"shape"`
	Sufficiency string   `json:"sufficiency"`
	Offered     []string `json:"offered_tools,omitempty"`
	Preferred   []string `json:"preferred_tools,omitempty"`
	Hints       []string `json:"hints,omitempty"`
	Matched     []string `json:"matched,omitempty"`
	Reorder     bool     `json:"reordered,omitempty"`
	DurationUS  int64    `json:"duration_us"`
}

type aesBenchRun struct {
	Label             string           `json:"label"`
	Phase             string           `json:"phase"`
	Model             string           `json:"model"`
	Provider          string           `json:"provider"`
	ModelID           string           `json:"model_id"`
	Prompt            string           `json:"prompt"`
	Authority         string           `json:"authority"`
	Root              string           `json:"root"`
	GitHead           string           `json:"git_head,omitempty"`
	GitStatus         string           `json:"git_status,omitempty"`
	StartedAt         string           `json:"started_at"`
	FinishedAt        string           `json:"finished_at"`
	DurationMS        int64            `json:"duration_ms"`
	ModelRequests     int              `json:"model_requests"`
	ToolCalls         int              `json:"tool_calls"`
	ToolsUsed         []string         `json:"tools_used,omitempty"`
	RepeatedProbes    int              `json:"repeated_probes"`
	InputTokens       int              `json:"input_tokens"`
	OutputTokens      int              `json:"output_tokens"`
	CacheReadTokens   int              `json:"cache_read_tokens"`
	CacheWriteTokens  int              `json:"cache_write_tokens"`
	PeakInput         int              `json:"peak_request_input"`
	TaskStatus        string           `json:"task_status"`
	ReviewReason      string           `json:"review_reason,omitempty"`
	Answer            string           `json:"answer,omitempty"`
	AnswerChars       int              `json:"answer_chars"`
	Sufficiency       string           `json:"sufficiency,omitempty"`
	EvidenceSummary   string           `json:"evidence_summary,omitempty"`
	Shape             string           `json:"shape,omitempty"`
	Stage             string           `json:"stage,omitempty"`
	AnswerEligible    bool             `json:"answer_eligible,omitempty"`
	Terminal          bool             `json:"terminal,omitempty"`
	StopReason        string           `json:"stop_reason,omitempty"`
	StateBlockBytes   int              `json:"state_block_bytes"`
	BytesBefore       int              `json:"bytes_before"`
	BytesAfter        int              `json:"bytes_after"`
	ExperienceApplied []string         `json:"experience_applied,omitempty"`
	ExperienceHints   []string         `json:"experience_hints,omitempty"`
	ToolOrder         []string         `json:"tool_order,omitempty"`
	Records           []aesBenchRecord `json:"experience_records,omitempty"`
	ExperienceFile    string           `json:"experience_file,omitempty"`
	ExperienceBytes   int64            `json:"experience_file_bytes"`
	Error             string           `json:"error,omitempty"`
	Turns             []aesBenchTurn   `json:"turns,omitempty"`
	AdvisorCalls      int              `json:"advisor_calls"`
	AdvisorApplied    int              `json:"advisor_applied_calls"`
	AdvisorReorder    int              `json:"advisor_reorder_calls"`
	Advice            []aesBenchAdvice `json:"advice,omitempty"`
	AdvisorUS         int64            `json:"advisor_total_us"`
	AdvisorMaxUS      int64            `json:"advisor_max_us"`
}

func TestAESBenchLive(t *testing.T) {
	if os.Getenv("OHGO_AES_BENCH") != "1" {
		t.Skip("set OHGO_AES_BENCH=1 to run the live adaptive-experience benchmark")
	}
	root := strings.TrimSpace(os.Getenv("AES_BENCH_ROOT"))
	if root == "" {
		t.Fatal("AES_BENCH_ROOT is required")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	label := envOr("AES_BENCH_LABEL", "run")
	phase := envOr("AES_BENCH_PHASE", "cold")
	prompt := envOr("AES_BENCH_PROMPT", "Check this project and analyze what this project is about?")
	modelRef := envOr("AES_BENCH_MODEL", "groq/openai/gpt-oss-120b")
	authority := strings.ToLower(envOr("AES_BENCH_AUTHORITY", "ask"))
	maxTurns := envInt("AES_BENCH_MAX_TURNS", 12)
	projectID := strings.TrimSpace(os.Getenv("AES_BENCH_PROJECT_ID"))

	registry := ai.NewRegistry()
	model, err := catalog.Resolve(modelRef)
	if err != nil {
		model, err = dynamicResolver(context.Background(), registry)(modelRef)
	}
	if err != nil {
		t.Fatalf("resolve model %q: %v", modelRef, err)
	}
	ws, err := workspace.Parse(authority)
	if err != nil {
		t.Fatalf("authority %q: %v", authority, err)
	}

	settings := config.Default()
	opts := options{
		root:        root,
		workspace:   ws,
		model:       model,
		maxTokens:   resolveMaxTokens(settings, model),
		maxTurns:    maxTurns,
		maxDepth:    1,
		settings:    settings,
		autoApprove: true,
	}

	started := time.Now().UTC()
	var (
		observed *tui.ExperienceObservation
		usageLog = &agent.UsageLog{}
	)
	usageLog.SetCost(model.Cost)

	loop, err := buildLoop(opts, registry, io.Discard, nil, nil, nil)
	if err != nil {
		t.Fatalf("build loop: %v", err)
	}

	program := tui.NewProgram(loop, model.Ref(), model.ContextWindow)
	program.SetUsageLog(usageLog)
	// The production hosts install the workspace root for the pre-run target
	// snapshot and completion context. Without it the benchmark built the task
	// shape with empty repository facts, which made a code-project survey look
	// satisfied by documentation and metadata alone. This is the same seam the
	// interactive and one-shot hosts use.
	program.SetCompletionContext(root, structuralResolver(loop.Tools()))
	// The production TUI installs workspace hooks. The benchmark needs the same
	// live authority on the task shape without a full harness; this minimal
	// owner reports the fixed ASK authority and performs no transition.
	program.SetWorkspaceSwitcher(tui.WorkspaceHooks{
		Current: func() string { return ws.String() },
		Switch:  func(string) error { return nil },
		PrepareTurn: func(text string) tui.TurnAuthority {
			return tui.TurnAuthority{Active: strings.ToUpper(ws.String()), Intent: strings.ToUpper(string(workspace.Classify(text)))}
		},
	})
	controller := installTaskContinuity(program, root, loop.Tools(), func() workspace.Workspace { return ws })
	var advice []aesBenchAdvice
	engine := installBenchExperience(program, root, projectID, func(observation tui.ExperienceObservation) {
		copied := observation
		observed = &copied
	}, func(request cognition.AdviceRequest, result cognition.Advice, durationUS int64) {
		entry := aesBenchAdvice{
			DurationUS:  durationUS,
			Shape:       request.Shape.Summary(),
			Sufficiency: string(request.Sufficiency.State),
			Preferred:   append([]string(nil), result.PreferredTools...),
			Hints:       append([]string(nil), result.Hints...),
			Matched:     append([]string(nil), result.Matched...),
		}
		for _, tool := range request.Tools {
			entry.Offered = append(entry.Offered, tool.Name())
		}
		if len(result.PreferredTools) > 0 && len(entry.Offered) > 0 {
			entry.Reorder = entry.Offered[0] != result.PreferredTools[0]
		}
		advice = append(advice, entry)
	})

	runErr := ""
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				runErr = fmt.Sprintf("panic: %v", recovered)
			}
		}()
		program.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(prompt)})
		_, cmd := program.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if cmd == nil {
			runErr = "submit produced no command"
			return
		}
		msg := cmd()
		if msg != nil {
			program.Update(msg)
		}
	}()
	finished := time.Now().UTC()

	report := agent.ProjectionReport{}
	if observed != nil {
		report = observed.Projection
	}
	if report.Sufficiency == "" {
		if last, ok := controller.LastProjection(); ok {
			report = last
		}
	}
	turns := []agent.Turn(nil)
	if observed != nil {
		turns = observed.Turns
	}
	status, review := "", ""
	if current, ok := controller.Current(); ok {
		status = string(current.Status)
		review = current.ReviewReason
	}

	run := aesBenchRun{
		Label:             label,
		Phase:             phase,
		Model:             model.Ref(),
		Provider:          model.Provider,
		ModelID:           model.ID,
		Prompt:            prompt,
		Authority:         ws.String(),
		Root:              root,
		StartedAt:         started.Format(time.RFC3339Nano),
		FinishedAt:        finished.Format(time.RFC3339Nano),
		DurationMS:        finished.Sub(started).Milliseconds(),
		TaskStatus:        status,
		ReviewReason:      review,
		Sufficiency:       report.Sufficiency,
		EvidenceSummary:   report.Evidence,
		Shape:             report.Shape,
		Stage:             report.Stage,
		AnswerEligible:    report.AnswerEligible,
		Terminal:          report.Terminal,
		StopReason:        report.StopReason,
		StateBlockBytes:   report.StateBlockBytes,
		BytesBefore:       report.BytesBefore,
		BytesAfter:        report.BytesAfter,
		ExperienceApplied: report.ExperienceApplied,
		ExperienceHints:   report.ExperienceHints,
		ToolOrder:         report.ToolNames,
		Error:             runErr,
		Advice:            advice,
	}
	run.AdvisorCalls = len(advice)
	for _, item := range advice {
		if len(item.Matched) > 0 {
			run.AdvisorApplied++
		}
		if item.Reorder {
			run.AdvisorReorder++
		}
		run.AdvisorUS += item.DurationUS
		if item.DurationUS > run.AdvisorMaxUS {
			run.AdvisorMaxUS = item.DurationUS
		}
	}

	toolSeen := map[string]bool{}
	for i, turn := range turns {
		entry := aesBenchTurn{
			Index:      i,
			Usage:      aesBenchUsage{Input: turn.Assistant.Usage.Input, Output: turn.Assistant.Usage.Output, CacheRead: turn.Assistant.Usage.CacheRead, CacheWrite: turn.Assistant.Usage.CacheWrite},
			StopReason: string(turn.Assistant.StopReason),
			TextChars:  len(turn.Assistant.Text()),
		}
		if turn.Err != nil {
			entry.Err = turn.Err.Error()
		}
		for _, outcome := range turn.Results {
			entry.ToolCalls = append(entry.ToolCalls, outcome.ToolName)
			if !toolSeen[outcome.ToolName] {
				toolSeen[outcome.ToolName] = true
				run.ToolsUsed = append(run.ToolsUsed, outcome.ToolName)
			}
		}
		run.ModelRequests++
		run.ToolCalls += len(turn.Results)
		run.InputTokens += turn.Assistant.Usage.Input
		run.OutputTokens += turn.Assistant.Usage.Output
		run.CacheReadTokens += turn.Assistant.Usage.CacheRead
		run.CacheWriteTokens += turn.Assistant.Usage.CacheWrite
		if turn.Assistant.Usage.Input > run.PeakInput {
			run.PeakInput = turn.Assistant.Usage.Input
		}
		run.Turns = append(run.Turns, entry)
		if text := strings.TrimSpace(turn.Assistant.Text()); text != "" {
			run.Answer = text
			run.AnswerChars = len(text)
		}
	}
	run.RepeatedProbes = countRepeatedProbes(turns)
	if head, statusText, ok := gitState(root); ok {
		run.GitHead, run.GitStatus = head, statusText
	}

	for _, summary := range engine.List("") {
		run.Records = append(run.Records, aesBenchRecord{
			ID:             summary.ID,
			Status:         string(summary.Status),
			Scope:          string(summary.Scope),
			Kind:           string(summary.Kind),
			LessonKind:     summary.LessonKind,
			Statement:      summary.Statement,
			Hint:           summary.Hint,
			Context:        summary.Context,
			Observations:   summary.Observations,
			Contradictions: summary.Contradictions,
			Regression:     summary.Regression,
		})
	}
	run.ExperienceFile = filepath.Join(root, ".ohgo", "experience.json")
	if info, err := os.Stat(run.ExperienceFile); err == nil {
		run.ExperienceBytes = info.Size()
	}

	if out := strings.TrimSpace(os.Getenv("AES_BENCH_OUT")); out != "" {
		if err := appendBenchRun(out, run); err != nil {
			t.Fatalf("write result: %v", err)
		}
	} else {
		data, _ := json.MarshalIndent(run, "", "  ")
		t.Logf("%s", data)
	}
	if run.Error != "" {
		t.Fatalf("benchmark run failed: %s", run.Error)
	}
}

// installBenchExperience is the production installExperience wiring with one
// added read-only capture callback. It constructs the same engine, enables the
// same AutoPromote policy, and calls the same domain observation mapping. The
// callback runs before Observe and never changes the observation.
func installBenchExperience(program *tui.Program, root, projectID string, capture func(tui.ExperienceObservation), onAdvice func(cognition.AdviceRequest, cognition.Advice, int64)) *experience.Engine {
	cfg := experience.Config{
		ProjectRoot: root,
		ProjectID:   projectID,
		AutoPromote: true,
	}
	if dir, err := state.Dir(); err == nil {
		cfg.GlobalPath = filepath.Join(dir, "experience", "global.json")
	}
	engine := experience.New(cfg)
	advisor := cognition.ExperienceAdvisor(engine)
	if onAdvice != nil {
		advisor = benchAdvisor{inner: engine, observe: onAdvice}
	}
	program.SetExperienceHooks(tui.ExperienceHooks{
		Advisor: advisor,
		Observe: func(observation tui.ExperienceObservation) {
			defer func() {
				if recover() != nil {
					program.PublishNotice("experience: observation failed")
				}
			}()
			if capture != nil {
				capture(observation)
			}
			if _, err := engine.Observe(toExperienceObservation(observation)); err != nil {
				program.PublishNotice("experience: " + err.Error())
			}
		},
	})
	return engine
}

// benchAdvisor observes the advisor boundary and returns exactly what the
// engine returned. It cannot change the advice.
type benchAdvisor struct {
	inner   cognition.ExperienceAdvisor
	observe func(cognition.AdviceRequest, cognition.Advice, int64)
}

func (b benchAdvisor) Advise(request cognition.AdviceRequest) cognition.Advice {
	start := time.Now()
	result := b.inner.Advise(request)
	if b.observe != nil {
		b.observe(request, result, time.Since(start).Microseconds())
	}
	return result
}

// countRepeatedProbes counts an identical successful call signature repeated
// without an intervening mutation, the same relation the extractor uses.
func countRepeatedProbes(turns []agent.Turn) int {
	first := map[string]bool{}
	position, lastMutation := 0, -1
	repeats := 0
	for _, turn := range turns {
		for _, outcome := range turn.Results {
			position++
			if outcome.ToolName == "todo" || outcome.ToolName == "artifact" || outcome.ToolName == "task" {
				continue
			}
			if outcome.Result.Mutates || (outcome.Authorization != nil && outcome.Authorization.Mutates) {
				lastMutation = position
			}
			if outcome.Result.IsError || outcome.Refused() {
				continue
			}
			signature := agent.CallSignature(outcome.ToolName, outcome.Args)
			if first[signature] {
				if lastMutation < position {
					repeats++
				}
				continue
			}
			first[signature] = true
		}
	}
	return repeats
}

func appendBenchRun(path string, run aesBenchRun) error {
	data, err := json.Marshal(run)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	if _, err := file.Write(append(data, '\n')); err != nil {
		return err
	}
	return nil
}

func gitState(root string) (head, status string, ok bool) {
	head = strings.TrimSpace(benchRunGit(root, "rev-parse", "HEAD"))
	status = strings.TrimSpace(benchRunGit(root, "status", "--porcelain"))
	if head == "" {
		return "", "", false
	}
	return head, status, true
}

func benchRunGit(root string, args ...string) string {
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	out, _ := cmd.Output()
	return string(out)
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envInt(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	var parsed int
	if _, err := fmt.Sscanf(value, "%d", &parsed); err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}
