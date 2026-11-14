package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/tokens"
)

// Compaction settings.
//
// The defaults mirror the original's: a proportional reserve with a
// compaction strategy that summarises history once the window fills. The
// policy fields extend that behaviour without changing the meaning of the
// original thresholds: a configured ThresholdPercent still wins, and an
// unset policy keeps the window-minus-reserve trigger.
type Compaction struct {
	// Enabled disables compaction entirely when false.
	Enabled bool
	// Strategy selects the compaction behaviour.
	Strategy CompactionStrategy
	// ThresholdTokens is a fixed trigger. When set it takes priority over
	// ThresholdPercent and the reserve.
	ThresholdTokens int
	// ThresholdPercent is a percentage of the context window. Ignored when
	// ThresholdTokens is set.
	ThresholdPercent float64
	// ReserveTokens is the absolute budget held back from the window. When
	// unset, a proportional reserve of 15% is used if the default would be
	// impossible for the window.
	ReserveTokens int
	// KeepRecent is the number of trailing messages preserved verbatim
	// through compaction, so the model keeps its immediate working set.
	KeepRecent int
	// KeepRecentToolResults is the number of trailing tool results always
	// retained exactly by deterministic trimming. Zero uses the default.
	KeepRecentToolResults int
	// ExpectedOutputTokens is the completion budget included in the projected
	// request size. Zero derives a bounded value from the window, which keeps
	// a predictive trigger from assuming the model returns nothing.
	ExpectedOutputTokens int
	// SafetyMarginPercent is held back from the window for estimator error and
	// provider framing. Zero uses the default (5%).
	SafetyMarginPercent float64
	// MonitorPercent is the pressure band where the runtime starts measuring
	// more aggressively. Zero uses the default (50%).
	MonitorPercent float64
	// PreparePercent is the band where a compaction projection is prepared but
	// not yet applied. Zero derives it from MonitorPercent and the compact
	// threshold.
	PreparePercent float64
	// MaxAttempts bounds how many compactions may run in one turn before the
	// loop reports a truthful context-overflow failure. Zero uses the default
	// (3).
	MaxAttempts int
}

// CompactionStrategy selects how context is reduced.
type CompactionStrategy string

// Compaction strategies.
const (
	// StrategySummarize replaces older history with a summary.
	StrategySummarize CompactionStrategy = "summarize"
	// StrategyOff disables compaction.
	StrategyOff CompactionStrategy = "off"
)

// DefaultCompaction returns the built-in settings.
func DefaultCompaction() Compaction {
	return Compaction{
		Enabled:               true,
		Strategy:              StrategySummarize,
		KeepRecent:            8,
		KeepRecentToolResults: 2,
		SafetyMarginPercent:   5,
		MonitorPercent:        50,
		MaxAttempts:           3,
	}
}

// reserveProportionalFraction is the fallback fraction of the window held back
// when the configured reserve is impossible for that window.
const reserveProportionalFraction = 0.15

// Default policy values. They are named here so no scattered literal decides
// the trigger on its own.
const (
	defaultSafetyMarginPercent   = 5.0
	defaultMonitorPercent        = 50.0
	defaultKeepRecentToolResults = 2
	defaultCompactionAttempts    = 3
	minSafetyMarginTokens        = 512
	maxExpectedOutputTokens      = 8192
	minExpectedOutputTokens      = 256
	// minSummaryTokens is the floor for a semantic handover when the preserved
	// suffix already fills the trigger. It is a truthfulness bound, not a
	// promise that the request fits.
	minSummaryTokens = 64
	// defaultBoundToolBytes bounds one tool result when deterministic dedup
	// alone cannot reduce the context below the trigger.
	defaultBoundToolBytes = 1024
	// minBoundToolBytes is the emergency bound used when even the default
	// bound cannot make the request fit.
	minBoundToolBytes = 128
)

// Pressure is the runtime's context-pressure band.
//
// It is a measurement, not authority: no band changes what a role may do. The
// distinctions exist so the runtime can prepare and warn before it has to
// rewrite context on the critical path.
type Pressure string

// Pressure bands, lowest to highest.
const (
	// PressureNormal means the projected request has ample room.
	PressureNormal Pressure = "NORMAL"
	// PressureMonitor means the window is filling and the runtime should keep
	// measuring.
	PressureMonitor Pressure = "MONITOR"
	// PressurePrepare means a compaction projection should be prepared, but
	// the context still fits.
	PressurePrepare Pressure = "PREPARE"
	// PressureCompact means the projected request crosses the compaction
	// trigger and must be reduced before the next request.
	PressureCompact Pressure = "COMPACT"
	// PressureCritical means the projected request would exceed the window.
	// The next request must not be sent unreduced.
	PressureCritical Pressure = "CRITICAL"
)

// resolveBudgetReserveTokens computes the tokens held back from the window.
func resolveBudgetReserveTokens(contextWindow int, c Compaction) int {
	proportional := int(float64(contextWindow) * reserveProportionalFraction)
	if proportional < 1 {
		proportional = 1
	}
	if contextWindow <= 0 {
		return 0
	}

	reserve := c.ReserveTokens
	if reserve == 0 {
		return proportional
	}
	if reserve >= contextWindow {
		return proportional
	}
	return reserve
}

// resolveThresholdTokens computes the token count that triggers compaction.
func resolveThresholdTokens(contextWindow int, c Compaction) int {
	if contextWindow <= 0 {
		return 0
	}

	if c.ThresholdTokens > 0 {
		return clamp(1, contextWindow-1, c.ThresholdTokens)
	}

	if c.ThresholdPercent > 0 {
		pct := c.ThresholdPercent
		if pct > 99 {
			pct = 99
		}
		if pct < 1 {
			pct = 1
		}
		return int(float64(contextWindow) * pct / 100)
	}

	threshold := contextWindow - resolveBudgetReserveTokens(contextWindow, c)
	if threshold < 0 {
		threshold = 0
	}
	if threshold > contextWindow-1 {
		threshold = contextWindow - 1
	}
	return threshold
}

