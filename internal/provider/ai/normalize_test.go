package ai

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// pseudoEnvelope renders the XML invoke grammar the dots-style protocol uses.
// It is only a fixture for the protocol-registry tests, never a special case
// in the parser.
func pseudoEnvelope(name, path string) string {
	return fmt.Sprintf("<dotsfunctioncall>\n  <invoke name=%q>\n    <parameter name=\"path\">%s</parameter>\n  </invoke>\n</dotsfunctioncall>", name, path)
}

// rawTextStream builds the wire-shaped stream a text-only provider emits.
func rawTextStream(deltas ...string) *Stream {
	var text strings.Builder
	for _, delta := range deltas {
		text.WriteString(delta)
	}
	msg := AssistantMessage{
		Content:    []ContentBlock{TextContent{Text: text.String()}},
		StopReason: StopEndTurn,
	}
	stream := newStream(len(deltas) + 2)
	ctx := context.Background()
	stream.publish(ctx, EventStart{Message: AssistantMessage{}})
	for _, delta := range deltas {
		if delta != "" {
			stream.publish(ctx, EventTextDelta{Delta: delta})
		}
	}
	stream.publish(ctx, EventDone{Message: msg})
	stream.finish(msg, nil)
	return stream
}

type normalizationRun struct {
	msg    AssistantMessage
	text   string
	calls  []ToolCall
	events []StreamEvent
	diags  []NormalizationDiagnostic
}

// runNormalize drains a normalized stream and records both what a renderer
// would have received live and what the agent loop receives as the final turn.
func runNormalize(t *testing.T, deltas []string, opts NormalizeOptions) normalizationRun {
	t.Helper()
	var (
		text   strings.Builder
		calls  []ToolCall
		events []StreamEvent
		diags  []NormalizationDiagnostic
	)
	previous := opts.OnDiagnostic
	opts.OnDiagnostic = func(d NormalizationDiagnostic) {
		diags = append(diags, d)
		if previous != nil {
			previous(d)
		}
	}

	out := NormalizeStream(context.Background(), rawTextStream(deltas...), opts)
	for event := range out.Events {
		events = append(events, event)
		switch e := event.(type) {
		case EventTextDelta:
			text.WriteString(e.Delta)
		case EventToolCallEnd:
			calls = append(calls, ToolCall{ID: e.ID, Name: e.Name, Arguments: e.Arguments})
		}
	}
	msg, err := out.Result()
	if err != nil {
		t.Fatalf("Result: %v", err)
	}
	return normalizationRun{msg: msg, text: text.String(), calls: calls, events: events, diags: diags}
}

func describeBlocks(blocks []ContentBlock) []string {
	out := make([]string, 0, len(blocks))
	for _, block := range blocks {
		switch b := block.(type) {
		case TextContent:
			out = append(out, "text:"+b.Text)
		case ThinkingContent:
			out = append(out, "thinking:"+b.Thinking)
		case ToolCall:
			out = append(out, fmt.Sprintf("call:%s:%v", b.Name, b.Arguments))
		default:
			out = append(out, fmt.Sprintf("block:%T", block))
		}
	}
	return out
}

func assertNoProtocolSyntax(t *testing.T, text string) {
	t.Helper()
	for _, marker := range []string{
		"<dotsfunctioncall", "</dotsfunctioncall", "<dots_function_call", "</dots_function_call",
		"<dots_functioncall", "<dotsfunction_call", "<invoke", "<parameter", "<tool_call", "</tool_call",
	} {
		if strings.Contains(text, marker) {
			t.Errorf("visible text leaked protocol syntax %q: %q", marker, text)
		}
	}
}

func TestNormalizeStreamRecognizesCompletePseudoToolCall(t *testing.T) {
	input := "hello\n" + pseudoEnvelope("ls", "internal") + "\nworld"
	run := runNormalize(t, []string{input}, NormalizeOptions{})

	if len(run.calls) != 1 {
		t.Fatalf("live tool calls = %d, want 1", len(run.calls))
	}
	if run.calls[0].Name != "ls" || run.calls[0].Arguments["path"] != "internal" {
		t.Errorf("call = %+v, want ls/internal", run.calls[0])
	}
	if run.text != "hello\n\n\nworld" {
		t.Errorf("live text = %q, want the envelope omitted with completed-text separators", run.text)
	}
	if run.text != run.msg.Text() {
		t.Errorf("live text %q != completed text %q", run.text, run.msg.Text())
	}
	assertNoProtocolSyntax(t, run.text)
	assertNoProtocolSyntax(t, run.msg.Text())

	final := run.msg.ToolCalls()
	if len(final) != 1 || final[0].Name != "ls" {
		t.Fatalf("final tool calls = %+v, want one ls", final)
	}
	if run.msg.StopReason != StopToolUse {
		t.Errorf("stop reason = %q, want %q", run.msg.StopReason, StopToolUse)
	}
	if len(run.diags) != 1 || run.diags[0].Kind != DiagnosticPseudoToolCall {
		t.Errorf("diagnostics = %+v, want one recognized-call report", run.diags)
	}
}

