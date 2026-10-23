package main

import (
	"encoding/json"
	"sort"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/ai"
	"github.com/PizenLabs/ohgo/tokens"
)

// requestMetric is the measured shape of one provider request.
//
// Every byte here was assembled by the runtime and handed to the StreamFn. The
// numbers are not provider billing data; they are the exact request surface the
// runtime controls. Token counts are the runtime's own estimate and are labelled
// as such.
type requestMetric struct {
	Index           int      `json:"index"`
	TotalBytes      int      `json:"total_bytes"`
	SystemBytes     int      `json:"system_bytes"`
	MessageBytes    int      `json:"message_bytes"`
	ToolSchemaBytes int      `json:"tool_schema_bytes"`
	Tokens          int      `json:"estimated_tokens"`
	RepeatedBytes   int      `json:"repeated_bytes"`
	NewBytes        int      `json:"new_bytes"`
	Messages        int      `json:"messages"`
	Tools           int      `json:"tools"`
	ToolNames       []string `json:"tool_names,omitempty"`
}

// runMetrics is one scenario's measured runtime behaviour.
type runMetrics struct {
	Scenario string `json:"scenario"`
	Prompt   string `json:"prompt"`

	Requests            []requestMetric `json:"requests"`
	TurnCount           int             `json:"turn_count"`
	RequestCount        int             `json:"request_count"`
	ToolCallCount       int             `json:"tool_call_count"`
	SuccessfulCalls     int             `json:"successful_tool_calls"`
	FailedCalls         int             `json:"failed_tool_calls"`
	CandidateRejections int             `json:"candidate_rejections"`
	Retries             int             `json:"retries"`

	FilesRead    []string `json:"files_read,omitempty"`
	BytesRead    int      `json:"bytes_read"`
	FilesWritten []string `json:"files_written,omitempty"`
	BytesWritten int      `json:"bytes_written"`

	InputTokens  int `json:"estimated_input_tokens"`
	OutputTokens int `json:"estimated_output_tokens"`

	// Provider usage is only populated by live-provider runs. Zeros mean the
	// provider did not report usage or the run was scripted; they are never
	// presented as measured provider billing.
	ProviderInputTokens  int `json:"provider_input_tokens,omitempty"`
	ProviderOutputTokens int `json:"provider_output_tokens,omitempty"`
	ProviderCacheRead    int `json:"provider_cache_read_tokens,omitempty"`
	ProviderCacheWrite   int `json:"provider_cache_write_tokens,omitempty"`

	RawToolOutputBytes int `json:"raw_tool_output_bytes"`
	ContextBytes       int `json:"context_bytes"`
	ToolSchemaBytes    int `json:"tool_schema_bytes"`
	RepeatedBytes      int `json:"repeated_context_bytes"`
	NewBytes           int `json:"new_context_bytes"`

	// Baseline and final requests make repeated-context growth visible without
	// asking a reader to sum a table.
	FirstRequestBytes int `json:"first_request_bytes"`
	LastRequestBytes  int `json:"last_request_bytes"`

	WallMillis int64 `json:"wall_millis"`

	ObjectiveStatus    string   `json:"objective_status"`
	VerificationStatus string   `json:"verification_status"`
	CompletionReason   string   `json:"completion_reason"`
	Mutated            bool     `json:"mutated"`
	Verified           bool     `json:"verified"`
	MutatedPaths       []string `json:"mutated_paths,omitempty"`
	Unexpected         []string `json:"unexpected_mutations,omitempty"`

	ProjectionReports []agent.ProjectionReport `json:"projection_reports,omitempty"`
	EvidenceBytes     int                      `json:"evidence_state_bytes"`
	EvidenceItems     int                      `json:"evidence_items"`
}

