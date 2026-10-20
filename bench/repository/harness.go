package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/ai"
	"github.com/PizenLabs/ohgo/tools"
)

// benchmarkPrompt is identical for every track. It states the answer contract
// but never names a discovery tool, so the tool set is the only variable.
const benchmarkPrompt = `You are answering a repository-discovery benchmark question about a read-only Go repository.
Use only the tools provided. Do not modify files, run commands, or use the network.

When you have the answer, end with exactly one line:
FINAL: {"status":"exact|ambiguous|not_found","targets":[{"path":"repository/relative/path","symbol":"SymbolName","start_line":1,"end_line":2}]}

Status rules:
- exact: every requested target was found without ambiguity. Multiple requested targets may be listed together.
- ambiguous: the question asks for all declarations of a name and more than one exists. List all of them.
- not_found: no declaration with the requested exact name exists. targets must be empty.

Paths are repository-relative. Include every target the question asks for. Do not include a target you did not verify.`

// Track is one controlled discovery environment.
type Track struct {
	Name string
	// NewTools builds the tools the model may call. Every tool is read-only.
	NewTools func(root string) []agent.Tool
}

// TrackA is conventional lexical discovery.
func trackA(root string) []agent.Tool {
	return []agent.Tool{
		tools.NewReadTool(root),
		tools.NewGlobTool(root),
		tools.NewGrepTool(root),
		tools.NewLsTool(root),
	}
}

// TrackB is structural discovery. It deliberately omits grep and glob so the
// measured effect is the coordinate primitive, not a richer tool set.
func trackB(root string) []agent.Tool {
	return []agent.Tool{
		tools.NewReadTool(root),
		tools.NewSymbolsTool(root),
		tools.NewLsTool(root),
	}
}

// TrackC is the practical hybrid: both mechanisms are present and the model
// chooses per intent.
func trackC(root string) []agent.Tool {
	return []agent.Tool{
		tools.NewReadTool(root),
		tools.NewSymbolsTool(root),
		tools.NewGrepTool(root),
		tools.NewGlobTool(root),
		tools.NewLsTool(root),
	}
}

// Tracks returns the three compared environments in stable order.
func Tracks() []Track {
	return []Track{
		{Name: "A-lexical", NewTools: trackA},
		{Name: "B-structural", NewTools: trackB},
		{Name: "C-hybrid", NewTools: trackC},
	}
}

// RunConfig is the controlled part of a model run.
type RunConfig struct {
	Model       ai.Model
	Stream      ai.StreamFn
	MaxTokens   int
	MaxTurns    int
	Temperature float64
	Timeout     time.Duration
}

// CallRecord is one tool invocation and the measurements it produced.
type CallRecord struct {
	Index int    `json:"index"`
	Tool  string `json:"tool"`
	// Args is the exact map the model produced. It is kept because query
	// choice is part of discovery efficiency.
	Args map[string]any `json:"args"`
	// IsError reports a tool-level failure, not an evaluation failure.
	IsError bool `json:"is_error"`
	// DurationMS is the tool's own execution time.
	DurationMS int64 `json:"duration_ms"`
	// OffsetMS is when the call finished relative to the run start.
	OffsetMS int64 `json:"offset_ms"`
	// ResultBytes and ResultLines are the context the tool added.
	ResultBytes int `json:"result_bytes"`
	ResultLines int `json:"result_lines"`
	// CandidateFiles are file paths surfaced by this call.
	CandidateFiles []string `json:"candidate_files,omitempty"`
	// ReportedFileTotal is a tool-reported total when the output is paged.
	ReportedFileTotal int `json:"reported_file_total,omitempty"`
	// OpenedFile is set for read calls.
	OpenedFile string `json:"opened_file,omitempty"`
	// LinesInspected is set for read calls.
	LinesInspected int `json:"lines_inspected,omitempty"`
	// RevealedTargets lists expected target indices this result made visible.
	RevealedTargets []int `json:"revealed_targets,omitempty"`
}