func TestNormalizeStreamRecognizesFragmentedOpeningMarker(t *testing.T) {
	run := runNormalize(t, []string{
		"a<dot",
		"sfunctioncall><invoke name=\"ls\"><parameter name=\"path\">x</parameter></invoke></dotsfunctioncall>b",
	}, NormalizeOptions{})
	if len(run.calls) != 1 || run.calls[0].Name != "ls" {
		t.Fatalf("calls = %+v, want one ls", run.calls)
	}
	if run.text != "a\nb" {
		t.Errorf("live text = %q, want %q", run.text, "a\nb")
	}
}

func TestNormalizeStreamRecognizesFragmentedClosingMarker(t *testing.T) {
	run := runNormalize(t, []string{
		"<dotsfunctioncall><invoke name=\"ls\"><parameter name=\"path\">x</parameter></invoke></dots",
		"functioncall>",
	}, NormalizeOptions{})
	if len(run.calls) != 1 || run.calls[0].Name != "ls" {
		t.Fatalf("calls = %+v, want one ls", run.calls)
	}
	if run.text != "" {
		t.Errorf("live text = %q, want empty", run.text)
	}
}

func TestNormalizeStreamIsStableAcrossArbitraryFragmentation(t *testing.T) {
	input := "intro\n" +
		pseudoEnvelope("ls", "backend") +
		"between\n" +
		pseudoEnvelope("read", "main.go") +
		"tail"

	baseline := runNormalize(t, []string{input}, NormalizeOptions{})
	if len(baseline.calls) != 2 {
		t.Fatalf("baseline calls = %d, want 2", len(baseline.calls))
	}

	for split := 0; split <= len(input); split++ {
		name := fmt.Sprintf("split_%d", split)
		t.Run(name, func(t *testing.T) {
			run := runNormalize(t, []string{input[:split], input[split:]}, NormalizeOptions{})
			if run.text != baseline.text {
				t.Fatalf("live text = %q, want %q (chunking must not change visible text)", run.text, baseline.text)
			}
			if !reflect.DeepEqual(run.calls, baseline.calls) {
				t.Fatalf("live calls = %+v, want %+v", run.calls, baseline.calls)
			}
			if !reflect.DeepEqual(run.msg.Content, baseline.msg.Content) {
				t.Fatalf("final content = %#v, want %#v", run.msg.Content, baseline.msg.Content)
			}
		})
	}

	// Byte-at-a-time is the most adversarial legal chunking of the same bytes.
	byteChunks := make([]string, 0, len(input))
	for i := 0; i < len(input); i++ {
		byteChunks = append(byteChunks, input[i:i+1])
	}
	run := runNormalize(t, byteChunks, NormalizeOptions{})
	if run.text != baseline.text || !reflect.DeepEqual(run.calls, baseline.calls) {
		t.Fatalf("byte-at-a-time changed the result: text %q calls %+v", run.text, run.calls)
	}
	if !reflect.DeepEqual(run.msg.Content, baseline.msg.Content) {
		t.Fatalf("byte-at-a-time final content changed: %#v", run.msg.Content)
	}
}

func TestNormalizeMessagePreservesTextToolOrdering(t *testing.T) {
	msg := AssistantMessage{
		StopReason: StopEndTurn,
		Content: []ContentBlock{
			TextContent{Text: "before "},
			TextContent{Text: pseudoEnvelope("ls", "a")},
			TextContent{Text: " middle "},
			ThinkingContent{Thinking: "plan"},
			TextContent{Text: pseudoEnvelope("read", "b")},
			ToolCall{ID: "native-1", Name: "grep", Arguments: map[string]any{"pattern": "x"}},
			TextContent{Text: " after"},
		},
	}
	got := NormalizeMessage(msg, NormalizeOptions{})
	want := []string{
		"text:before ",
		"call:ls:map[path:a]",
		"text: middle ",
		"thinking:plan",
		"call:read:map[path:b]",
		"call:grep:map[pattern:x]",
		"text: after",
	}
	if diff := describeBlocks(got.Content); !reflect.DeepEqual(diff, want) {
		t.Fatalf("blocks = %#v, want %#v", diff, want)
	}
	if got.StopReason != StopToolUse {
		t.Errorf("stop reason = %q, want %q", got.StopReason, StopToolUse)
	}
}

