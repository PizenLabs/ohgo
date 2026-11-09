package experience

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/cognition"
)

// Extraction bounds. They keep one observation from producing an unbounded
// lesson set; a runaway run is summarized by its dominant pattern, not by one
// event per call.
const (
	maxEventsPerObservation = 24
	maxEventTools           = 16
	maxEventRefs            = 8
	// contextGrowthBytes is the projected-request size at which growth is worth
	// recording. It is a diagnostic threshold, never a completion or authority
	// signal.
	contextGrowthBytes = 48 * 1024
	// cheapToolCalls is the ceiling for a successful minimal path candidate.
	cheapToolCalls = 8
	// expensiveToolCalls is the floor for a successful path that spent enough
	// calls that a shorter path is worth learning.
	expensiveToolCalls = 12
)

// Extract derives deterministic incidents from one observation.
//
// It never interprets model prose and never re-reads the transcript as a source
// of truth. The signals come from completed turns, the projection report, the
// evidence state, the contract kind, and the runtime's outcome label.
func Extract(obs Observation) []Event {
	ctx := ContextFor(obs)
	now := obs.FinishedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	workspace := ctx.Workspace
	taskKind := ctx.TaskKind

	events := make([]Event, 0, 8)
	seen := make(map[string]bool, 8)
	add := func(event Event) {
		if len(events) >= maxEventsPerObservation {
			return
		}
		event.Workspace = workspace
		event.TaskKind = taskKind
		event.Outcome = obs.Outcome
		event.At = now
		if event.Key == "" {
			event.Key = strings.Join([]string{event.Kind, ctx.Key(), event.Tool, event.Target}, "|")
		}
		if seen[event.Key] {
			return
		}
		seen[event.Key] = true
		event.ID = stableID("event", event.Key)
		events = append(events, event)
	}

	type failureAt struct {
		position int
		outcome  agent.ToolOutcome
		target   string
		path     string
	}
	type callSeen struct {
		position int
		ok       bool
	}

	firstSeen := make(map[string]callSeen)
	successByTarget := make(map[string]int)
	listingSuccess := make(map[string]int)
	failures := make([]failureAt, 0, 4)
	priorPaths := evidencePaths(obs.PriorEvidence)

	var (
		position            int
		lastMutation        int
		toolCalls           int
		repeatedCalls       int
		evidenceGained      int
		verifications       int
		recoveryAttempts    int
		handoffRedundancies int
		usedTools           []string
	)
	lastMutation = -1

	for _, turn := range obs.Turns {
		for _, outcome := range turn.Results {
			position++
			if ignoredTool(outcome.ToolName) {
				continue
			}
			toolCalls++
			tool := outcome.ToolName
			target := agent.CallTarget(tool, outcome.Args)
			path := callPath(outcome.Args)

			mutated := outcome.Result.Mutates ||
				(outcome.Authorization != nil && outcome.Authorization.Mutates)
			if mutated {
				lastMutation = position
			}

			if outcome.Refused() {
				recoveryAttempts++
				add(Event{
					Kind:    EventAuthorityRefusal,
					Tool:    tool,
					Target:  target,
					Summary: "a call was refused by the runtime's authority boundary: " + tool,
					Refs:    []string{stableID("call", target)},
				})
				continue
			}

			if outcome.Result.IsError {
				if !outcome.Result.ObservedAbsence && outcome.Result.Exhausted == nil {
					failures = append(failures, failureAt{
						position: position,
						outcome:  outcome,
						target:   target,
						path:     path,
					})
				}
				continue
			}

			// A successful call is an observation. Only read-like capabilities
			// count as evidence gain; a todo or artifact update is not a
			// repository observation.
			if !containsString(usedTools, tool) {
				usedTools = append(usedTools, tool)
			}
			if observationTool(tool) {
				evidenceGained++
			}
			if outcome.Result.Verifies {
				verifications++
			}
			successByTarget[target] = position
			if tool == "ls" {
				listingSuccess[normalizePath(path)] = position
			}
			if len(priorPaths) > 0 && path != "" && priorPaths[normalizePath(path)] {
				handoffRedundancies++
			}

			signature := agent.CallSignature(tool, outcome.Args)
			if seenCall, ok := firstSeen[signature]; ok {
				// A repeat is redundant only when it observes the same
				// coordinate without an intervening mutation. Re-running a
				// check after a fix is a new observation, not a duplicate.
				if seenCall.ok && lastMutation < seenCall.position {
					repeatedCalls++
					add(Event{
						Kind:    EventRepeatedProbe,
						Tool:    tool,
						Target:  target,
						Summary: "an identical successful probe was repeated without new evidence: " + tool,
						Tools:   []string{tool},
						Refs:    []string{stableID("call", signature)},
					})
				}
			} else {
				firstSeen[signature] = callSeen{position: position, ok: true}
			}
		}
	}

	for _, failure := range failures {
		recoveryAttempts++
		nonMaterial := failure.outcome.ToolName == "read" &&
			directoryReadFailure(failure.outcome.Result.Text) &&
			equivalentListing(failure.path, listingSuccess)
		recovered := successByTarget[failure.target] > failure.position || nonMaterial
		if !recovered {
			add(Event{
				Kind:         EventMaterialFailure,
				Tool:         failure.outcome.ToolName,
				Target:       failure.target,
				FailureClass: failure.outcome.Result.FailureClass,
				Summary:      "a required capability failed and no later observation covered it: " + failure.outcome.ToolName,
				Refs:         []string{stableID("call", failure.target)},
			})
			continue
		}
		summary := "a failed probe was recovered by a later observation of the same coordinate: " + failure.outcome.ToolName
		if nonMaterial {
			summary = "a failed directory read was non-material because a listing already established the structure"
		}
		add(Event{
			Kind:         EventRecoverableFailure,
			Tool:         failure.outcome.ToolName,
			Target:       failure.target,
			FailureClass: failure.outcome.Result.FailureClass,
			Summary:      summary,
			NonMaterial:  nonMaterial,
			Tools:        []string{failure.outcome.ToolName},
			Refs:         []string{stableID("call", failure.target)},
		})
	}

	// A completed path is the positive signal. Prefer the shortcut lesson when
	// the run was cheap and duplicate-free; a completed but expensive path gets
	// the efficiency lesson instead. Both are learned from success, not only
	// failure.
	if obs.Outcome == OutcomeCompleted && toolCalls >= 2 {
		switch {
		case repeatedCalls == 0 && toolCalls <= cheapToolCalls:
			add(Event{
				Kind:    EventSuccessfulShortcut,
				Summary: fmt.Sprintf("a low-cost path completed the objective in %d tool call(s) with no repeats", toolCalls),
				Tools:   append([]string(nil), usedTools...),
			})
		case repeatedCalls > 0 || toolCalls >= expensiveToolCalls:
			add(Event{
				Kind:    EventExpensivePath,
				Summary: fmt.Sprintf("a successful path used %d tool call(s), including %d repeated call(s)", toolCalls, repeatedCalls),
				Tools:   append([]string(nil), usedTools...),
			})
		}
	}

	if obs.Outcome == OutcomeNeedsReview {
		if review := strings.TrimSpace(obs.Projection.EvidenceReview()); review != "" {
			add(Event{
				Kind:    EventEvidenceGap,
				Summary: "the run stopped without the evidence its objective required: " + review,
				Refs:    []string{"projection"},
			})
		} else if obs.Projection.Sufficiency == "CONFLICTING" || obs.Projection.Sufficiency == "BLOCKED" {
			add(Event{
				Kind:    EventEvidenceGap,
				Summary: "the run stopped on a conflicting or blocked evidence state",
				Refs:    []string{"projection"},
			})
		} else if obs.Projection.Provenance.RequireAttribution &&
			obs.Projection.Provenance.HasDocumentation &&
			!obs.Projection.Provenance.HasImplementation {
			add(Event{
				Kind:    EventEvidenceGap,
				Summary: "the answer relied on documentation without an implementation observation",
				Refs:    []string{"provenance"},
			})
		}
	}

	if strings.EqualFold(strings.TrimSpace(obs.PriorStatus), "NEEDS_REVIEW") &&
		strings.TrimSpace(obs.PriorReviewReason) != "" &&
		obs.Outcome != OutcomeCompleted {
		add(Event{
			Kind:    EventStaleReview,
			Summary: "the same review boundary recurred without acquiring the missing evidence",
			Refs:    []string{stableID("review", obs.PriorReviewReason)},
		})
	}

	if obs.PriorWorkspace != "" && !strings.EqualFold(obs.PriorWorkspace, obs.Workspace) &&
		handoffRedundancies > 0 {
		add(Event{
			Kind:    EventHandoffRedundancy,
			Summary: fmt.Sprintf("a role handoff was followed by %d probe(s) of evidence the task already carried", handoffRedundancies),
			Refs:    []string{strings.ToLower(obs.PriorWorkspace), strings.ToLower(obs.Workspace)},
		})
	}

	if obs.Projection.BytesBefore >= contextGrowthBytes {
		add(Event{
			Kind:    EventContextGrowth,
			Summary: fmt.Sprintf("the projected request reached %d bytes before the turn ended", obs.Projection.BytesBefore),
			Refs:    []string{"projection"},
		})
	}

	cost := Cost{
		ToolCalls:         toolCalls,
		RepeatedToolCalls: repeatedCalls,
		ModelTurns:        len(obs.Turns),
		InputTokens:       observationInputTokens(obs.Turns),
		OutputTokens:      observationOutputTokens(obs.Turns),
		EvidenceGained:    evidenceGained,
		Verifications:     verifications,
		RecoveryAttempts:  recoveryAttempts,
	}
	if !obs.StartedAt.IsZero() && !obs.FinishedAt.IsZero() && obs.FinishedAt.After(obs.StartedAt) {
		cost.DurationMS = obs.FinishedAt.Sub(obs.StartedAt).Milliseconds()
	}
	for i := range events {
		events[i].Cost = cost
		if len(events[i].Tools) > maxEventTools {
			events[i].Tools = events[i].Tools[:maxEventTools]
		}
		events[i].Tools = uniqueStrings(events[i].Tools)
		if len(events[i].Refs) > maxEventRefs {
			events[i].Refs = events[i].Refs[:maxEventRefs]
		}
	}
	return events
}