// Run is one task x track measurement.
type Run struct {
	TaskID   string `json:"task_id"`
	Category string `json:"category"`
	Track    string `json:"track"`
	Model    string `json:"model"`
	Repeat   int    `json:"repeat"`

	Success        bool   `json:"success"`
	Outcome        string `json:"outcome"`
	StatusReported string `json:"status_reported"`
	AnswerFormat   string `json:"answer_format"`

	Turns          int `json:"turns"`
	ToolCalls      int `json:"tool_calls"`
	DiscoveryCalls int `json:"discovery_calls"`
	FilesOpened    int `json:"files_opened"`
	LinesInspected int `json:"lines_inspected"`
	ContextBytes   int `json:"context_bytes"`

	CandidateFiles       int `json:"candidate_files"`
	IrrelevantCandidates int `json:"irrelevant_candidates"`
	ReportedCandidates   int `json:"reported_candidates"`

	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
	CacheReadTokens  int `json:"cache_read_tokens"`
	CacheWriteTokens int `json:"cache_write_tokens"`

	CallsToFirstTarget  int   `json:"calls_to_first_target"`
	CallsToAllTargets   int   `json:"calls_to_all_targets"`
	TimeToFirstTargetMS int64 `json:"time_to_first_target_ms"`
	TimeToAllTargetsMS  int64 `json:"time_to_all_targets_ms"`
	DurationMS          int64 `json:"duration_ms"`

	MatchedTargets int `json:"matched_targets"`
	MissingTargets int `json:"missing_targets"`
	ExtraTargets   int `json:"extra_targets"`
	LineExact      int `json:"line_exact"`

	TargetsReported []ReportedTarget `json:"targets_reported,omitempty"`
	FinalText       string           `json:"final_text,omitempty"`
	Calls           []CallRecord     `json:"calls,omitempty"`
	Error           string           `json:"error,omitempty"`
}

// ReportedTarget is the normalized form of one target in the final answer.
type ReportedTarget struct {
	Path      string `json:"path"`
	Symbol    string `json:"symbol,omitempty"`
	StartLine int    `json:"start_line,omitempty"`
	EndLine   int    `json:"end_line,omitempty"`
}

// recorder collects tool-level measurements for one run.
type recorder struct {
	mu sync.Mutex
	// root is the fixture root, used to normalize absolute paths.
	root string
	// expected is the task ground truth, used to mark target revelation.
	expected []Target
	calls    []CallRecord
	// start is the run's wall-clock origin.
	start time.Time
}

func newRecorder(root string, expected []Target) *recorder {
	return &recorder{root: root, expected: expected, start: time.Now()}
}

// recordingTool wraps a real tool. It never changes behavior: the wrapped
// Execute is called exactly once with the model's own arguments.
type recordingTool struct {
	agent.Tool
	rec *recorder
}

// Execute implements agent.Tool.
func (t *recordingTool) Execute(ctx agent.Context, args map[string]any) agent.Result {
	start := time.Now()
	result := t.Tool.Execute(ctx, args)
	t.rec.record(t.Tool.Name(), args, result, start)
	return result
}

func (r *recorder) record(name string, args map[string]any, result agent.Result, start time.Time) {
	files, total := surfacedFiles(name, args, result, r.root)
	lines := 0
	opened := ""
	if name == "read" {
		lines = countNumberedLines(result.Text)
		if raw, ok := args["path"].(string); ok {
			opened = normalizePath(raw, r.root)
		}
	}
	revealed := r.reveal(result.Text, opened)

	r.mu.Lock()
	defer r.mu.Unlock()
	index := len(r.calls)
	r.calls = append(r.calls, CallRecord{
		Index:             index,
		Tool:              name,
		Args:              args,
		IsError:           result.IsError,
		DurationMS:        time.Since(start).Milliseconds(),
		OffsetMS:          time.Since(r.start).Milliseconds(),
		ResultBytes:       len(result.Text),
		ResultLines:       textLines(result.Text),
		CandidateFiles:    files,
		ReportedFileTotal: total,
		OpenedFile:        opened,
		LinesInspected:    lines,
		RevealedTargets:   revealed,
	})
}

// reveal reports which expected targets are visible in this result. For a
// coordinate tool the path is enough; for a textual task the line text must
// also be present. This measures how early discovery reached the answer, not
// whether the model understood it.
func (r *recorder) reveal(text, opened string) []int {
	var out []int
	for i, target := range r.expected {
		if target.Symbol != "" && strings.Contains(text, target.Symbol) {
			out = append(out, i)
			continue
		}
		pathSeen := strings.Contains(text, target.Path) || (opened != "" && opened == target.Path)
		if pathSeen && (target.Contains == "" || strings.Contains(text, target.Contains)) {
			out = append(out, i)
		}
	}
	return out
}