func TestNormalizeMessageMixesNativeAndPseudoCallsInOrder(t *testing.T) {
	msg := AssistantMessage{
		StopReason: StopEndTurn,
		Content: []ContentBlock{
			TextContent{Text: "start "},
			ToolCall{ID: "native-1", Name: "grep", Arguments: map[string]any{"pattern": "a"}},
			TextContent{Text: pseudoEnvelope("ls", "one")},
			ToolCall{ID: "native-2", Name: "grep", Arguments: map[string]any{"pattern": "b"}},
			TextContent{Text: " note "},
			TextContent{Text: pseudoEnvelope("read", "two")},
		},
	}
	got := NormalizeMessage(msg, NormalizeOptions{})
	blocks := describeBlocks(got.Content)
	want := []string{
		"text:start ",
		"call:grep:map[pattern:a]",
		"call:ls:map[path:one]",
		"call:grep:map[pattern:b]",
		"text: note ",
		"call:read:map[path:two]",
	}
	if !reflect.DeepEqual(blocks, want) {
		t.Fatalf("blocks = %#v, want %#v", blocks, want)
	}
	if got.StopReason != StopToolUse {
		t.Errorf("stop reason = %q, want %q", got.StopReason, StopToolUse)
	}
}

func TestNormalizeMessageSupportsMultipleConsecutiveCalls(t *testing.T) {
	input := pseudoEnvelope("ls", "a") + pseudoEnvelope("ls", "b") + pseudoEnvelope("ls", "c")
	got := NormalizeMessage(AssistantMessage{
		Content:    []ContentBlock{TextContent{Text: input}},
		StopReason: StopEndTurn,
	}, NormalizeOptions{})

	calls := got.ToolCalls()
	if len(calls) != 3 {
		t.Fatalf("calls = %d, want 3", len(calls))
	}
	for i, want := range []string{"a", "b", "c"} {
		if calls[i].Arguments["path"] != want {
			t.Errorf("call %d path = %v, want %q", i, calls[i].Arguments["path"], want)
		}
		if calls[i].ID == "" {
			t.Errorf("call %d has no id", i)
		}
	}
	if got.Text() != "" {
		t.Errorf("text = %q, want empty", got.Text())
	}
}

func TestNormalizeMessageRecognizesGenericToolCallPayloads(t *testing.T) {
	cases := map[string]struct {
		body string
		want []ToolCall
	}{
		"object": {
			body: `<tool_call>{"name":"ls","arguments":{"path":"a"}}</tool_call>`,
			want: []ToolCall{{Name: "ls", Arguments: map[string]any{"path": "a"}}},
		},
		"encoded arguments": {
			body: `<tool_call>{"name":"ls","arguments":"{\"path\":\"b\"}"}</tool_call>`,
			want: []ToolCall{{Name: "ls", Arguments: map[string]any{"path": "b"}}},
		},
		"function wrapper": {
			body: `<tool_call>{"function":{"name":"read","arguments":{"path":"c"}}}</tool_call>`,
			want: []ToolCall{{Name: "read", Arguments: map[string]any{"path": "c"}}},
		},
		"array": {
			body: `<tool_call>[{"name":"ls","arguments":{"path":"d"}},{"name":"read","arguments":{"path":"e"}}]</tool_call>`,
			want: []ToolCall{
				{Name: "ls", Arguments: map[string]any{"path": "d"}},
				{Name: "read", Arguments: map[string]any{"path": "e"}},
			},
		},
		"xml invoke body": {
			body: `<tool_call><invoke name="ls"><parameter name="path">f</parameter></invoke></tool_call>`,
			want: []ToolCall{{Name: "ls", Arguments: map[string]any{"path": "f"}}},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := NormalizeMessage(AssistantMessage{
				Content: []ContentBlock{TextContent{Text: "before " + tc.body + " after"}},
			}, NormalizeOptions{})
			calls := got.ToolCalls()
			if len(calls) != len(tc.want) {
				t.Fatalf("calls = %d, want %d", len(calls), len(tc.want))
			}
			for i := range calls {
				if calls[i].Name != tc.want[i].Name {
					t.Errorf("call %d name = %q, want %q", i, calls[i].Name, tc.want[i].Name)
				}
				if !reflect.DeepEqual(calls[i].Arguments, tc.want[i].Arguments) {
					t.Errorf("call %d args = %#v, want %#v", i, calls[i].Arguments, tc.want[i].Arguments)
				}
			}
			// Text() separates the two remaining text blocks with one newline
			// because the canonical tool call sits between them.
			if got.Text() != "before \n after" {
				t.Errorf("text = %q, want only the surrounding prose", got.Text())
			}
		})
	}
}

func TestNormalizeMessageRejectsMalformedGenericToolCall(t *testing.T) {
	cases := []string{
		`<tool_call>{"name":"","arguments":{"path":"a"}}</tool_call>`,
		`<tool_call>{"name":"ls","arguments":"not json"}</tool_call>`,
		`<tool_call>{"arguments":{"path":"a"}}</tool_call>`,
		`<tool_call>not json and not invoke</tool_call>`,
		`<tool_call>[]</tool_call>`,
	}
	for _, input := range cases {
		got := NormalizeMessage(AssistantMessage{
			Content: []ContentBlock{TextContent{Text: input}},
		}, NormalizeOptions{})
		if calls := got.ToolCalls(); len(calls) != 0 {
			t.Errorf("input %q fabricated calls %+v", input, calls)
		}
		assertNoProtocolSyntax(t, got.Text())
		if !strings.Contains(got.Text(), "could not be normalized") {
			t.Errorf("text = %q, want a truthful normalization failure", got.Text())
		}
		if !got.ProtocolResidue {
			t.Errorf("input %q was not marked as protocol residue", input)
		}
	}
}