func clamp(lo, hi, value int) int {
	if value < lo {
		return lo
	}
	if value > hi {
		return hi
	}
	return value
}

func clampFloat(lo, hi, value float64) float64 {
	if value < lo {
		return lo
	}
	if value > hi {
		return hi
	}
	return value
}

// safetyMarginPercent resolves the configured margin.
func (c Compaction) safetyMarginPercent() float64 {
	if c.SafetyMarginPercent <= 0 {
		return defaultSafetyMarginPercent
	}
	return clampFloat(0.1, 50, c.SafetyMarginPercent)
}

// monitorPercent resolves the configured monitor band.
func (c Compaction) monitorPercent() float64 {
	if c.MonitorPercent <= 0 {
		return defaultMonitorPercent
	}
	return clampFloat(1, 95, c.MonitorPercent)
}

// preparePercent resolves the configured prepare band, never above the
// compaction trigger expressed as a window percentage.
func (c Compaction) preparePercent(window int) float64 {
	if window <= 0 {
		return 0
	}
	compactPercent := float64(resolveThresholdTokens(window, c)) / float64(window) * 100
	prepare := c.PreparePercent
	if prepare <= 0 {
		prepare = c.monitorPercent() + (compactPercent-c.monitorPercent())*0.6
	}
	if prepare < c.monitorPercent() {
		prepare = c.monitorPercent()
	}
	if prepare > compactPercent {
		prepare = compactPercent
	}
	return prepare
}

// expectedOutput resolves the completion budget included in the projection.
func (c Compaction) expectedOutput(window int) int {
	if c.ExpectedOutputTokens > 0 {
		return c.ExpectedOutputTokens
	}
	if window <= 0 {
		return 0
	}
	value := window / 8
	if value < minExpectedOutputTokens {
		value = minExpectedOutputTokens
	}
	if value > maxExpectedOutputTokens {
		value = maxExpectedOutputTokens
	}
	return value
}

// maxAttempts resolves the per-turn compaction attempt bound.
func (c Compaction) maxAttempts() int {
	if c.MaxAttempts > 0 {
		return c.MaxAttempts
	}
	return defaultCompactionAttempts
}

func (c Compaction) keepRecentToolResults() int {
	if c.KeepRecentToolResults > 0 {
		return c.KeepRecentToolResults
	}
	return defaultKeepRecentToolResults
}

// ShouldCompact reports whether the context has grown past the trigger.
//
// It is the legacy primitive, kept for callers that only have a context size.
// New callers should use MeasureRequest, which accounts for system overhead,
// tool definitions, the expected output, and a safety margin.
func ShouldCompact(contextTokens, contextWindow int, c Compaction) bool {
	if !c.Enabled || c.Strategy == StrategyOff || contextWindow <= 0 {
		return false
	}
	return contextTokens > resolveThresholdTokens(contextWindow, c)
}

// MeasureInput is the runtime's accounting for one prospective request.
//
// Every field is an estimate the caller owns. ProviderTokens is the last
// measured request total when the provider reported one; it is a floor, never
// a substitute for counting the stored conversation.
type MeasureInput struct {
	// ContextTokens is the stored conversation estimate.
	ContextTokens int
	// ProviderTokens is the provider-reported total of the previous request,
	// or zero when no usage was reported.
	ProviderTokens int
	// SystemTokens is the system-prompt overhead.
	SystemTokens int
	// ToolTokens is the tool-definition overhead.
	ToolTokens int
	// TaskStateTokens is the durable task projection overhead.
	TaskStateTokens int
	// ExpectedOutputTokens is the completion budget for this request. Zero
	// uses the policy default.
	ExpectedOutputTokens int
	// Window is the model's context limit.
	Window int
}

// RequestMeasurement is the predicted size of the next request and the
// pressure band it implies.
type RequestMeasurement struct {
	ContextTokens        int      `json:"contextTokens"`
	ProviderTokens       int      `json:"providerTokens,omitempty"`
	SystemTokens         int      `json:"systemTokens"`
	ToolTokens           int      `json:"toolTokens"`
	TaskStateTokens      int      `json:"taskStateTokens,omitempty"`
	ExpectedOutputTokens int      `json:"expectedOutputTokens"`
	SafetyMarginTokens   int      `json:"safetyMarginTokens"`
	Projected            int      `json:"projected"`
	Window               int      `json:"window"`
	Headroom             int      `json:"headroom"`
	Threshold            int      `json:"threshold"`
	Pressure             Pressure `json:"pressure"`
}

// MeasureRequest predicts the next request's size and classifies it.
//
// The projection deliberately adds every component the provider bills: stored
// conversation, system blocks, tool definitions, the durable task projection,
// the completion budget, and a safety margin. Using the context size alone is
// what let a large tool set or a generous output budget overflow a window that
// looked half empty.
func MeasureRequest(in MeasureInput, c Compaction) RequestMeasurement {
	window := in.Window
	if window <= 0 {
		return RequestMeasurement{}
	}
	safety := int(float64(window) * c.safetyMarginPercent() / 100)
	if safety < minSafetyMarginTokens && window > minSafetyMarginTokens {
		safety = minSafetyMarginTokens
	}
	if safety > window/4 {
		safety = window / 4
	}
	expected := in.ExpectedOutputTokens
	if expected <= 0 {
		expected = c.expectedOutput(window)
	}
	context := in.ContextTokens
	if in.ProviderTokens > context {
		context = in.ProviderTokens
	}
	estimated := in.ContextTokens + in.SystemTokens + in.ToolTokens + in.TaskStateTokens
	base := context
	if estimated > base {
		base = estimated
	}
	projected := base + expected + safety
	threshold := resolveThresholdTokens(window, c)
	unavoidable := expected + safety
	// The predictive line is the point where the non-context overhead leaves
	// the conversation room to grow. An explicit low threshold (for example a
	// 1.5k trigger on a 100k window) still means "compact when the stored
	// conversation is larger than 1.5k"; it must not fire merely because the
	// tool definitions and the completion budget exist.
	predictiveLine := threshold
	if predictiveLine < unavoidable {
		predictiveLine = window - unavoidable
	}
	if predictiveLine < 0 {
		predictiveLine = window
	}
	monitorLine := int(float64(window) * c.monitorPercent() / 100)
	prepareLine := int(float64(window) * c.preparePercent(window) / 100)
	if prepareLine < monitorLine {
		prepareLine = monitorLine
	}
	if prepareLine > predictiveLine {
		prepareLine = predictiveLine
	}

	pressure := PressureNormal
	switch {
	case projected >= window:
		pressure = PressureCritical
	case context >= threshold || projected >= predictiveLine:
		pressure = PressureCompact
	case projected >= prepareLine:
		pressure = PressurePrepare
	case projected >= monitorLine:
		pressure = PressureMonitor
	}

	headroom := window - projected
	if headroom < 0 {
		headroom = 0
	}
	return RequestMeasurement{
		ContextTokens:        in.ContextTokens,
		ProviderTokens:       in.ProviderTokens,
		SystemTokens:         in.SystemTokens,
		ToolTokens:           in.ToolTokens,
		TaskStateTokens:      in.TaskStateTokens,
		ExpectedOutputTokens: expected,
		SafetyMarginTokens:   safety,
		Projected:            projected,
		Window:               window,
		Headroom:             headroom,
		Threshold:            threshold,
		Pressure:             pressure,
	}
}