func (r *recorder) snapshot() []CallRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]CallRecord, len(r.calls))
	copy(out, r.calls)
	return out
}

func (r *recorder) elapsed() time.Duration {
	return time.Since(r.start)
}

// surfacedFiles extracts the file paths a tool result put in front of the
// model. It also returns a reported total when the tool paged its output.
func surfacedFiles(name string, args map[string]any, result agent.Result, root string) ([]string, int) {
	details, _ := result.Details.(map[string]any)
	switch name {
	case "symbols":
		var files []string
		if coords, ok := details["coordinates"].([]map[string]any); ok {
			for _, coord := range coords {
				if path, ok := coord["path"].(string); ok {
					files = append(files, normalizePath(path, root))
				}
			}
		}
		total := len(files)
		if n, ok := details["results"].(int); ok {
			total = n
		}
		return dedupe(files), total
	case "grep":
		var files []string
		for _, line := range strings.Split(result.Text, "\n") {
			if line == "" || strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
				continue
			}
			if strings.HasSuffix(line, ":") && !strings.Contains(line, "(") {
				files = append(files, normalizePath(strings.TrimSuffix(line, ":"), root))
			}
		}
		total := len(files)
		if n, ok := details["files"].(int); ok {
			total = n
		}
		return dedupe(files), total
	case "glob":
		var files []string
		for _, line := range strings.Split(result.Text, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "[") || line == "No files matched." {
				continue
			}
			files = append(files, normalizePath(line, root))
		}
		total := len(files)
		if n, ok := details["matches"].(int); ok {
			total = n
		}
		return dedupe(files), total
	case "ls":
		var files []string
		dir := ""
		if raw, ok := args["path"].(string); ok {
			dir = raw
		}
		for _, line := range strings.Split(result.Text, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasSuffix(line, "/") || strings.Contains(line, " -> ") {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) == 0 {
				continue
			}
			path := fields[0]
			if dir != "" && dir != "." {
				path = filepath.ToSlash(filepath.Join(dir, path))
			}
			files = append(files, normalizePath(path, root))
		}
		return dedupe(files), len(files)
	}
	return nil, 0
}

func dedupe(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func normalizePath(path, root string) string {
	path = strings.TrimSpace(strings.Trim(path, "`'\""))
	path = strings.ReplaceAll(path, "\\", "/")
	path = strings.TrimPrefix(path, "./")
	if root != "" {
		absRoot := filepath.ToSlash(root)
		path = strings.TrimPrefix(path, strings.TrimSuffix(absRoot, "/")+"/")
	}
	return path
}

func textLines(text string) int {
	if text == "" {
		return 0
	}
	return strings.Count(text, "\n") + 1
}

// countNumberedLines counts read output lines of the form "123\tcontent".
func countNumberedLines(text string) int {
	count := 0
	for _, line := range strings.Split(text, "\n") {
		tab := strings.IndexByte(line, '\t')
		if tab <= 0 {
			continue
		}
		if _, err := parsePositiveInt(line[:tab]); err == nil {
			count++
		}
	}
	return count
}

func parsePositiveInt(raw string) (int, error) {
	var value int
	if raw == "" {
		return 0, fmt.Errorf("empty")
	}
	for _, c := range raw {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("not an integer")
		}
		value = value*10 + int(c-'0')
	}
	if value < 1 {
		return 0, fmt.Errorf("not positive")
	}
	return value, nil
}