func TestNormalizeMessageExpandsMultipleInvokesInOneEnvelope(t *testing.T) {
	envelope := `<dotsfunctioncall>
  <invoke name="ls"><parameter name="path">a</parameter></invoke>
  <invoke name="read"><parameter name="path">b</parameter></invoke>
</dotsfunctioncall>`
	got := NormalizeMessage(AssistantMessage{
		Content: []ContentBlock{TextContent{Text: envelope}},
	}, NormalizeOptions{})
	calls := got.ToolCalls()
	if len(calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(calls))
	}
	if calls[0].Name != "ls" || calls[0].Arguments["path"] != "a" {
		t.Errorf("call 0 = %+v, want ls/a", calls[0])
	}
	if calls[1].Name != "read" || calls[1].Arguments["path"] != "b" {
		t.Errorf("call 1 = %+v, want read/b", calls[1])
	}
}

// TestNormalizeMessageHandlesToolRecapTool reproduces the observed dots
// failure shape: several tool calls, assistant recap text, then another call.
// The recap must stay visible and the later call must remain a call.
func TestNormalizeMessageHandlesToolRecapTool(t *testing.T) {
	input := pseudoEnvelope("ls", "backend/cmd") +
		pseudoEnvelope("ls", "backend/internal") +
		pseudoEnvelope("ls", "backend/pkg") +
		"Recap: I inspected the backend layout.\n" +
		pseudoEnvelope("read", "backend/main.go")
	got := NormalizeMessage(AssistantMessage{
		Content:    []ContentBlock{TextContent{Text: input}},
		StopReason: StopEndTurn,
	}, NormalizeOptions{})

	blocks := describeBlocks(got.Content)
	want := []string{
		"call:ls:map[path:backend/cmd]",
		"call:ls:map[path:backend/internal]",
		"call:ls:map[path:backend/pkg]",
		"text:Recap: I inspected the backend layout.\n",
		"call:read:map[path:backend/main.go]",
	}
	if !reflect.DeepEqual(blocks, want) {
		t.Fatalf("blocks = %#v, want %#v", blocks, want)
	}
	assertNoProtocolSyntax(t, got.Text())
}

func TestNormalizeMessageRejectsMalformedEnvelope(t *testing.T) {
	cases := map[string]string{
		"mismatched tags": "<dotsfunctioncall><invoke name=\"ls\"><parameter name=\"path\">x</invoke></dotsfunctioncall>",
		"no invoke":       "<dotsfunctioncall>just text</dotsfunctioncall>",
		"no invoke name":  "<dotsfunctioncall><invoke><parameter name=\"path\">x</parameter></invoke></dotsfunctioncall>",
		"no param name":   "<dotsfunctioncall><invoke name=\"ls\"><parameter>x</parameter></invoke></dotsfunctioncall>",
	}
	for name, envelope := range cases {
		t.Run(name, func(t *testing.T) {
			input := "before " + envelope + " after"
			got := NormalizeMessage(AssistantMessage{
				Content: []ContentBlock{TextContent{Text: input}},
			}, NormalizeOptions{})

			if calls := got.ToolCalls(); len(calls) != 0 {
				t.Fatalf("fabricated calls = %+v, want none", calls)
			}
			assertNoProtocolSyntax(t, got.Text())
			if !strings.Contains(got.Text(), "could not be normalized") {
				t.Errorf("text = %q, want a truthful normalization failure", got.Text())
			}
			if !got.ProtocolResidue {
				t.Errorf("%s was not marked as protocol residue", name)
			}
		})
	}
}

func TestNormalizeMessageRejectsDuplicateEnvelopeMarkers(t *testing.T) {
	input := `<dotsfunctioncall><dotsfunctioncall><invoke name="ls"><parameter name="path">x</parameter></invoke></dotsfunctioncall></dotsfunctioncall>`
	got := NormalizeMessage(AssistantMessage{
		Content: []ContentBlock{TextContent{Text: input}},
	}, NormalizeOptions{})
	if calls := got.ToolCalls(); len(calls) != 0 {
		t.Fatalf("fabricated calls = %+v, want none", calls)
	}
	assertNoProtocolSyntax(t, got.Text())
	if !got.ProtocolResidue {
		t.Error("duplicate envelope markers were not marked as residue")
	}
}