// observationInputTokens sums measured provider input usage.
func observationInputTokens(turns []agent.Turn) int {
	total := 0
	for _, turn := range turns {
		total += turn.Assistant.Usage.Input
	}
	return total
}

// observationOutputTokens sums measured provider output usage.
func observationOutputTokens(turns []agent.Turn) int {
	total := 0
	for _, turn := range turns {
		total += turn.Assistant.Usage.Output
	}
	return total
}

// ignoredTool reports a capability that is control/plan bookkeeping rather than
// repository observation. Repeating it is not exploration waste.
func ignoredTool(tool string) bool {
	switch tool {
	case "todo", "artifact", "task":
		return true
	default:
		return false
	}
}

// observationTool reports a capability that observes repository state.
func observationTool(tool string) bool {
	switch tool {
	case "read", "ls", "glob", "grep", "symbols", "graph", "refs":
		return true
	default:
		return false
	}
}

// callPath extracts a repository-relative path from call arguments.
func callPath(args map[string]any) string {
	for _, key := range []string{"path", "file", "file_path", "target"} {
		if value, ok := args[key].(string); ok {
			if path := normalizePath(value); path != "" {
				return path
			}
		}
	}
	return ""
}

// normalizePath trims decoration and normalizes a repository-relative path.
func normalizePath(value string) string {
	value = strings.TrimSpace(strings.Trim(value, "`'\""))
	value = strings.TrimPrefix(value, "./")
	value = filepath.ToSlash(filepath.Clean(value))
	if value == "." || value == "" || strings.HasPrefix(value, "../") || strings.HasPrefix(value, "/") {
		return ""
	}
	return value
}

// directoryReadFailure recognizes the common "read a directory" mistake.
func directoryReadFailure(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "is a directory") || strings.Contains(lower, "is dir")
}

// equivalentListing reports whether a listing at the path or its parent exists.
func equivalentListing(path string, listings map[string]int) bool {
	if path == "" {
		return false
	}
	if listings[path] > 0 {
		return true
	}
	parent := filepath.ToSlash(filepath.Dir(path))
	if parent == "." || parent == "" {
		return listings["."] > 0
	}
	return listings[parent] > 0
}

// evidencePaths returns the repository coordinates an evidence state already
// carries. It is used only for handoff redundancy, never as new evidence.
func evidencePaths(state *cognition.EvidenceState) map[string]bool {
	if state == nil {
		return nil
	}
	out := make(map[string]bool, len(state.Items))
	for _, item := range state.Items {
		if path := normalizePath(item.Source.Path); path != "" {
			out[path] = true
		}
		for _, prefix := range []string{"file:", "dir:"} {
			if strings.HasPrefix(item.ID, prefix) {
				if path := normalizePath(strings.TrimPrefix(item.ID, prefix)); path != "" {
					out[path] = true
				}
			}
		}
	}
	return out
}

// containsString reports whether values contains target.
func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