// measureRequests builds per-request metrics and computes how much of each
// request is repeated context rather than content first sent on that request.
//
// The "repeated" definition is deliberately mechanical: a message or tool
// schema whose exact bytes were already present in an earlier request is
// repeated. It is the runtime-side fact the benchmark is allowed to claim,
// separately from anything a provider chooses to bill or cache.
func measureRequests(requests []ai.Request) ([]requestMetric, int, int, int, int, int) {
	seen := map[string]bool{}
	var out []requestMetric
	var totalBytes, totalRepeated, totalNew, totalToolSchema, totalTokens int
	for i, req := range requests {
		metric := requestMetric{Index: i + 1, Messages: len(req.Messages), Tools: len(req.Tools)}
		newBytes, repeatedBytes := 0, 0

		for _, block := range req.System {
			size := len(block.Text)
			metric.SystemBytes += size
			key := "system:" + block.Text
			if seen[key] {
				repeatedBytes += size
			} else {
				newBytes += size
				seen[key] = true
			}
		}

		for _, spec := range req.Tools {
			encoded, _ := json.Marshal(spec.Parameters)
			size := len(spec.Name) + len(spec.Description) + len(encoded)
			metric.ToolSchemaBytes += size
			metric.ToolNames = append(metric.ToolNames, spec.Name)
			key := "tool:" + spec.Name + ":" + spec.Description + ":" + string(encoded)
			if seen[key] {
				repeatedBytes += size
			} else {
				newBytes += size
				seen[key] = true
			}
		}
		sort.Strings(metric.ToolNames)

		for _, msg := range req.Messages {
			text := messageText(msg)
			key := string(msg.Role()) + ":" + text
			size := len(text)
			metric.MessageBytes += size
			if seen[key] {
				repeatedBytes += size
			} else {
				newBytes += size
				seen[key] = true
			}
		}

		metric.TotalBytes = metric.SystemBytes + metric.MessageBytes + metric.ToolSchemaBytes
		metric.Tokens = requestTokens(req)
		metric.NewBytes = newBytes
		metric.RepeatedBytes = repeatedBytes

		totalBytes += metric.TotalBytes
		totalRepeated += repeatedBytes
		totalNew += newBytes
		totalToolSchema += metric.ToolSchemaBytes
		totalTokens += metric.Tokens
		out = append(out, metric)
	}
	return out, totalBytes, totalRepeated, totalNew, totalToolSchema, totalTokens
}

// summarizeTurns folds the loop's turns into the aggregate metrics.
func summarizeTurns(metrics *runMetrics, turns []agent.Turn) {
	metrics.TurnCount = len(turns)
	for _, turn := range turns {
		for _, outcome := range turn.Results {
			metrics.ToolCallCount++
			if outcome.Result.CandidateRejected {
				metrics.CandidateRejections++
			}
			if outcome.Result.IsError {
				metrics.FailedCalls++
			} else {
				metrics.SuccessfulCalls++
			}
			metrics.RawToolOutputBytes += len(outcome.Result.Text)
			metrics.OutputTokens += tokens.Count(outcome.Result.Text, tokens.ModeEstimate)
			recordPath(&metrics.FilesRead, &metrics.BytesRead, outcome, false)
			recordPath(&metrics.FilesWritten, &metrics.BytesWritten, outcome, true)
		}
	}
	metrics.FilesRead = uniqueSorted(metrics.FilesRead)
	metrics.FilesWritten = uniqueSorted(metrics.FilesWritten)
}

// recordPath attributes one tool outcome to the read or write metrics.
func recordPath(files *[]string, bytes *int, outcome agent.ToolOutcome, write bool) {
	details, ok := outcome.Result.Details.(map[string]any)
	if !ok {
		return
	}
	path, _ := details["path"].(string)
	if path == "" {
		return
	}
	if write {
		if !outcome.Result.Mutates {
			return
		}
	} else if outcome.Result.Mutates || outcome.ToolName != "read" {
		return
	}
	*files = append(*files, path)
	if !write {
		*bytes += len(outcome.Result.Text)
	}
}

func uniqueSorted(values []string) []string {
	seen := map[string]bool{}
	var out []string
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

// aggregateRequestMetrics installs the request-level totals on a run.
func aggregateRequestMetrics(metrics *runMetrics, requests []requestMetric, totalBytes, repeated, newBytes, toolSchema, totalTokens int) {
	metrics.Requests = requests
	metrics.RequestCount = len(requests)
	metrics.ContextBytes = totalBytes - toolSchema
	metrics.ToolSchemaBytes = toolSchema
	metrics.RepeatedBytes = repeated
	metrics.NewBytes = newBytes
	metrics.InputTokens = totalTokens
	if len(requests) > 0 {
		metrics.FirstRequestBytes = requests[0].TotalBytes
		metrics.LastRequestBytes = requests[len(requests)-1].TotalBytes
	}
}