// ContextBudget is the conversation room left after every non-context cost.
// It is the tighter of the trigger and the window's real headroom, so a
// rebuilt context cannot overflow just because the trigger was generous.
func (m RequestMeasurement) ContextBudget() int {
	if m.Window <= 0 {
		return 0
	}
	budget := m.Window - m.SystemTokens - m.ToolTokens - m.ExpectedOutputTokens - m.SafetyMarginTokens
	if budget < 1 {
		return 1
	}
	if m.Threshold > 0 && budget > m.Threshold {
		budget = m.Threshold
	}
	return budget
}

// NeedsCompaction reports whether the measurement requires a rewrite before
// the next request.
func NeedsCompaction(m RequestMeasurement) bool {
	return m.Pressure == PressureCompact || m.Pressure == PressureCritical
}

// EstimateToolTokens estimates the wire cost of a tool set.
//
// Tool definitions are a fixed per-request cost that the conversation
// estimate never sees. A large MCP surface can consume a meaningful fraction
// of a small window, so the predictive trigger counts it explicitly.
func EstimateToolTokens(tools []Tool) int {
	if len(tools) == 0 {
		return 0
	}
	data, err := json.Marshal(Specs(tools))
	if err != nil {
		// A tool spec that cannot be serialized is a construction bug, not a
		// measurement failure. Fall back to names so the estimate errs low but
		// still reflects the surface.
		var names []string
		for _, tool := range tools {
			names = append(names, tool.Name())
		}
		return tokens.Count(strings.Join(names, ","), tokens.ModeEstimate)
	}
	return tokens.Count(string(data), tokens.ModeEstimate)
}

// EstimateSystemTokens estimates the wire cost of system prompt blocks.
func EstimateSystemTokens(blocks []ai.SystemPromptBlock) int {
	if len(blocks) == 0 {
		return 0
	}
	texts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if block.Text != "" {
			texts = append(texts, block.Text)
		}
	}
	return tokens.CountBlocks(texts, tokens.ModeEstimate)
}

// CompactionContextTokens floors a provider-reported context size by a local
// estimate of the stored conversation.
func CompactionContextTokens(providerContextTokens, storedConversationEstimate int) int {
	if storedConversationEstimate > providerContextTokens {
		return storedConversationEstimate
	}
	return providerContextTokens
}

// EstimateMessages approximates the token size of a message list.
//
// It counts only messages that reach the provider and includes tool-call
// arguments, which are billed on the wire even though they are not assistant
// prose. Counting only text let a model that called tools with large payloads
// grow the real request far past the estimate.
func EstimateMessages(messages []Message) int {
	texts := make([]string, 0, len(messages))
	for _, msg := range messages {
		if !sendsToLLM(msg) {
			continue
		}
		texts = append(texts, estimateMessageText(msg))
	}
	return tokens.CountBlocks(texts, tokens.ModeEstimate)
}

// sendsToLLM reports whether a message reaches the provider at all.
func sendsToLLM(msg Message) bool {
	_, ok := msg.toLLM()
	return ok
}

func estimateMessageText(msg Message) string {
	switch m := msg.(type) {
	case UserMessage:
		return m.Text
	case AssistantMessage:
		return assistantEstimateText(m.Message)
	case ToolResultMessage:
		return m.Text()
	case Notification:
		return ""
	default:
		return ""
	}
}

// assistantEstimateText includes visible text and every tool-call payload.
func assistantEstimateText(message ai.AssistantMessage) string {
	text := message.Text()
	if thinking := message.ThinkingText(); thinking != "" {
		text += "\n" + thinking
	}
	var calls strings.Builder
	for _, block := range message.Content {
		call, ok := block.(ai.ToolCall)
		if !ok {
			continue
		}
		calls.WriteString(call.Name)
		calls.WriteByte(' ')
		if data, err := json.Marshal(call.Arguments); err == nil {
			calls.Write(data)
		}
		calls.WriteByte('\n')
	}
	if calls.Len() == 0 {
		return text
	}
	return text + "\n" + calls.String()
}

// findValidCutPoints returns message indices that may begin a compacted
// region.
func findValidCutPoints(messages []Message) []int {
	var points []int
	for i, msg := range messages {
		switch msg.(type) {
		case UserMessage:
			points = append(points, i)
		case AssistantMessage:
			points = append(points, i)
		case ToolResultMessage:
		case Notification:
		}
	}
	return points
}

// selectCut chooses the index that splits messages into a compacted prefix and
// a preserved suffix.
func selectCut(messages []Message, keepRecent int) int {
	points := findValidCutPoints(messages)
	if len(points) == 0 {
		return -1
	}

	boundary := len(messages) - keepRecent
	if boundary <= 0 {
		return -1
	}

	chosen := -1
	for _, point := range points {
		if point <= boundary {
			chosen = point
		}
	}
	if chosen <= 0 {
		return -1
	}
	return chosen
}