func TestNormalizeMessageReplacesIncompleteEnvelopeWithDiagnostic(t *testing.T) {
	input := "before <dotsfunctioncall><invoke name=\"ls\"><parameter name=\"path\">x"
	var diags []NormalizationDiagnostic
	got := NormalizeMessage(AssistantMessage{
		Content: []ContentBlock{TextContent{Text: input}},
	}, NormalizeOptions{OnDiagnostic: func(d NormalizationDiagnostic) { diags = append(diags, d) }})

	if calls := got.ToolCalls(); len(calls) != 0 {
		t.Fatalf("fabricated calls = %+v, want none", calls)
	}
	assertNoProtocolSyntax(t, got.Text())
	if !strings.Contains(got.Text(), "before ") || !strings.Contains(got.Text(), "could not be normalized") {
		t.Errorf("text = %q, want ordinary prose plus a truthful failure", got.Text())
	}
	if !got.ProtocolResidue {
		t.Error("an incomplete envelope was not marked as residue")
	}
	if !hasDiagnostic(diags, DiagnosticIncompletePseudoToolCall) {
		t.Errorf("diagnostics = %+v, want an incomplete-envelope report", diags)
	}
}

func TestNormalizeMessageLeavesOrdinaryXMLAndUnknownMarkersAlone(t *testing.T) {
	cases := []string{
		"Use <invoke name=\"ls\"> in your answer, but this is prose.",
		"<unknown_call><invoke name=\"ls\"><parameter name=\"path\">x</parameter></invoke></unknown_call>",
		"<function>read</function> and <parameter>path</parameter> are just words here.",
	}
	for _, input := range cases {
		got := NormalizeMessage(AssistantMessage{
			Content: []ContentBlock{TextContent{Text: input}},
		}, NormalizeOptions{})
		if calls := got.ToolCalls(); len(calls) != 0 {
			t.Errorf("input %q produced fabricated calls %+v", input, calls)
		}
		if got.Text() != input {
			t.Errorf("text = %q, want %q", got.Text(), input)
		}
	}
}

func TestNormalizeMessageReturnsOversizedEnvelopeAsText(t *testing.T) {
	input := "<dotsfunctioncall>" + strings.Repeat("a", 500) + "</dotsfunctioncall>"
	var diags []NormalizationDiagnostic
	got := NormalizeMessage(AssistantMessage{
		Content: []ContentBlock{TextContent{Text: input}},
	}, NormalizeOptions{
		MaxEnvelopeBytes: 64,
		OnDiagnostic:     func(d NormalizationDiagnostic) { diags = append(diags, d) },
	})

	if calls := got.ToolCalls(); len(calls) != 0 {
		t.Fatalf("fabricated calls = %+v, want none", calls)
	}
	assertNoProtocolSyntax(t, got.Text())
	if !strings.Contains(got.Text(), "could not be normalized") {
		t.Errorf("text = %q, want a truthful normalization failure", got.Text())
	}
	if !got.ProtocolResidue {
		t.Error("an oversized envelope was not marked as residue")
	}
	if !hasDiagnostic(diags, DiagnosticOversizedPseudoToolCall) {
		t.Errorf("diagnostics = %+v, want an oversized-envelope report", diags)
	}
}

func TestNormalizerRetainedStateIsBounded(t *testing.T) {
	const limit = 128
	maxClose := 0
	for _, protocol := range defaultPseudoProtocols() {
		if len(protocol.close) > maxClose {
			maxClose = len(protocol.close)
		}
	}
	normalizer := newTextNormalizer(NormalizeOptions{MaxEnvelopeBytes: limit})
	normalizer.feed("<dotsfunctioncall>")
	chunk := strings.Repeat("a", 32)
	for i := 0; i < 200; i++ {
		normalizer.feed(chunk)
		if len(normalizer.body) > limit || len(normalizer.raw) > limit {
			t.Fatalf("after chunk %d: body=%d raw=%d, want <= %d", i, len(normalizer.body), len(normalizer.raw), limit)
		}
		if len(normalizer.pending) >= maxClose {
			// pending may hold at most one close-marker prefix.
			t.Fatalf("after chunk %d: pending=%d, want < close marker length %d",
				i, len(normalizer.pending), maxClose)
		}
	}
}

func TestNormalizeCoercesParametersUsingToolSchema(t *testing.T) {
	tools := []ToolSpec{{
		Name: "demo",
		Parameters: []byte(`{
			"type":"object",
			"properties":{
				"path":{"type":"string"},
				"limit":{"type":"integer"},
				"verbose":{"type":"boolean"},
				"tags":{"type":"array"},
				"options":{"type":"object"}
			}
		}`),
	}}
	envelope := `<dotsfunctioncall>
  <invoke name="demo">
    <parameter name="path">123</parameter>
    <parameter name="limit">10</parameter>
    <parameter name="verbose">true</parameter>
    <parameter name="tags">["a","b"]</parameter>
    <parameter name="options">{"x":1}</parameter>
  </invoke>
</dotsfunctioncall>`
	got := NormalizeMessage(AssistantMessage{
		Content: []ContentBlock{TextContent{Text: envelope}},
	}, NormalizeOptions{Tools: tools})

	calls := got.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	args := calls[0].Arguments
	if args["path"] != "123" {
		t.Errorf("path = %#v, want the string \"123\" because the schema says string", args["path"])
	}
	if args["limit"] != float64(10) {
		t.Errorf("limit = %#v, want float64(10)", args["limit"])
	}
	if args["verbose"] != true {
		t.Errorf("verbose = %#v, want true", args["verbose"])
	}
	if !reflect.DeepEqual(args["tags"], []any{"a", "b"}) {
		t.Errorf("tags = %#v, want [a b]", args["tags"])
	}
	if !reflect.DeepEqual(args["options"], map[string]any{"x": float64(1)}) {
		t.Errorf("options = %#v, want map[x:1]", args["options"])
	}
}