// RunTask executes one task in one track against root.
func RunTask(ctx context.Context, root string, task Task, track Track, cfg RunConfig) Run {
	run := Run{
		TaskID:   task.ID,
		Category: task.Category,
		Track:    track.Name,
		Model:    cfg.Model.Provider + "/" + cfg.Model.ID,
	}

	rec := newRecorder(root, task.Targets)
	toolSet := make([]agent.Tool, 0, 5)
	for _, tool := range track.NewTools(root) {
		toolSet = append(toolSet, &recordingTool{Tool: tool, rec: rec})
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if cfg.Timeout > 0 {
		var timeoutCancel context.CancelFunc
		runCtx, timeoutCancel = context.WithTimeout(runCtx, cfg.Timeout)
		defer timeoutCancel()
	}

	turnCount := 0
	loop, err := agent.New(agent.Config{
		Model:     cfg.Model,
		Stream:    cfg.Stream,
		System:    []ai.SystemPromptBlock{{Text: benchmarkPrompt, Cache: true}},
		Tools:     toolSet,
		MaxTokens: cfg.MaxTokens,
		OnTurn: func(agent.Turn) {
			turnCount++
			if cfg.MaxTurns > 0 && turnCount >= cfg.MaxTurns {
				cancel()
			}
		},
	})
	if err != nil {
		run.Error = err.Error()
		run.Outcome = "harness_error"
		return run
	}

	start := time.Now()
	turns, runErr := loop.Run(runCtx, []agent.Message{agent.UserMessage{Text: task.Prompt}})
	run.DurationMS = time.Since(start).Milliseconds()
	run.Calls = rec.snapshot()
	run.Turns = len(turns)
	if runErr != nil && runErr != context.Canceled && runErr != context.DeadlineExceeded {
		run.Error = runErr.Error()
	}

	var finalText strings.Builder
	for _, turn := range turns {
		run.InputTokens += turn.Assistant.Usage.Input
		run.OutputTokens += turn.Assistant.Usage.Output
		run.CacheReadTokens += turn.Assistant.Usage.CacheRead
		run.CacheWriteTokens += turn.Assistant.Usage.CacheWrite
		for _, block := range turn.Assistant.Content {
			if text, ok := block.(ai.TextContent); ok && text.Text != "" {
				finalText.WriteString(text.Text)
				finalText.WriteByte('\n')
			}
		}
	}
	run.FinalText = strings.TrimSpace(finalText.String())

	fillCallMetrics(&run, root, task)
	answer, format := parseFinalAnswer(run.FinalText, root)
	run.AnswerFormat = format
	run.StatusReported = answer.Status
	run.TargetsReported = answer.Targets
	score := scoreAnswer(task, answer, root)
	run.Success = score.success
	run.Outcome = score.outcome
	if run.Error != "" && !run.Success {
		run.Outcome = "provider_error"
	}
	run.MatchedTargets = score.matched
	run.MissingTargets = score.missing
	run.ExtraTargets = score.extra
	run.LineExact = score.lineExact
	return run
}

func fillCallMetrics(run *Run, root string, task Task) {
	candidateSet := map[string]bool{}
	openedSet := map[string]bool{}
	revealed := map[int]bool{}
	firstCall := -1
	firstOffset := int64(0)
	allCall := -1
	allOffset := int64(0)
	for _, call := range run.Calls {
		run.ToolCalls++
		if call.Tool != "read" {
			run.DiscoveryCalls++
		}
		run.ContextBytes += call.ResultBytes
		run.ReportedCandidates += call.ReportedFileTotal
		run.LinesInspected += call.LinesInspected
		if call.OpenedFile != "" {
			openedSet[call.OpenedFile] = true
		}
		for _, file := range call.CandidateFiles {
			candidateSet[file] = true
		}
		for _, index := range call.RevealedTargets {
			revealed[index] = true
			if firstCall < 0 {
				firstCall = call.Index
				firstOffset = call.OffsetMS
			}
		}
		if len(task.Targets) > 0 && len(revealed) == len(task.Targets) && allCall < 0 {
			allCall = call.Index
			allOffset = call.OffsetMS
		}
	}
	if firstCall >= 0 {
		run.CallsToFirstTarget = firstCall + 1
		run.TimeToFirstTargetMS = firstOffset
	}
	if allCall >= 0 {
		run.CallsToAllTargets = allCall + 1
		run.TimeToAllTargetsMS = allOffset
	}
	run.FilesOpened = len(openedSet)
	run.CandidateFiles = len(candidateSet)
	expectedPaths := map[string]bool{}
	for _, target := range task.Targets {
		expectedPaths[normalizePath(target.Path, root)] = true
	}
	for file := range candidateSet {
		if !expectedPaths[file] {
			run.IrrelevantCandidates++
		}
	}
}

// finalAnswer is the machine-readable answer contract.
type finalAnswer struct {
	Status  string           `json:"status"`
	Targets []ReportedTarget `json:"targets"`
}

// rawTarget accepts the spellings a model commonly uses while keeping the
// evaluator's canonical form strict.
type rawTarget struct {
	Path      string `json:"path"`
	File      string `json:"file"`
	Symbol    string `json:"symbol"`
	Name      string `json:"name"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Line      int    `json:"line"`
}

type rawAnswer struct {
	Status  string      `json:"status"`
	Targets []rawTarget `json:"targets"`
}

func parseFinalAnswer(text, root string) (finalAnswer, string) {
	var out finalAnswer
	if strings.TrimSpace(text) == "" {
		return out, "missing"
	}

	// Prefer the explicit FINAL line. The decoder stops at the end of the
	// first JSON value, so trailing prose is harmless.
	lower := strings.ToLower(text)
	if idx := strings.LastIndex(lower, "final:"); idx >= 0 {
		rest := text[idx+len("final:"):]
		if ans, ok := decodeAnswer(rest, root); ok {
			return ans, "json"
		}
	}

	// A fenced or unlabelled JSON object is still a valid machine answer.
	for _, candidate := range jsonCandidates(text) {
		if ans, ok := decodeAnswer(candidate, root); ok {
			return ans, "json"
		}
	}

	// Last resort: recover paths and symbols from prose. This keeps a
	// formatting mistake from being counted as a discovery failure, but the
	// format is recorded so the report can separate the two.
	return lenientAnswer(text, root), "lenient"
}

func decodeAnswer(text, root string) (finalAnswer, bool) {
	text = strings.TrimSpace(text)
	start := strings.IndexByte(text, '{')
	if start < 0 {
		return finalAnswer{}, false
	}
	var raw rawAnswer
	decoder := json.NewDecoder(strings.NewReader(text[start:]))
	if err := decoder.Decode(&raw); err != nil {
		return finalAnswer{}, false
	}
	if raw.Status == "" && len(raw.Targets) == 0 {
		return finalAnswer{}, false
	}
	out := finalAnswer{Status: normalizeStatus(raw.Status)}
	seen := map[string]bool{}
	for _, target := range raw.Targets {
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
		endLine := target.EndLine
		if endLine == 0 {
			endLine = startLine
		}
		normalized := ReportedTarget{
			Path:      normalizePath(path, root),
			Symbol:    strings.TrimSpace(strings.Trim(symbol, "`'\"")),
			StartLine: startLine,
			EndLine:   endLine,
		}
		key := fmt.Sprintf("%s|%s|%d|%d", normalized.Path, normalized.Symbol, normalized.StartLine, normalized.EndLine)
		if seen[key] {
			continue
		}
		seen[key] = true
		out.Targets = append(out.Targets, normalized)
	}
	return out, true
}