// fitCut chooses a suffix boundary whose estimated size fits target.
//
// It walks the preserved window down from keepRecent. The cut itself always
// comes from selectCut, so a tool result can never begin the suffix and the
// provider never receives an orphaned result.
func fitCut(messages []Message, keepRecent, target int) int {
	if keepRecent < 1 {
		keepRecent = 1
	}
	for keep := keepRecent; keep >= 1; keep-- {
		cut := selectCut(messages, keep)
		if cut <= 0 {
			continue
		}
		if target <= 0 || EstimateMessages(messages[cut:]) <= target {
			return cut
		}
	}
	// No suffix fits. The caller either reconstructs from durable state alone
	// or summarizes the whole conversation; keeping an oversized tail would
	// only reproduce the overflow.
	return -1
}

// SummarizeFunc replaces a message range with a summary.
type SummarizeFunc func(ctx context.Context, messages []Message) (string, error)

// CompactResult is the outcome of a compaction.
type CompactResult struct {
	// Messages is the rebuilt conversation.
	Messages []Message
	// Summary is the narrative text that replaced the compacted prefix. It is
	// empty for a state-first reconstruction, which is the preferred mode.
	Summary string
	// Compacted is how many messages were replaced or reconstructed.
	Compacted int
	// Retained is how many messages were kept verbatim.
	Retained int
	// BeforeTokens is the estimated size of the input.
	BeforeTokens int
	// AfterTokens is the estimated size of the output.
	AfterTokens int
	// Mode is "reconstructed" for a state-first rebuild or "summarized" when
	// the semantic fallback ran.
	Mode string
	// Trimmed counts tool results replaced by deterministic trimming.
	Trimmed int
	// Methods records the compaction levels that ran, in order.
	Methods []string
	// Metrics is the structured diagnostic record for this compaction.
	Metrics CompactionMetrics
}

// Compaction modes.
const (
	// ModeReconstructed means the context was rebuilt from authoritative
	// durable state. No model call was needed.
	ModeReconstructed = "reconstructed"
	// ModeSummarized means the semantic fallback replaced a prefix.
	ModeSummarized = "summarized"
	// ModeUnchanged means the requested compaction was already as small as the
	// authoritative projection, so the context was returned unchanged.
	ModeUnchanged = "unchanged"
)

// PreservationCounts is what a reconstruction preserved by category. It is
// computed from runtime state, never from model output.
type PreservationCounts struct {
	Constraints   int `json:"constraints,omitempty"`
	Evidence      int `json:"evidence,omitempty"`
	Changes       int `json:"changes,omitempty"`
	Verifications int `json:"verifications,omitempty"`
	Completion    int `json:"completion,omitempty"`
	OpenQuestions int `json:"open_questions,omitempty"`
}

// Total reports the number of preserved state items.
func (c PreservationCounts) Total() int {
	return c.Constraints + c.Evidence + c.Changes + c.Verifications + c.Completion + c.OpenQuestions
}

// ContextProjection is the authoritative Tier-0 and Tier-1 context a state-first
// compaction reconstructs from.
//
// It is produced by the control/continuity plane, never by a model. Blocks are
// deterministic text drawn from durable runtime state. The loop validates the
// projection before using it, so a malformed or fabricated projection is
// rejected, leaving the previous valid context in place.
type ContextProjection struct {
	// Authority is the live execution role and its permissions, rendered from
	// authoritative policy.
	Authority string
	// Objective is the user's request, preserved exactly.
	Objective string
	// Completion is the current completion state (for example PROVEN,
	// NEEDS_REVIEW, IN_PROGRESS) from the objective/proof system.
	Completion string
	// Blocks are deterministic orientation blocks: intent contract,
	// constraints, facts, evidence, decisions, changes, verification, open
	// questions, blockers, and next action.
	Blocks []string
	// Counts describes what the projection preserved.
	Counts PreservationCounts
	// Digest is the deterministic identity of the projection. An empty value
	// is filled by Render; a non-empty value that does not match is rejected.
	Digest string
}

// Validate reports whether the projection is usable.
func (p ContextProjection) Validate() error {
	if strings.TrimSpace(p.Authority) == "" {
		return fmt.Errorf("%w: authority is empty", ErrInvalidProjection)
	}
	if strings.TrimSpace(p.Objective) == "" {
		return fmt.Errorf("%w: objective is empty", ErrInvalidProjection)
	}
	if len(p.Blocks) == 0 {
		return fmt.Errorf("%w: no durable state blocks", ErrInvalidProjection)
	}
	digest := ProjectionDigest(p)
	if p.Digest != "" && p.Digest != digest {
		return fmt.Errorf("%w: digest mismatch", ErrInvalidProjection)
	}
	return nil
}

// ProjectionDigest returns the deterministic identity of a projection.
func ProjectionDigest(p ContextProjection) string {
	var sb strings.Builder
	sb.WriteString(p.Authority)
	sb.WriteByte(0)
	sb.WriteString(p.Objective)
	sb.WriteByte(0)
	sb.WriteString(p.Completion)
	for _, block := range p.Blocks {
		sb.WriteByte(0)
		sb.WriteString(block)
	}
	sum := sha256.Sum256([]byte(sb.String()))
	return hex.EncodeToString(sum[:12])
}

// Render returns the deterministic model-facing text for a projection.
func (p ContextProjection) Render() string {
	var sb strings.Builder
	sb.WriteString("# Authoritative task state (reconstructed by the runtime, not a model summary)\n\n")
	if p.Authority != "" {
		sb.WriteString("Authority: ")
		sb.WriteString(p.Authority)
		sb.WriteByte('\n')
	}
	if p.Completion != "" {
		sb.WriteString("Completion state: ")
		sb.WriteString(p.Completion)
		sb.WriteByte('\n')
	}
	if p.Objective != "" {
		sb.WriteString("Objective: ")
		sb.WriteString(p.Objective)
		sb.WriteByte('\n')
	}
	for _, block := range p.Blocks {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		if duplicateProjectionLine(block, p) {
			continue
		}
		sb.WriteByte('\n')
		sb.WriteString(block)
		sb.WriteByte('\n')
	}
	return strings.TrimRight(sb.String(), "\n")
}