func TestNormalizeMessageLeavesNativeToolCallsUnchanged(t *testing.T) {
	native := ToolCall{ID: "call_1", Name: "read", Arguments: map[string]any{"path": "a.go"}}
	msg := AssistantMessage{
		Content: []ContentBlock{
			TextContent{Text: "hello"},
			native,
		},
		StopReason: StopToolUse,
	}
	got := NormalizeMessage(msg, NormalizeOptions{})
	if !reflect.DeepEqual(got.Content, msg.Content) {
		t.Fatalf("content = %#v, want native content unchanged %#v", got.Content, msg.Content)
	}
	if got.StopReason != StopToolUse {
		t.Errorf("stop reason = %q, want %q", got.StopReason, StopToolUse)
	}
}

func TestNormalizeStreamForwardsOrdinaryTextBeforeCompletion(t *testing.T) {
	ctx := context.Background()
	input := newStream(4)
	output := NormalizeStream(ctx, input, NormalizeOptions{})

	input.publish(ctx, EventStart{Message: AssistantMessage{}})
	input.publish(ctx, EventTextDelta{Delta: "hello"})

	deadline := time.After(2 * time.Second)
	received := false
	for !received {
		select {
		case event := <-output.Events:
			delta, ok := event.(EventTextDelta)
			if !ok {
				continue
			}
			if delta.Delta != "hello" {
				t.Fatalf("text delta = %q, want hello", delta.Delta)
			}
			received = true
		case <-deadline:
			t.Fatal("ordinary text was buffered until completion instead of streaming")
		}
	}

	msg := AssistantMessage{Content: []ContentBlock{TextContent{Text: "hello"}}, StopReason: StopEndTurn}
	input.publish(ctx, EventDone{Message: msg})
	input.finish(msg, nil)
	for range output.Events {
	}
	if _, err := output.Result(); err != nil {
		t.Fatalf("Result: %v", err)
	}
}

func TestNormalizerWithholdsAnAmbiguousMarkerPrefix(t *testing.T) {
	normalizer := newTextNormalizer(NormalizeOptions{})
	if items := normalizer.feed("<dot"); len(items) != 0 {
		t.Fatalf("feed(<dot) emitted %#v, want the possible marker retained", items)
	}
	items := normalizer.feed("sfunctioncall><invoke name=\"ls\"><parameter name=\"path\">x</parameter></invoke></dotsfunctioncall>")
	if len(items) != 1 || !items[0].isCall || items[0].call.Name != "ls" {
		t.Fatalf("resolved items = %#v, want one canonical call", items)
	}
}

func TestNormalizeStreamLiveTextMatchesCompletedText(t *testing.T) {
	input := "a" + pseudoEnvelope("ls", "one") + "b" + pseudoEnvelope("ls", "two") + "c"
	run := runNormalize(t, []string{input}, NormalizeOptions{})

	// AssistantMessage.Text joins the remaining text blocks with one newline.
	if run.msg.Text() != "a\nb\nc" {
		t.Fatalf("completed text = %q, want %q", run.msg.Text(), "a\nb\nc")
	}
	if run.text != run.msg.Text() {
		t.Fatalf("live text = %q, completed text = %q: a renderer would republish the tail",
			run.text, run.msg.Text())
	}
}

func TestNormalizeStreamLiveTextMatchesCompletedTextAcrossNativeCall(t *testing.T) {
	msg := AssistantMessage{
		Content: []ContentBlock{
			TextContent{Text: "a"},
			ToolCall{ID: "c1", Name: "read", Arguments: map[string]any{"path": "x"}},
			TextContent{Text: "b"},
		},
		StopReason: StopToolUse,
	}
	stream := newStream(8)
	ctx := context.Background()
	stream.publish(ctx, EventStart{Message: AssistantMessage{}})
	stream.publish(ctx, EventTextDelta{Delta: "a"})
	stream.publish(ctx, EventToolCallStart{ID: "c1", Name: "read"})
	stream.publish(ctx, EventToolCallEnd{ID: "c1", Name: "read", Arguments: map[string]any{"path": "x"}})
	stream.publish(ctx, EventTextDelta{Delta: "b"})
	stream.publish(ctx, EventDone{Message: msg})
	stream.finish(msg, nil)

	normalized := NormalizeStream(ctx, stream, NormalizeOptions{})
	var text strings.Builder
	for event := range normalized.Events {
		if delta, ok := event.(EventTextDelta); ok {
			text.WriteString(delta.Delta)
		}
	}
	if text.String() != "a\nb" || text.String() != msg.Text() {
		t.Fatalf("live text = %q, completed = %q, want a newline between the text blocks",
			text.String(), msg.Text())
	}
}