func jsonCandidates(text string) []string {
	var out []string
	// The answer may be a bare JSON object, or the same object after prose.
	if idx := strings.Index(text, "{"); idx >= 0 {
		out = append(out, text[idx:])
	}
	// Fenced blocks are the common alternative.
	for {
		start := strings.Index(text, "```")
		if start < 0 {
			break
		}
		rest := text[start+3:]
		end := strings.Index(rest, "```")
		if end < 0 {
			break
		}
		block := strings.TrimSpace(rest[:end])
		block = strings.TrimPrefix(block, "json")
		out = append(out, block)
		text = rest[end+3:]
	}
	return out
}

func normalizeStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "ok", "found", "exact":
		return "exact"
	case "ambiguous", "multiple":
		return "ambiguous"
	case "not_found", "not found", "missing", "absent":
		return "not_found"
	case "truncated":
		return "truncated"
	default:
		return strings.ToLower(strings.TrimSpace(status))
	}
}

func lenientAnswer(text, root string) finalAnswer {
	lower := strings.ToLower(text)
	status := ""
	if strings.Contains(lower, "not_found") || strings.Contains(lower, "not found") ||
		strings.Contains(lower, "does not exist") || strings.Contains(lower, "no declaration") {
		status = "not_found"
	}
	var targets []ReportedTarget
	seen := map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
		for _, field := range strings.Fields(line) {
			field = strings.Trim(field, "`'\"(),;.")
			path, rest, ok := strings.Cut(field, ":")
			if !ok || !(strings.HasSuffix(path, ".go") || strings.HasSuffix(path, ".md") || path == "Makefile") {
				continue
			}
			path = normalizePath(path, root)
			if seen[path] {
				continue
			}
			seen[path] = true
			target := ReportedTarget{Path: path}
			if rest != "" {
				if start, end, ok := parseLineRange(rest); ok {
					target.StartLine, target.EndLine = start, end
				}
			}
			targets = append(targets, target)
		}
	}
	if status == "" && len(targets) > 0 {
		status = "exact"
	}
	return finalAnswer{Status: status, Targets: targets}
}