// duplicateProjectionLine reports a block the explicit header already carries.
func duplicateProjectionLine(block string, p ContextProjection) bool {
	lower := strings.ToLower(block)
	switch {
	case strings.HasPrefix(lower, "authority:") && p.Authority != "":
		return true
	case strings.HasPrefix(lower, "objective:") && p.Objective != "":
		return true
	}
	return false
}

// RecallItem is one archived historical observation.
type RecallItem struct {
	ID    string `json:"id"`
	Tool  string `json:"tool"`
	Text  string `json:"text"`
	Bytes int    `json:"bytes"`
}

// Bounds keep the in-memory recall archive from becoming a second transcript.
// The durable session store remains the complete record; this archive exists
// only so the active run can retrieve a bounded observation by id.
const (
	// MaxRecallItems bounds archived observations held by one run.
	MaxRecallItems = 512
	// MaxRecallBytes bounds the total archived text.
	MaxRecallBytes = 8 << 20
	// MaxRecallItemBytes bounds one archived observation.
	MaxRecallItemBytes = 64 << 10
)

// RecallStore archives observations so a compacted context can still retrieve
// them by stable id. It is safe for concurrent use.
type RecallStore struct {
	mu     sync.RWMutex
	next   int
	bytes  int
	byCall map[string]string
	items  map[string]RecallItem
}

// NewRecallStore returns an empty archive.
func NewRecallStore() *RecallStore {
	return &RecallStore{
		byCall: map[string]string{},
		items:  map[string]RecallItem{},
	}
}