func TestNormalizeStreamPassesNativeToolEventsThrough(t *testing.T) {
	msg := AssistantMessage{
		Content: []ContentBlock{
			TextContent{Text: "hello"},
			ToolCall{ID: "call_1", Name: "read", Arguments: map[string]any{"path": "a.go"}},
		},
		StopReason: StopToolUse,
	}
	stream := newStream(8)
	ctx := context.Background()
	stream.publish(ctx, EventStart{Message: AssistantMessage{}})
	stream.publish(ctx, EventTextDelta{Delta: "hello"})
	stream.publish(ctx, EventToolCallStart{ID: "call_1", Name: "read"})
	stream.publish(ctx, EventToolCallDelta{ID: "call_1", Delta: `{"path":`})
	stream.publish(ctx, EventToolCallEnd{ID: "call_1", Name: "read", Arguments: map[string]any{"path": "a.go"}})
	stream.publish(ctx, EventDone{Message: msg})
	stream.finish(msg, nil)

	normalized := NormalizeStream(ctx, stream, NormalizeOptions{})
	var text strings.Builder
	var callEnds []EventToolCallEnd
	for event := range normalized.Events {
		switch e := event.(type) {
		case EventTextDelta:
			text.WriteString(e.Delta)
		case EventToolCallEnd:
			callEnds = append(callEnds, e)
		}
	}
	if text.String() != "hello" {
		t.Errorf("text = %q, want hello", text.String())
	}
	if len(callEnds) != 1 || callEnds[0].ID != "call_1" || callEnds[0].Name != "read" {
		t.Fatalf("native events = %+v, want the original call", callEnds)
	}
	final, err := normalized.Result()
	if err != nil {
		t.Fatalf("Result: %v", err)
	}
	if !reflect.DeepEqual(final.Content, msg.Content) {
		t.Fatalf("final content = %#v, want native content unchanged", final.Content)
	}
}

func TestNormalizeRegisteredProtocolIsRecognized(t *testing.T) {
	protocols := []pseudoProtocol{{
		name:  "synthetic",
		open:  []byte("<call>"),
		close: []byte("</call>"),
		parse: func(body string, _ *argumentCoercer) ([]ToolCall, error) {
			return []ToolCall{{Name: strings.TrimSpace(body)}}, nil
		},
	}}
	got := NormalizeMessage(AssistantMessage{
		Content: []ContentBlock{TextContent{Text: "before<call>ls</call>after"}},
	}, NormalizeOptions{protocols: protocols})

	blocks := describeBlocks(got.Content)
	want := []string{"text:before", "call:ls:map[]", "text:after"}
	if !reflect.DeepEqual(blocks, want) {
		t.Fatalf("blocks = %#v, want %#v", blocks, want)
	}
}

func TestNormalizeMessageDoesNotFabricateFromEmptyEnvelope(t *testing.T) {
	envelope := "<dotsfunctioncall></dotsfunctioncall>"
	got := NormalizeMessage(AssistantMessage{
		Content: []ContentBlock{TextContent{Text: envelope}},
	}, NormalizeOptions{})
	if calls := got.ToolCalls(); len(calls) != 0 {
		t.Fatalf("fabricated calls = %+v, want none", calls)
	}
	assertNoProtocolSyntax(t, got.Text())
	if !got.ProtocolResidue {
		t.Error("an empty envelope was not marked as residue")
	}
}

func TestNormalizeMessageReportsRecognizedCall(t *testing.T) {
	var diags []NormalizationDiagnostic
	got := NormalizeMessage(AssistantMessage{
		Content: []ContentBlock{TextContent{Text: pseudoEnvelope("ls", "x")}},
	}, NormalizeOptions{OnDiagnostic: func(d NormalizationDiagnostic) { diags = append(diags, d) }})
	if len(got.ToolCalls()) != 1 {
		t.Fatalf("calls = %d, want 1", len(got.ToolCalls()))
	}
	if !hasDiagnostic(diags, DiagnosticPseudoToolCall) {
		t.Errorf("diagnostics = %+v, want a recognized-call report", diags)
	}
}