func parseLineRange(raw string) (int, int, bool) {
	raw = strings.Trim(raw, "`'\"")
	startRaw, endRaw, hasEnd := strings.Cut(raw, "-")
	start, err := parsePositiveInt(startRaw)
	if err != nil {
		return 0, 0, false
	}
	end := start
	if hasEnd {
		if parsed, err := parsePositiveInt(endRaw); err == nil {
			end = parsed
		}
	}
	return start, end, true
}

type answerScore struct {
	success   bool
	outcome   string
	matched   int
	missing   int
	extra     int
	lineExact int
}

func scoreAnswer(task Task, answer finalAnswer, root string) answerScore {
	var score answerScore
	if answer.Status == "" && len(answer.Targets) == 0 {
		score.outcome = "no_answer"
		return score
	}
	if task.ExpectedStatus == "not_found" {
		if answer.Status == "not_found" && len(answer.Targets) == 0 {
			score.success = true
			score.outcome = "correct_not_found"
			return score
		}
		if len(answer.Targets) > 0 {
			score.outcome = "false_positive"
			score.extra = len(answer.Targets)
			return score
		}
		score.outcome = "wrong_status"
		return score
	}

	used := make([]bool, len(answer.Targets))
	for _, expected := range task.Targets {
		found := false
		for i, reported := range answer.Targets {
			if used[i] {
				continue
			}
			if targetMatches(expected, reported, root) {
				used[i] = true
				found = true
				score.matched++
				if lineOverlaps(expected, reported, 2) {
					score.lineExact++
				}
				break
			}
		}
		if !found {
			score.missing++
		}
	}
	for _, u := range used {
		if !u {
			score.extra++
		}
	}

	switch {
	case score.missing > 0 && score.extra > 0:
		score.outcome = "wrong_target"
	case score.missing > 0:
		score.outcome = "false_negative"
	case score.extra > 0:
		score.outcome = "false_positive"
	case answer.Status != task.ExpectedStatus:
		score.outcome = "status_mismatch"
	default:
		score.success = true
		score.outcome = "correct"
	}
	return score
}

func targetMatches(expected Target, reported ReportedTarget, root string) bool {
	if normalizePath(expected.Path, root) != normalizePath(reported.Path, root) {
		return false
	}
	if expected.Symbol != "" {
		if !symbolMatches(expected, reported.Symbol) {
			return false
		}
	}
	if expected.Contains != "" && !lineOverlaps(expected, reported, 0) {
		return false
	}
	// A symbol answer with no line is not a coordinate answer. Keep it a miss
	// rather than silently accepting a half-answer.
	if expected.StartLine > 0 && reported.StartLine <= 0 {
		return false
	}
	return true
}

func symbolMatches(expected Target, reported string) bool {
	reported = strings.TrimSpace(strings.Trim(reported, "`'\""))
	if reported == "" {
		return false
	}
	candidates := append([]string{expected.Symbol}, expected.AcceptSymbols...)
	for _, candidate := range candidates {
		if reported == candidate {
			return true
		}
		// Accept package- or receiver-qualified variants without accepting a
		// merely similar name: the dot boundary is the discriminator.
		if strings.HasSuffix(reported, "."+candidate) || strings.HasSuffix(candidate, "."+reported) {
			return true
		}
	}
	return false
}

func lineOverlaps(expected Target, reported ReportedTarget, tolerance int) bool {
	if expected.StartLine <= 0 {
		return true
	}
	if reported.StartLine <= 0 {
		return false
	}
	end := reported.EndLine
	if end < reported.StartLine {
		end = reported.StartLine
	}
	start := expected.StartLine - tolerance
	stop := expected.EndLine + tolerance
	return reported.StartLine <= stop && end >= start
}

// accumulate folds one run into a model-independent aggregate.
func accumulate(runs []Run, key func(Run) string) map[string][]Run {
	out := map[string][]Run{}
	for _, run := range runs {
		out[key(run)] = append(out[key(run)], run)
	}
	return out
}

func median(values []int) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]int(nil), values...)
	sort.Ints(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 0 {
		return float64(sorted[mid-1]+sorted[mid]) / 2
	}
	return float64(sorted[mid])
}

func mean(values []int) float64 {
	if len(values) == 0 {
		return 0
	}
	sum := 0
	for _, value := range values {
		sum += value
	}
	return float64(sum) / float64(len(values))
}

func writeJSON(path string, value any) error {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}