// Archive stores text and returns its stable id, or "" when the archive
// refuses it because a bound is reached. Archiving the same call twice returns
// the existing id.
func (r *RecallStore) Archive(tool, callID, text string) string {
	if r == nil || strings.TrimSpace(text) == "" {
		return ""
	}
	if len(text) > MaxRecallItemBytes {
		text = text[:MaxRecallItemBytes] + "\n[archived evidence bounded]"
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if callID != "" {
		if id, ok := r.byCall[callID]; ok {
			return id
		}
	}
	if len(r.items) >= MaxRecallItems || r.bytes+len(text) > MaxRecallBytes {
		return ""
	}
	r.next++
	id := fmt.Sprintf("E%d", r.next)
	item := RecallItem{ID: id, Tool: tool, Text: text, Bytes: len(text)}
	r.items[id] = item
	r.bytes += len(text)
	if callID != "" {
		r.byCall[callID] = id
	}
	return id
}

// Get returns one archived observation.
func (r *RecallStore) Get(id string) (RecallItem, bool) {
	if r == nil {
		return RecallItem{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	item, ok := r.items[strings.TrimSpace(id)]
	return item, ok
}

// Len reports how many observations are archived.
func (r *RecallStore) Len() int {
	if r == nil {
		return 0
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.items)
}

// BoundToolResults replaces an oversized tool payload with a bounded head and
// an archive reference. It is the size-based counterpart of deduplication: the
// model keeps enough of the observation to decide whether to recall the rest.
func BoundToolResults(messages []Message, recall *RecallStore, maxBytes int) ([]Message, TrimReport) {
	report := TrimReport{}
	if maxBytes <= 0 {
		maxBytes = defaultBoundToolBytes
	}
	out := make([]Message, len(messages))
	copy(out, messages)
	for i, message := range messages {
		result, ok := message.(ToolResultMessage)
		if !ok {
			continue
		}
		text := result.Text()
		if len(text) <= maxBytes {
			continue
		}
		limit := maxBytes
		for limit > 0 && !utf8RuneStart(text[limit]) {
			limit--
		}
		head := text[:limit]
		ref := ""
		if recall != nil {
			if id := recall.Archive(result.ToolName, result.ToolCallID, text); id != "" {
				ref = " Archived as " + id + "; request it with the recall tool."
			}
		}
		replacement := fmt.Sprintf(
			"[tool output bounded to fit the model window; %d bytes omitted.%s]\n%s",
			len(text)-limit, ref, head)
		report.ToolResultsTrimmed++
		report.BytesRemoved += len(text) - len(replacement)
		out[i] = ToolResultMessage{
			ToolCallID: result.ToolCallID,
			ToolName:   result.ToolName,
			Content:    ai.TextBlocks(replacement),
			IsError:    result.IsError,
		}
	}
	return out, report
}

// utf8RuneStart reports whether b begins a UTF-8 rune.
func utf8RuneStart(b byte) bool { return b&0xC0 != 0x80 }

// TrimReport records deterministic trimming.
type TrimReport struct {
	ToolResultsTrimmed int
	Duplicates         int
	Superseded         int
	BytesRemoved       int
}

// TrimMessages removes disposable active-context content without deleting the
// historical record.
//
// It replaces an older tool result whose identical invocation was observed
// again with a short reference, and archives the original text when a recall
// store is supplied. It never trims an error result or the newest results, and
// it never changes a user message, assistant prose, or a tool call.
func TrimMessages(messages []Message, c Compaction, recall *RecallStore) ([]Message, TrimReport) {
	report := TrimReport{}
	if len(messages) == 0 {
		return messages, report
	}
	keepRecent := c.keepRecentToolResults()
	calls := toolCallsByID(messages)

	resultIndexes := make([]int, 0, len(messages))
	for i, message := range messages {
		if _, ok := message.(ToolResultMessage); ok {
			resultIndexes = append(resultIndexes, i)
		}
	}
	if len(resultIndexes) <= keepRecent {
		return append([]Message(nil), messages...), report
	}
	keepFrom := len(resultIndexes) - keepRecent

	// The newest observation of each canonical invocation wins. Older results
	// with the same invocation are superseded and may be replaced by a
	// reference; the raw text stays addressable.
	newest := make(map[string]int, len(resultIndexes))
	for position, index := range resultIndexes {
		result := messages[index].(ToolResultMessage)
		key := toolInvocationKey(result, calls)
		if key == "" {
			continue
		}
		if position >= keepFrom {
			newest[key] = index
		}
	}

	out := make([]Message, len(messages))
	copy(out, messages)
	for position, index := range resultIndexes {
		if position >= keepFrom {
			continue
		}
		result := out[index].(ToolResultMessage)
		if result.IsError {
			continue
		}
		key := toolInvocationKey(result, calls)
		if key == "" {
			continue
		}
		newer, ok := newest[key]
		if !ok {
			continue
		}
		original := result.Text()
		replacement := fmt.Sprintf(
			"[%s result trimmed: the same invocation was observed again later; the newer result is authoritative. %s]",
			result.ToolName, recallReference(recall, result.ToolName, result.ToolCallID, original))
		if len(replacement) >= len(original) {
			continue
		}
		report.ToolResultsTrimmed++
		if newer > index {
			report.Superseded++
		}
		report.BytesRemoved += len(original) - len(replacement)
		out[index] = ToolResultMessage{
			ToolCallID: result.ToolCallID,
			ToolName:   result.ToolName,
			Content:    ai.TextBlocks(replacement),
		}
	}
	return out, report
}

// recallReference archives an observation and renders the pointer the model
// can use to request it. It is empty when no archive is installed.
func recallReference(recall *RecallStore, tool, callID, text string) string {
	if recall == nil {
		return ""
	}
	id := recall.Archive(tool, callID, text)
	if id == "" {
		return ""
	}
	return "Archived as " + id + "; request it with the recall tool."
}

// toolCallsByID indexes the canonical calls in a conversation so a result can
// be matched to the invocation that produced it.
func toolCallsByID(messages []Message) map[string]ai.ToolCall {
	calls := map[string]ai.ToolCall{}
	for _, message := range messages {
		assistant, ok := message.(AssistantMessage)
		if !ok {
			continue
		}
		for _, block := range assistant.Message.Content {
			call, ok := block.(ai.ToolCall)
			if ok && call.ID != "" {
				calls[call.ID] = call
			}
		}
	}
	return calls
}

// toolInvocationKey is the canonical identity of an observation.
//
// Only read-only observation tools are deduplicated. A mutating or verifying
// result stays exact even when the invocation repeats: the newest run is the
// current state, but the older one is evidence of what changed, and collapsing
// it would erase history.
func toolInvocationKey(result ToolResultMessage, calls map[string]ai.ToolCall) string {
	if !observationTool(result.ToolName) {
		return ""
	}
	call, ok := calls[result.ToolCallID]
	if !ok {
		return ""
	}
	data, err := json.Marshal(call.Arguments)
	if err != nil {
		return ""
	}
	return result.ToolName + "|" + string(data)
}

// Compact reduces messages so the conversation fits the window.
//
// It is the semantic-only primitive retained for callers that have no durable
// state projection. New callers should use CompactWith, which tries
// deterministic trimming and state-first reconstruction first.
func Compact(ctx context.Context, messages []Message, c Compaction, window int, summarize SummarizeFunc) (CompactResult, error) {
	return compactSummary(ctx, messages, c, window, summarize, nil, 0)
}

// CompactOptions configures one intelligent compaction.
type CompactOptions struct {
	// Compaction is the policy.
	Compaction Compaction
	// Window is the model context limit.
	Window int
	// Summarize is the semantic fallback. It is required only when no
	// projection is available.
	Summarize SummarizeFunc
	// Projection returns the authoritative durable state for a state-first
	// rebuild. It must never return model-generated text.
	Projection func() (ContextProjection, bool)
	// Recall archives trimmed or reconstructed history. Nil disables id
	// references.
	Recall *RecallStore
	// ExpectedOutputTokens is the completion budget included in the
	// post-compaction re-evaluation. Zero uses the policy default.
	ExpectedOutputTokens int
	// ContextBudgetTokens is the window left after system, tool, output, and
	// safety overhead. It caps the rebuilt conversation more tightly than the
	// trigger when the overhead is significant. Zero uses the trigger alone.
	ContextBudgetTokens int
	// Trigger records which pressure band caused this compaction.
	Trigger Pressure
}

// CompactionMetrics is the structured diagnostic record of one compaction.
type CompactionMetrics struct {
	ContextBefore       int      `json:"context_before"`
	ContextAfter        int      `json:"context_after"`
	ContextLimit        int      `json:"context_limit"`
	ProjectedBefore     int      `json:"projected_request"`
	Trigger             Pressure `json:"compaction_trigger"`
	DurationMS          int64    `json:"compaction_duration_ms"`
	Mode                string   `json:"mode"`
	CompactionModel     string   `json:"compaction_model,omitempty"`
	InputTokens         int      `json:"compaction_input_tokens,omitempty"`
	OutputTokens        int      `json:"compaction_output_tokens,omitempty"`
	TokensSaved         int      `json:"tokens_saved"`
	ToolOutputRemoved   int      `json:"tool_output_removed"`
	EvidencePreserved   int      `json:"evidence_preserved"`
	StateItemsPreserved int      `json:"state_items_preserved"`
	StateItemsRejected  int      `json:"state_items_rejected,omitempty"`
	ValidationFailures  int      `json:"validation_failures,omitempty"`
	CompactionFailures  int      `json:"compaction_failures,omitempty"`
}

// CompactWith performs deterministic trimming, then prefers a state-first
// reconstruction over semantic summarization.
//
// The order is deliberate: trimming and reconstruction are deterministic and
// cost no model call, and reconstruction is a projection of authoritative
// state rather than a summary of a summary. The semantic fallback runs only
// when no valid projection exists.
//
// A failed projection validation or summarizer call returns an error and
// leaves the caller's messages untouched; the caller keeps its previous valid
// context.
func CompactWith(ctx context.Context, messages []Message, opts CompactOptions) (CompactResult, error) {
	started := time.Now()
	c := opts.Compaction
	window := opts.Window
	if window <= 0 {
		return CompactResult{}, ErrNoContextWindow
	}
	before := EstimateMessages(messages)
	target := resolveThresholdTokens(window, c)
	if opts.ContextBudgetTokens > 0 && opts.ContextBudgetTokens < target {
		target = opts.ContextBudgetTokens
	}

	trimmed, trimReport := TrimMessages(messages, c, opts.Recall)
	if EstimateMessages(trimmed) > target {
		// Deduplication was not enough. Bound oversized tool payloads so the
		// durable state and the recent decision survive inside the window; the
		// full text stays addressable through the recall archive.
		bounded, boundReport := BoundToolResults(trimmed, opts.Recall, defaultBoundToolBytes)
		if boundReport.ToolResultsTrimmed > 0 {
			trimmed = bounded
			trimReport.ToolResultsTrimmed += boundReport.ToolResultsTrimmed
			trimReport.BytesRemoved += boundReport.BytesRemoved
		}
	}

	result := CompactResult{
		BeforeTokens: before,
		Trimmed:      trimReport.ToolResultsTrimmed,
		Methods:      nil,
		Metrics: CompactionMetrics{
			ContextBefore:     before,
			ContextLimit:      window,
			Trigger:           opts.Trigger,
			ToolOutputRemoved: trimReport.BytesRemoved,
		},
	}
	if trimReport.ToolResultsTrimmed > 0 {
		result.Methods = append(result.Methods, "trim")
	}

	if opts.Projection != nil {
		projection, ok := opts.Projection()
		if ok {
			if err := projection.Validate(); err != nil {
				result.Metrics.ValidationFailures++
				result.Metrics.CompactionFailures++
				return result, err
			}
			projection.Digest = ProjectionDigest(projection)
			out, retained, err := reconstruct(trimmed, projection, c, target, opts.Recall)
			if err != nil {
				result.Metrics.ValidationFailures++
				result.Metrics.CompactionFailures++
				return result, err
			}
			after := EstimateMessages(out)
			if after >= before {
				if NeedsCompaction(RequestMeasurement{Pressure: opts.Trigger}) {
					// A triggered compaction that does not reduce would loop or
					// leave an unsafe request. Report it; the loop's emergency
					// trim / overflow policy owns the next step.
					result.Metrics.CompactionFailures++
					return result, ErrCompactionIneffective
				}
				// A manual request on an already-compact context is a safe
				// no-op. It must not grow the working history.
				result.Messages = append([]Message(nil), messages...)
				result.Mode = ModeUnchanged
				result.Retained = len(messages)
				result.AfterTokens = before
				result.Methods = append(result.Methods, "unchanged")
				result.Metrics.Mode = ModeUnchanged
				result.Metrics.ContextAfter = before
				result.Metrics.TokensSaved = 0
				result.Metrics.EvidencePreserved = projection.Counts.Evidence
				result.Metrics.StateItemsPreserved = projection.Counts.Total()
				result.Metrics.DurationMS = time.Since(started).Milliseconds()
				return result, nil
			}
			result.Messages = out
			result.Mode = ModeReconstructed
			result.Compacted = len(trimmed) - retained
			result.Retained = retained
			result.AfterTokens = after
			result.Methods = append(result.Methods, "reconstruct")
			result.Metrics.Mode = ModeReconstructed
			result.Metrics.ContextAfter = result.AfterTokens
			result.Metrics.TokensSaved = before - result.AfterTokens
			result.Metrics.EvidencePreserved = projection.Counts.Evidence
			result.Metrics.StateItemsPreserved = projection.Counts.Total()
			result.Metrics.DurationMS = time.Since(started).Milliseconds()
			return result, nil
		}
	}

	if opts.Summarize == nil {
		return CompactResult{}, ErrNoSummarizer
	}
	compact, err := compactSummary(ctx, trimmed, c, window, opts.Summarize, opts.Recall, target)
	if err != nil {
		result.Metrics.CompactionFailures++
		return result, err
	}
	compact.BeforeTokens = before
	compact.Trimmed = trimReport.ToolResultsTrimmed
	compact.Methods = append(result.Methods, "summarize")
	compact.Metrics.ContextBefore = before
	compact.Metrics.ContextLimit = window
	compact.Metrics.Trigger = opts.Trigger
	compact.Metrics.ToolOutputRemoved = trimReport.BytesRemoved
	compact.Metrics.TokensSaved = before - compact.AfterTokens
	compact.Metrics.DurationMS = time.Since(started).Milliseconds()
	if compact.AfterTokens >= target && compact.AfterTokens >= before {
		compact.Metrics.CompactionFailures++
		return compact, ErrCompactionIneffective
	}
	return compact, nil
}

// reconstruct builds a context from durable state plus a bounded recent
// window. It never uses model prose as a state source.
func reconstruct(messages []Message, projection ContextProjection, c Compaction, target int, recall *RecallStore) ([]Message, int, error) {
	header := projection.Render()
	// The durable projection consumes budget before any recent message does.
	// Reserve it explicitly instead of guessing a split, so the recent window
	// is as large as the state leaves room for.
	stateTokens := EstimateMessages([]Message{UserMessage{Text: header}})
	suffixBudget := target - stateTokens
	cut := -1
	if suffixBudget > 0 {
		cut = fitCut(messages, c.KeepRecent, suffixBudget)
	}
	if cut <= 0 {
		// Nothing fits beside the durable projection; it is the whole context.
		out := []Message{UserMessage{Text: header}}
		if err := ValidateCompactedMessages(out); err != nil {
			return nil, 0, err
		}
		return out, 0, nil
	}
	if ids := archiveHistory(messages[:cut], recall); ids != "" {
		header += "\n\nHistorical evidence retained: " + ids +
			". Request an id with the recall tool when the brief is not enough."
	}
	suffix := messages[cut:]
	out := make([]Message, 0, len(suffix)+1)
	out = append(out, UserMessage{Text: header})
	out = append(out, suffix...)
	if err := ValidateCompactedMessages(out); err != nil {
		return nil, 0, err
	}
	return out, len(suffix), nil
}

// archiveHistory archives the tool observations a compaction replaces and
// returns a bounded pointer list. It never returns evidence text: the model
// sees ids and must request the record it needs.
func archiveHistory(messages []Message, recall *RecallStore) string {
	if recall == nil {
		return ""
	}
	var ids []string
	for _, message := range messages {
		result, ok := message.(ToolResultMessage)
		if !ok || result.ToolCallID == "" {
			continue
		}
		if id := recall.Archive(result.ToolName, result.ToolCallID, result.Text()); id != "" {
			ids = append(ids, id+" ("+result.ToolName+")")
		}
		if len(ids) >= 8 {
			break
		}
	}
	if len(ids) == 0 {
		return ""
	}
	return strings.Join(ids, ", ")
}

// compactSummary is the original semantic compaction path.
func compactSummary(ctx context.Context, messages []Message, c Compaction, window int, summarize SummarizeFunc, recall *RecallStore, fitTarget int) (CompactResult, error) {
	if window <= 0 {
		return CompactResult{}, ErrNoContextWindow
	}
	if summarize == nil {
		return CompactResult{}, ErrNoSummarizer
	}

	cut := selectCut(messages, c.KeepRecent)
	if cut <= 0 {
		return CompactResult{}, ErrNothingToCompact
	}
	if fitTarget > 0 && EstimateMessages(messages[cut:]) > fitTarget-minSummaryTokens {
		cut = fitCut(messages, c.KeepRecent, fitTarget-minSummaryTokens)
	}
	if cut <= 0 && fitTarget > 0 {
		// No recent suffix fits beside the summary. Preserve the whole
		// conversation in the handover rather than sending an oversized tail.
		cut = len(messages)
	}
	if cut <= 0 {
		return CompactResult{}, ErrNothingToCompact
	}
	if fitTarget > 0 && EstimateMessages(messages[cut:]) > fitTarget-minSummaryTokens {
		// No suffix fits beside the summary. Preserve the whole conversation
		// in the handover instead of sending a request that cannot fit or
		// truncating the summary into uselessness. The durable state and the
		// recall archive remain the authoritative record.
		cut = len(messages)
	}

	prefix := messages[:cut]
	suffix := messages[cut:]

	summary, err := summarize(ctx, prefix)
	if err != nil {
		return CompactResult{}, fmt.Errorf("agent: summarise context: %w", err)
	}
	if strings.TrimSpace(summary) == "" {
		return CompactResult{}, ErrEmptySummary
	}

	remaining := resolveThresholdTokens(window, c) - EstimateMessages(suffix)
	if remaining < minSummaryTokens {
		// A preserved suffix larger than the trigger leaves no nominal room
		// for the summary. A one-token summary would erase the handover and
		// make the next turn repeat the same work, so keep a bounded minimum
		// and let the loop's post-compaction check report the real overflow.
		remaining = minSummaryTokens
	}
	summary = tokens.TruncateToBudget(summary, remaining)

	header := "Summary of earlier conversation:\n\n" + summary
	if ids := archiveHistory(prefix, recall); ids != "" {
		header += "\n\nHistorical evidence retained: " + ids +
			". Request an id with the recall tool when the summary is not enough."
	}
	out := make([]Message, 0, len(suffix)+1)
	out = append(out, UserMessage{Text: header})
	out = append(out, suffix...)
	if err := ValidateCompactedMessages(out); err != nil {
		return CompactResult{}, err
	}

	result := CompactResult{
		Messages:     out,
		Summary:      summary,
		Compacted:    len(prefix),
		Retained:     len(suffix),
		BeforeTokens: EstimateMessages(messages),
		AfterTokens:  EstimateMessages(out),
		Mode:         ModeSummarized,
	}
	return result, nil
}

// ValidateCompactedMessages checks the structural invariants every provider
// requires: the rebuilt conversation starts with a user turn and no tool
// result is orphaned from the call that produced it.
func ValidateCompactedMessages(messages []Message) error {
	if len(messages) == 0 {
		return fmt.Errorf("%w: rebuilt context is empty", ErrInvalidProjection)
	}
	if _, ok := messages[0].(UserMessage); !ok {
		return fmt.Errorf("%w: rebuilt context must begin with a user message", ErrInvalidProjection)
	}
	known := map[string]bool{}
	for i, message := range messages {
		switch m := message.(type) {
		case AssistantMessage:
			for _, block := range m.Message.Content {
				if call, ok := block.(ai.ToolCall); ok {
					known[call.ID] = true
				}
			}
		case ToolResultMessage:
			if !known[m.ToolCallID] {
				return fmt.Errorf("%w: tool result %q at %d has no preceding call", ErrInvalidProjection, m.ToolCallID, i)
			}
		}
	}
	return nil
}

// Compaction failure modes, matched with errors.Is.
var (
	// ErrNoContextWindow means compaction was asked to run without a window.
	ErrNoContextWindow = errors.New("agent: compaction needs a positive context window")
	// ErrNoSummarizer means no summary function was supplied and no durable
	// projection was available.
	ErrNoSummarizer = errors.New("agent: compaction needs a summarize function")
	// ErrNothingToCompact means the conversation has no valid cut point.
	ErrNothingToCompact = errors.New("agent: conversation has nothing to compact")
	// ErrEmptySummary means the summarizer returned nothing.
	ErrEmptySummary = errors.New("agent: summarizer returned an empty summary")
	// ErrInvalidProjection means a durable reconstruction was malformed or
	// failed validation. The previous valid context is retained.
	ErrInvalidProjection = errors.New("agent: invalid context projection")
	// ErrCompactionIneffective means a compaction ran but did not reduce the
	// context, so repeating it would only loop.
	ErrCompactionIneffective = errors.New("agent: compaction did not reduce the context")
	// ErrContextOverflow means the runtime cannot construct a request that
	// fits the model window. It is a truthful, actionable failure: the task
	// state is intact and the caller must report review rather than success.
	ErrContextOverflow = errors.New("agent: context cannot be reduced to fit the model window")
)