// The provider has emitted the underscored spelling in production. It is the
// same logical envelope and must converge on the same canonical call.
func TestNormalizeMessageRecognizesUnderscoredDotsEnvelope(t *testing.T) {
	envelope := `<dots_function_call>
  <invoke name="ls"><parameter name="path">cmd</parameter></invoke>
</dots_function_call>`
	got := NormalizeMessage(AssistantMessage{
		Content: []ContentBlock{TextContent{Text: envelope}},
	}, NormalizeOptions{})
	calls := got.ToolCalls()
	if len(calls) != 1 || calls[0].Name != "ls" || calls[0].Arguments["path"] != "cmd" {
		t.Fatalf("calls = %+v, want the canonical ls call", calls)
	}
	if got.Text() != "" {
		t.Fatalf("text = %q, want the envelope consumed", got.Text())
	}
	if got.ProtocolResidue {
		t.Fatal("a fully normalized envelope was reported as residue")
	}
}

// An opening without underscores can be closed with the underscored marker;
// both spellings belong to one envelope family.
func TestNormalizeMessageAcceptsMixedCloseMarker(t *testing.T) {
	envelope := `<dotsfunctioncall><invoke name="read"><parameter name="path">a.go</parameter></invoke></dots_function_call>`
	got := NormalizeMessage(AssistantMessage{
		Content: []ContentBlock{TextContent{Text: envelope}},
	}, NormalizeOptions{})
	calls := got.ToolCalls()
	if len(calls) != 1 || calls[0].Name != "read" {
		t.Fatalf("calls = %+v, want the canonical read call", calls)
	}
}

// Arbitrary fragmentation of the underscored envelope must converge exactly.
func TestNormalizeStreamRecognizesUnderscoredEnvelopeAcrossFragmentation(t *testing.T) {
	envelope := `<dots_function_call><invoke name="ls"><parameter name="path">x</parameter></invoke></dots_function_call>`
	for split := 1; split < len(envelope); split++ {
		run := runNormalize(t, []string{envelope[:split], envelope[split:]}, NormalizeOptions{})
		if len(run.calls) != 1 || run.calls[0].Name != "ls" {
			t.Fatalf("split %d: calls = %+v, want one ls call", split, run.calls)
		}
		for _, marker := range []string{"<dots_function_call", "</dots_function_call", "<invoke", "<parameter"} {
			if strings.Contains(run.text, marker) {
				t.Fatalf("split %d leaked %q into live text: %q", split, marker, run.text)
			}
		}
		if run.msg.ProtocolResidue {
			t.Fatalf("split %d: recognized envelope flagged as residue", split)
		}
	}
}

// An unknown spelling in the dots-call family is still protocol, not prose.
// The final message carries a truthful diagnostic and cannot complete.
func TestNormalizeMessageSanitizesUnknownDotsSpelling(t *testing.T) {
	input := `<dots_protocol_call><invoke name="ls"><parameter name="path">x</parameter></invoke></dots_protocol_call>`
	got := NormalizeMessage(AssistantMessage{
		Content: []ContentBlock{TextContent{Text: input}},
	}, NormalizeOptions{})
	assertNoProtocolSyntax(t, got.Text())
	if strings.Contains(strings.ToLower(got.Text()), "<dots") {
		t.Fatalf("unknown protocol spelling leaked: %q", got.Text())
	}
	if !got.ProtocolResidue {
		t.Fatal("an unknown dots spelling was not marked as residue")
	}
	if len(got.ToolCalls()) != 0 {
		t.Fatalf("fabricated calls = %+v", got.ToolCalls())
	}
}

// The live path must not publish an unknown dots-family spelling either; the
// replacement happens at the provider boundary before the renderer sees it.
func TestNormalizeStreamReplacesUnknownDotsSpellingLive(t *testing.T) {
	input := `before <dots_protocol_call><invoke name="ls"><parameter name="path">x</parameter></invoke></dots_protocol_call> after`
	run := runNormalize(t, []string{input}, NormalizeOptions{})
	assertNoProtocolSyntax(t, run.text)
	if !strings.Contains(run.text, "could not be normalized") {
		t.Fatalf("live text = %q, want a truthful normalization failure", run.text)
	}
	if !run.msg.ProtocolResidue {
		t.Fatal("the live message was not marked as protocol residue")
	}
}

// An envelope that is recognized but cannot be completed is preserved as
// truthful text, and the message is marked so completion cannot call it an
// ordinary answer.
func TestNormalizeMessageFlagsIncompleteEnvelopeAsResidue(t *testing.T) {
	envelope := `<dots_function_call><invoke name="ls"><parameter name="path">x`
	got := NormalizeMessage(AssistantMessage{
		Content: []ContentBlock{TextContent{Text: envelope}},
	}, NormalizeOptions{})
	if !got.ProtocolResidue {
		t.Fatal("an incomplete serialized call was not marked as residue")
	}
	assertNoProtocolSyntax(t, got.Text())
	if len(got.ToolCalls()) != 0 {
		t.Fatalf("fabricated calls = %+v", got.ToolCalls())
	}
}

func hasDiagnostic(diags []NormalizationDiagnostic, kind NormalizationDiagnosticKind) bool {
	for _, diag := range diags {
		if diag.Kind == kind {
			return true
		}
	}
	return false
}
