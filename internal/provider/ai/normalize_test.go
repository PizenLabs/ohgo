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

// longcatEnvelope renders the key/value grammar LongCat 2.5 emits. args are
// alternating key and value strings.
func longcatEnvelope(name string, args ...string) string {
	if len(args)%2 != 0 {
		panic("longcatEnvelope: args must be key/value pairs")
	}
	var b strings.Builder
	b.WriteString("<longcat_tool_call>")
	b.WriteString(name)
	b.WriteString("\n")
	for i := 0; i < len(args); i += 2 {
		b.WriteString("<longcat_arg_key>")
		b.WriteString(args[i])
		b.WriteString("</longcat_arg_key>\n")
		b.WriteString("<longcat_arg_value>")
		b.WriteString(args[i+1])
		b.WriteString("</longcat_arg_value>\n")
	}
	b.WriteString("</longcat_tool_call>")
	return b.String()
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
		"<longcat_tool_call", "</longcat_tool_call", "<longcat_arg_key", "<longcat_arg_value",
	} {
		if strings.Contains(text, marker) {
			t.Errorf("visible text leaked protocol syntax %q: %q", marker, text)
		}
	}
}

// assertNoDotsFamily checks the whole family, including spellings this build
// has never registered. A test that only names the known spellings cannot
// prove the live-stream class is closed.
func assertNoDotsFamily(t *testing.T, text string) {
	t.Helper()
	lower := strings.ToLower(text)
	for _, marker := range []string{"<dots", "</dots", "<invoke", "<parameter", "<tool_call", "</tool_call"} {
		if strings.Contains(lower, marker) {
			t.Errorf("visible text leaked protocol bytes %q: %q", marker, text)
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

// LongCat 2.5 emits one envelope per call, often several consecutively in one
// text run. Every envelope must become a canonical call; none may reach the
// transcript as prose.
func TestNormalizeMessageRecognizesLongCatToolCalls(t *testing.T) {
	input := strings.Join([]string{
		longcatEnvelope("glob", "pattern", "**/*.go"),
		longcatEnvelope("glob", "pattern", "cmd/**/*"),
		longcatEnvelope("glob", "pattern", "internal/**/*"),
		longcatEnvelope("glob", "pattern", "**/.env*"),
		longcatEnvelope("glob", "pattern", "docs/**/*"),
	}, "\n")

	got := NormalizeMessage(AssistantMessage{
		Content:    []ContentBlock{TextContent{Text: input}},
		StopReason: StopEndTurn,
	}, NormalizeOptions{})

	calls := got.ToolCalls()
	patterns := []string{"**/*.go", "cmd/**/*", "internal/**/*", "**/.env*", "docs/**/*"}
	if len(calls) != len(patterns) {
		t.Fatalf("calls = %d, want %d", len(calls), len(patterns))
	}
	ids := make(map[string]bool, len(calls))
	for i, call := range calls {
		if call.Name != "glob" {
			t.Errorf("call %d name = %q, want glob", i, call.Name)
		}
		if call.Arguments["pattern"] != patterns[i] {
			t.Errorf("call %d pattern = %#v, want %q", i, call.Arguments["pattern"], patterns[i])
		}
		if call.ID == "" || ids[call.ID] {
			t.Errorf("call %d id = %q, want unique and non-empty", i, call.ID)
		}
		ids[call.ID] = true
	}
	if strings.TrimSpace(got.Text()) != "" {
		t.Errorf("text = %q, want only the separators between consumed envelopes", got.Text())
	}
	if got.ProtocolResidue {
		t.Error("recognized LongCat calls were reported as protocol residue")
	}
	if got.StopReason != StopToolUse {
		t.Errorf("stop reason = %q, want %q", got.StopReason, StopToolUse)
	}
}

func TestNormalizeLongCatCoercesArgumentsUsingToolSchema(t *testing.T) {
	tools := []ToolSpec{{
		Name: "glob",
		Parameters: []byte(`{
			"type":"object",
			"properties":{
				"pattern":{"type":"string"},
				"limit":{"type":"integer"},
				"gitignore":{"type":"boolean"}
			}
		}`),
	}}
	got := NormalizeMessage(AssistantMessage{
		Content: []ContentBlock{TextContent{Text: longcatEnvelope(
			"glob", "limit", "50", "pattern", "**/*", "gitignore", "false",
		)}},
	}, NormalizeOptions{Tools: tools})

	calls := got.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	args := calls[0].Arguments
	if args["pattern"] != "**/*" {
		t.Errorf("pattern = %#v, want the string `**/*`", args["pattern"])
	}
	if args["limit"] != float64(50) {
		t.Errorf("limit = %#v, want float64(50)", args["limit"])
	}
	if args["gitignore"] != false {
		t.Errorf("gitignore = %#v, want false", args["gitignore"])
	}
}

// The key/value envelope must survive any provider chunk boundary, including
// one byte per chunk.
func TestNormalizeStreamLongCatCallSurvivesArbitraryFragmentation(t *testing.T) {
	envelope := longcatEnvelope("read", "path", "a.go", "line", "12")
	check := func(name string, run normalizationRun) {
		t.Helper()
		if len(run.calls) != 1 {
			t.Fatalf("%s: calls = %+v, want one read call", name, run.calls)
		}
		if run.calls[0].Name != "read" || run.calls[0].Arguments["path"] != "a.go" {
			t.Fatalf("%s: call = %+v, want read/a.go", name, run.calls[0])
		}
		if run.text != "" || run.msg.Text() != "" {
			t.Fatalf("%s: protocol leaked into text: live=%q final=%q", name, run.text, run.msg.Text())
		}
		if run.msg.ProtocolResidue {
			t.Fatalf("%s: recognized envelope flagged as residue", name)
		}
	}

	for split := 1; split < len(envelope); split++ {
		check(fmt.Sprintf("split %d", split), runNormalize(t, []string{envelope[:split], envelope[split:]}, NormalizeOptions{}))
	}

	deltas := make([]string, 0, len(envelope))
	for i := 0; i < len(envelope); i++ {
		deltas = append(deltas, envelope[i:i+1])
	}
	check("one byte per chunk", runNormalize(t, deltas, NormalizeOptions{}))
}

// LongCat-Flash-Chat documents a JSON function object in the same envelope.
// Supporting it costs one branch and keeps the older spelling canonical.
func TestNormalizeMessageRecognizesLongCatJSONEnvelope(t *testing.T) {
	envelope := `<longcat_tool_call>{"name":"glob","arguments":{"pattern":"*.go"}}</longcat_tool_call>`
	got := NormalizeMessage(AssistantMessage{
		Content: []ContentBlock{TextContent{Text: envelope}},
	}, NormalizeOptions{})

	calls := got.ToolCalls()
	if len(calls) != 1 || calls[0].Name != "glob" {
		t.Fatalf("calls = %+v, want one glob call", calls)
	}
	if calls[0].Arguments["pattern"] != "*.go" {
		t.Errorf("pattern = %#v, want *.go", calls[0].Arguments["pattern"])
	}
	if got.Text() != "" {
		t.Errorf("text = %q, want the envelope consumed", got.Text())
	}
}

func TestNormalizeMessageRejectsMalformedLongCatEnvelope(t *testing.T) {
	cases := map[string]string{
		"empty envelope":     "<longcat_tool_call></longcat_tool_call>",
		"no name":            "<longcat_tool_call><longcat_arg_key>pattern</longcat_arg_key><longcat_arg_value>x</longcat_arg_value></longcat_tool_call>",
		"prose name":         "<longcat_tool_call>just text</longcat_tool_call>",
		"missing value":      "<longcat_tool_call>glob<longcat_arg_key>pattern</longcat_arg_key></longcat_tool_call>",
		"unterminated value": "<longcat_tool_call>glob<longcat_arg_key>pattern</longcat_arg_key><longcat_arg_value>x</longcat_tool_call>",
		"duplicate key":      "<longcat_tool_call>glob<longcat_arg_key>pattern</longcat_arg_key><longcat_arg_value>a</longcat_arg_value><longcat_arg_key>pattern</longcat_arg_key><longcat_arg_value>b</longcat_arg_value></longcat_tool_call>",
	}
	for name, envelope := range cases {
		t.Run(name, func(t *testing.T) {
			got := NormalizeMessage(AssistantMessage{
				Content: []ContentBlock{TextContent{Text: "before " + envelope + " after"}},
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

func TestNormalizeMessageFlagsIncompleteLongCatEnvelopeAsResidue(t *testing.T) {
	envelope := longcatEnvelope("glob", "pattern", "*.go")
	envelope = strings.TrimSuffix(envelope, "</longcat_tool_call>")
	got := NormalizeMessage(AssistantMessage{
		Content: []ContentBlock{TextContent{Text: envelope}},
	}, NormalizeOptions{})

	if !got.ProtocolResidue {
		t.Fatal("an incomplete LongCat call was not marked as residue")
	}
	assertNoProtocolSyntax(t, got.Text())
	if len(got.ToolCalls()) != 0 {
		t.Fatalf("fabricated calls = %+v", got.ToolCalls())
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

// An unknown dots-family spelling must not become visible merely because the
// provider split it inside the family name, before a separator, or at any
// other byte. Every split is exercised, plus the one-byte-at-a-time stream.
func TestNormalizeStreamUnknownDotsSpellingNeverLeaksUnderFragmenting(t *testing.T) {
	envelope := `<dots_protocol_call><invoke name="ls"><parameter name="path">x</parameter></invoke></dots_protocol_call>`
	input := "before " + envelope + " after"

	check := func(name, text string, run normalizationRun) {
		t.Helper()
		assertNoDotsFamily(t, text)
		assertNoDotsFamily(t, run.msg.Text())
		if len(run.calls) != 0 {
			t.Fatalf("%s: unknown spelling fabricated calls %+v", name, run.calls)
		}
		if !run.msg.ProtocolResidue {
			t.Fatalf("%s: unknown spelling was not marked as residue", name)
		}
		if !strings.Contains(run.text, "could not be normalized") {
			t.Fatalf("%s: live text = %q, want a truthful diagnostic", name, run.text)
		}
		if run.text != run.msg.Text() {
			t.Fatalf("%s: live text %q != final text %q", name, run.text, run.msg.Text())
		}
	}

	for split := 0; split <= len(input); split++ {
		run := runNormalize(t, []string{input[:split], input[split:]}, NormalizeOptions{})
		check(fmt.Sprintf("split_%d", split), run.text, run)
	}

	chunks := make([]string, 0, len(input))
	for i := 0; i < len(input); i++ {
		chunks = append(chunks, input[i:i+1])
	}
	run := runNormalize(t, chunks, NormalizeOptions{})
	check("byte_at_a_time", run.text, run)
}

// Multiple adjacent unknown envelopes must each be consumed. The scanner may
// not fall out of family mode after the first close.
func TestNormalizeStreamConsumesAdjacentUnknownDotsEnvelopes(t *testing.T) {
	envelope := `<dots_protocol_call><invoke name="ls"><parameter name="path">x</parameter></invoke></dots_protocol_call>`
	input := envelope + envelope
	run := runNormalize(t, []string{input}, NormalizeOptions{})
	assertNoDotsFamily(t, run.text)
	assertNoDotsFamily(t, run.msg.Text())
	if !run.msg.ProtocolResidue {
		t.Fatal("adjacent unknown envelopes were not marked as residue")
	}
	if got := strings.Count(run.text, "could not be normalized"); got != 2 {
		t.Fatalf("diagnostic count = %d, want one per envelope: %q", got, run.text)
	}
}

// A known spelling still canonicalizes when an unknown sibling appears in the
// same message. The unknown one is residue; the known one is a real call.
func TestNormalizeStreamKnownAndUnknownDotsSpellingsCoexist(t *testing.T) {
	unknown := `<dots_protocol_call><invoke name="ls"><parameter name="path">x</parameter></invoke></dots_protocol_call>`
	input := "before " + pseudoEnvelope("read", "README.md") + unknown + " after"
	run := runNormalize(t, []string{input}, NormalizeOptions{})

	if len(run.calls) != 1 || run.calls[0].Name != "read" {
		t.Fatalf("calls = %+v, want the registered read call", run.calls)
	}
	assertNoDotsFamily(t, run.text)
	assertNoDotsFamily(t, run.msg.Text())
	if !run.msg.ProtocolResidue {
		t.Fatal("the unknown sibling was not marked as residue")
	}
}

// An oversized unknown envelope must not resume publishing its body just
// because the retained bound was reached. Markers inside the discarded body
// are protocol bytes, not prose.
func TestNormalizeStreamOversizedUnknownEnvelopeDoesNotLeakBody(t *testing.T) {
	body := `<invoke name="ls"><parameter name="path">` + strings.Repeat("a", 500) + `</parameter></invoke>`
	input := "before <dots_protocol_call>" + body + "</dots_protocol_call> after"
	run := runNormalize(t, []string{input}, NormalizeOptions{MaxEnvelopeBytes: 64})

	assertNoDotsFamily(t, run.text)
	assertNoDotsFamily(t, run.msg.Text())
	if !run.msg.ProtocolResidue {
		t.Fatal("an oversized unknown envelope was not marked as residue")
	}
	if !strings.Contains(run.text, "could not be normalized") {
		t.Fatalf("live text = %q, want a truthful diagnostic", run.text)
	}
	if !strings.Contains(run.text, "after") || !strings.Contains(run.msg.Text(), "after") {
		t.Fatalf("ordinary text after the envelope was lost: %q / %q", run.text, run.msg.Text())
	}
	if len(run.calls) != 0 {
		t.Fatalf("an oversized envelope fabricated calls %+v", run.calls)
	}
}

// Family recognition is case-insensitive; an uppercase spelling fragmented
// byte-by-byte is still one protocol envelope.
func TestNormalizeStreamUppercaseUnknownSpellingIsConsumed(t *testing.T) {
	input := "<DOTS_PROTOCOL_CALL><invoke name=\"ls\"><parameter name=\"path\">x</parameter></invoke></DOTS_PROTOCOL_CALL>"
	chunks := make([]string, 0, len(input))
	for i := 0; i < len(input); i++ {
		chunks = append(chunks, input[i:i+1])
	}
	run := runNormalize(t, chunks, NormalizeOptions{})
	assertNoDotsFamily(t, run.text)
	assertNoDotsFamily(t, run.msg.Text())
	if !run.msg.ProtocolResidue {
		t.Fatal("an uppercase unknown spelling was not marked as residue")
	}
}

// The bounded scanner must not accumulate an unfinished unknown opening tag
// forever. Over the bound it is treated as residue and consumed.
func TestNormalizeStreamUnknownOpeningTagIsBounded(t *testing.T) {
	input := "<dots" + strings.Repeat("x", maxOpenTagBytes*4)
	run := runNormalize(t, []string{input}, NormalizeOptions{})
	assertNoDotsFamily(t, run.text)
	if !run.msg.ProtocolResidue {
		t.Fatal("an overlong family opening was not marked as residue")
	}
	if !strings.Contains(run.text, "could not be normalized") {
		t.Fatalf("live text = %q, want a truthful diagnostic", run.text)
	}
}

// An unknown family envelope that never closes is incomplete protocol, not an
// answer. Its raw bytes must not survive to the final message.
func TestNormalizeStreamIncompleteUnknownEnvelopeIsDiagnosedAtEOF(t *testing.T) {
	input := "before <dots_protocol_call><invoke name=\"ls\"><parameter name=\"path\">x"
	var diags []NormalizationDiagnostic
	run := runNormalize(t, []string{input}, NormalizeOptions{
		OnDiagnostic: func(d NormalizationDiagnostic) { diags = append(diags, d) },
	})
	assertNoDotsFamily(t, run.text)
	assertNoDotsFamily(t, run.msg.Text())
	if !run.msg.ProtocolResidue {
		t.Fatal("an incomplete unknown envelope was not marked as residue")
	}
	if !hasDiagnostic(diags, DiagnosticIncompletePseudoToolCall) {
		t.Fatalf("diagnostics = %+v, want an incomplete-envelope report", diags)
	}
}

// A closing marker that never completes and has no opening is still protocol
// bytes at EOF, not prose. It must not survive into live text or persistence.
func TestNormalizeStreamIncompleteStrayCloseIsResidue(t *testing.T) {
	input := "before </dots_protocol_call"
	run := runNormalize(t, []string{input}, NormalizeOptions{})
	assertNoDotsFamily(t, run.text)
	assertNoDotsFamily(t, run.msg.Text())
	if !run.msg.ProtocolResidue {
		t.Fatal("an incomplete stray close was not marked as residue")
	}
	if run.text != run.msg.Text() {
		t.Fatalf("live text %q != final text %q", run.text, run.msg.Text())
	}
	if !strings.HasPrefix(run.text, "before ") {
		t.Fatalf("ordinary prose before the stray close was lost: %q", run.text)
	}
}

// Documentation is content. A protocol example in inline code, a fenced block,
// or a backslash escape must survive normalization verbatim and must not
// execute anything.
func TestNormalizeQuotedProtocolExamplesRemainContent(t *testing.T) {
	envelope := pseudoEnvelope("ls", "x")
	cases := map[string]string{
		"inline code":    "Use `<dotsfunctioncall>` and `</dotsfunctioncall>` to delimit a call.",
		"fenced block":   "Example:\n```text\n" + envelope + "\n```\nThat is the wire shape.",
		"tilde fence":    "Example:\n~~~\n" + envelope + "\n~~~",
		"escaped marker": `Write \<dotsfunctioncall> literally to mention it.`,
		"several inline": "- `<dots_function_call>`\n- `<dotsfunctioncall>`\n- `</dots_protocol_call>`",
		"plain xml":      "The tool uses <invoke name=\"ls\"> and <parameter name=\"path\">.",
		"longcat inline": "Use `<longcat_tool_call>` and `<longcat_arg_key>` to show the wire shape.",
		"longcat fence":  "Example:\n```text\n" + longcatEnvelope("glob", "pattern", "*.go") + "\n```\nThat is the wire shape.",
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			got := NormalizeMessage(AssistantMessage{
				Content: []ContentBlock{TextContent{Text: input}},
			}, NormalizeOptions{})
			if got.Text() != input {
				t.Fatalf("quoted content changed:\n got %q\nwant %q", got.Text(), input)
			}
			if got.ProtocolResidue {
				t.Fatal("quoted content was classified as protocol residue")
			}
			if calls := got.ToolCalls(); len(calls) != 0 {
				t.Fatalf("a quoted example executed as a call: %+v", calls)
			}
		})
	}
}

// Quote state must survive provider fragmentation exactly like protocol state.
// A fence emitted in one delta and an example in the next is still content.
func TestNormalizeStreamQuotedExampleAcrossDeltasIsContent(t *testing.T) {
	envelope := pseudoEnvelope("ls", "x")
	run := runNormalize(t, []string{
		"```text\n",
		envelope,
		"\n```\n",
	}, NormalizeOptions{})

	want := "```text\n" + envelope + "\n```\n"
	if run.text != want {
		t.Fatalf("live text = %q, want the quoted example preserved", run.text)
	}
	if run.msg.Text() != want {
		t.Fatalf("final text = %q, want the quoted example preserved", run.msg.Text())
	}
	if run.msg.ProtocolResidue {
		t.Fatal("a quoted example was classified as protocol residue")
	}
	if len(run.calls) != 0 {
		t.Fatalf("a quoted example executed as a call: %+v", run.calls)
	}
}

// An unquoted opening marker in prose is indistinguishable from incomplete
// provider protocol, so it remains a protocol problem. Text before it is kept;
// the unterminated envelope body is consumed rather than published as prose.
func TestNormalizeUnquotedProtocolMentionPreservesLeadingProse(t *testing.T) {
	got := NormalizeMessage(AssistantMessage{
		Content: []ContentBlock{TextContent{Text: "The marker <dotsfunctioncall> starts a call."}},
	}, NormalizeOptions{})
	if !got.ProtocolResidue {
		t.Fatal("an unquoted marker was not marked as residue")
	}
	assertNoDotsFamily(t, got.Text())
	if !strings.HasPrefix(got.Text(), "The marker ") ||
		!strings.Contains(got.Text(), "could not be normalized") {
		t.Fatalf("leading prose or diagnostic was lost: %q", got.Text())
	}
}

// Quoted content must not weaken unquoted detection: an unknown spelling
// outside the quote remains residue and still vetoes completion.
func TestNormalizeUnquotedUnknownSpellingStillVetoes(t *testing.T) {
	unknown := `<dots_protocol_call><invoke name="ls"><parameter name="path">x</parameter></invoke></dots_protocol_call>`
	input := "Example in a fence:\n```\n" + unknown + "\n```\nThen emitted: " + unknown
	got := NormalizeMessage(AssistantMessage{
		Content: []ContentBlock{TextContent{Text: input}},
	}, NormalizeOptions{})
	if !got.ProtocolResidue {
		t.Fatal("the unquoted occurrence was not marked as residue")
	}
	if !strings.Contains(got.Text(), "could not be normalized") {
		t.Fatalf("text = %q, want a truthful diagnostic for the unquoted occurrence", got.Text())
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

func TestNormalizeMessageRecordsStructuredFailureStatus(t *testing.T) {
	cases := []struct {
		name string
		text string
		want NormalizationStatus
	}{
		{
			name: "invalid JSON envelope",
			text: `<longcat_tool_call>{"name":"bash","arguments":{"command":` +
				`"npx live-server"}</longcat_tool_call>`,
			want: NormalizationInvalid,
		},
		{
			name: "incomplete envelope",
			text: `<longcat_tool_call>bash<longcat_arg_key>command</longcat_arg_key>` +
				`<longcat_arg_value>npx live-server</longcat_arg_value>`,
			want: NormalizationIncomplete,
		},
		{
			name: "unsupported family spelling",
			text: `<dots_protocol_call><invoke name="bash"><parameter name="command">` +
				`npx live-server</parameter></invoke></dots_protocol_call>`,
			want: NormalizationUnsupported,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := NormalizeMessage(AssistantMessage{
				Content: []ContentBlock{TextContent{Text: tc.text}},
			}, NormalizeOptions{Provider: "test"})
			if len(got.NormalizationFailures) == 0 {
				t.Fatal("failure was not recorded")
			}
			failure := got.NormalizationFailures[0]
			if failure.Status != tc.want {
				t.Fatalf("status = %q, want %q", failure.Status, tc.want)
			}
			if failure.Provider != "test" {
				t.Fatalf("provider = %q, want test", failure.Provider)
			}
			if failure.RawFragment == "" {
				t.Fatal("raw evidence was not retained")
			}
			if tc.want == NormalizationUnsupported && failure.Recoverable {
				t.Fatal("an unsupported spelling was marked recoverable")
			}
			if !got.ProtocolResidue {
				t.Fatal("a failed envelope did not mark protocol residue")
			}
		})
	}
}
