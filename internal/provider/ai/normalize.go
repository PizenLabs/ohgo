package ai

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Pseudo-tool normalization.
//
// A provider is a transport, not a trust boundary. Some models emit a
// serialized tool call inside streamed assistant text instead of (or in
// addition to) a native tool-call channel. Treating that text as ordinary
// assistant prose leaks protocol syntax into the transcript and, worse, hides
// a model-proposed action from the authority/execution pipeline.
//
// NormalizeStream is the boundary between a provider's heterogeneous wire
// output and ohgo's canonical ai events. It converts a recognized, complete,
// successfully parsed pseudo envelope into the same ToolCall representation a
// native tool call uses. It never executes anything: a normalized call still
// travels through the agent loop's authorization, budget, execution, and
// evidence sequence.
//
// Recognition is protocol-registry driven. Adding another serialized syntax
// means adding a pseudoProtocol here; it never means teaching the TUI or the
// execution plane about that syntax.

// DefaultMaxPseudoEnvelopeBytes bounds one recognized pseudo-tool envelope.
//
// The parser must not accumulate unbounded model output while waiting for a
// closing marker. A payload larger than this is not recognized: its bytes are
// returned to the caller as ordinary text and a diagnostic is reported. Large
// edit/write payloads therefore still fit well below this ceiling while a
// runaway or hostile stream cannot grow memory without bound.
const DefaultMaxPseudoEnvelopeBytes = 1 << 20

// NormalizationDiagnosticKind classifies one normalization decision.
type NormalizationDiagnosticKind string

const (
	// DiagnosticPseudoToolCall means a complete pseudo envelope parsed into one
	// or more canonical ToolCalls.
	DiagnosticPseudoToolCall NormalizationDiagnosticKind = "pseudo_tool_call"
	// DiagnosticMalformedPseudoToolCall means a recognized opening marker was
	// present but the envelope could not be parsed, or was interrupted by a
	// native event. The raw bytes stay visible as text; no call is fabricated.
	DiagnosticMalformedPseudoToolCall NormalizationDiagnosticKind = "malformed_pseudo_tool_call"
	// DiagnosticIncompletePseudoToolCall means the stream ended inside an
	// envelope. The raw bytes stay visible as text; no call is fabricated.
	DiagnosticIncompletePseudoToolCall NormalizationDiagnosticKind = "incomplete_pseudo_tool_call"
	// DiagnosticOversizedPseudoToolCall means an envelope exceeded
	// MaxEnvelopeBytes. It is not parsed and is returned as text.
	DiagnosticOversizedPseudoToolCall NormalizationDiagnosticKind = "oversized_pseudo_tool_call"
)

// NormalizationDiagnostic reports one normalization decision.
//
// Diagnostics never include the raw model payload. They are intended for
// debug/audit sinks; the default is silence.
type NormalizationDiagnostic struct {
	Kind      NormalizationDiagnosticKind
	Protocol  string
	Detail    string
	ToolCalls int
}

// NormalizeOptions configures stream normalization.
type NormalizeOptions struct {
	// Tools are the tools offered on this request. Their schemas let the
	// normalizer convert textual XML parameters into the JSON types the tool
	// expects (number, boolean, array, object). Missing or unhelpful schemas
	// leave the parameter as a string.
	Tools []ToolSpec
	// MaxEnvelopeBytes bounds one pseudo-tool envelope. Zero uses
	// DefaultMaxPseudoEnvelopeBytes.
	MaxEnvelopeBytes int
	// OnDiagnostic observes normalization decisions. Nil is silent.
	OnDiagnostic func(NormalizationDiagnostic)

	// protocols is the recognized envelope registry. Nil uses the built-in
	// set. Tests and future transports may extend it without touching the TUI
	// or the execution plane.
	protocols []pseudoProtocol
}

// NormalizeStream wraps a provider stream in the canonical tool-call boundary.
//
// Text deltas are scanned incrementally, so a marker split across any number
// of provider chunks is still recognized and no partial protocol prefix is
// published. Ambiguous suffixes are held back only until they can be resolved;
// ordinary text continues to stream as it arrives.
//
// The returned stream preserves ordering: text, pseudo tool calls, and native
// tool calls appear in the same logical sequence the provider produced. Its
// terminal message is the normalized message DrainStream and the agent loop
// consume. A nil stream is returned unchanged.
func NormalizeStream(ctx context.Context, stream *Stream, opts NormalizeOptions) *Stream {
	if stream == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	out := newStream(64)
	go func() {
		normalizer := newTextNormalizer(opts)
		var (
			final          AssistantMessage
			haveFinal      bool
			streamErr      error
			sawTextDelta   bool
			completionOpts = opts
		)
		// The live parser already reports decisions for streamed text. The
		// final message is normalized again because it is authoritative, but
		// suppressing the duplicate diagnostics keeps one audit event per
		// observed envelope.
		completionDiagnostics := func() {
			if !sawTextDelta {
				completionOpts.OnDiagnostic = opts.OnDiagnostic
				return
			}
			completionOpts.OnDiagnostic = nil
		}

		// AssistantMessage.Text joins its text blocks with a newline. When a
		// tool or thinking block splits the visible text, the live stream must
		// carry that separator too, or a renderer that skips text already
		// revealed live would publish the completed tail a second time.
		var (
			sawText   bool
			textBreak bool
		)
		publishText := func(text string) bool {
			if text == "" {
				return true
			}
			if textBreak && sawText {
				text = "\n" + text
			}
			if !out.publish(ctx, EventTextDelta{Delta: text}) {
				return false
			}
			sawText = true
			textBreak = false
			return true
		}
		publishItems := func(items []parsedItem) bool {
			for _, item := range items {
				if item.isCall {
					call := item.call
					if !out.publish(ctx, EventToolCallStart{ID: call.ID, Name: call.Name}) {
						return false
					}
					if !out.publish(ctx, EventToolCallEnd{
						ID: call.ID, Name: call.Name, Arguments: call.Arguments,
					}) {
						return false
					}
					if sawText {
						textBreak = true
					}
					continue
				}
				if !publishText(item.text) {
					return false
				}
			}
			return true
		}

		for {
			var event StreamEvent
			var ok bool
			select {
			case <-ctx.Done():
				out.finish(AssistantMessage{}, ctx.Err())
				return
			case event, ok = <-stream.Events:
			}
			if !ok {
				break
			}

			switch e := event.(type) {
			case EventStart:
				if !out.publish(ctx, e) {
					out.finish(AssistantMessage{}, ctx.Err())
					return
				}
			case EventTextDelta:
				sawTextDelta = true
				if !publishItems(normalizer.feed(e.Delta)) {
					out.finish(AssistantMessage{}, ctx.Err())
					return
				}
			case EventThinkingDelta:
				// A pseudo envelope cannot span a non-text block. Flush any
				// ambiguous text before the block so ordering is preserved.
				if !publishItems(normalizer.flush(DiagnosticMalformedPseudoToolCall)) {
					out.finish(AssistantMessage{}, ctx.Err())
					return
				}
				if !out.publish(ctx, e) {
					out.finish(AssistantMessage{}, ctx.Err())
					return
				}
				if sawText {
					textBreak = true
				}
			case EventToolCallStart, EventToolCallDelta, EventToolCallEnd:
				// A native call is already canonical. Interleaving ends any
				// pending text candidate; it is emitted before the call.
				if !publishItems(normalizer.flush(DiagnosticMalformedPseudoToolCall)) {
					out.finish(AssistantMessage{}, ctx.Err())
					return
				}
				if !out.publish(ctx, e) {
					out.finish(AssistantMessage{}, ctx.Err())
					return
				}
				if sawText {
					textBreak = true
				}
			case EventDone:
				completionDiagnostics()
				msg := normalizeAssistantMessage(e.Message, completionOpts)
				final, haveFinal = msg, true
				if !out.publish(ctx, EventDone{Message: msg}) {
					out.finish(AssistantMessage{}, ctx.Err())
					return
				}
			case EventError:
				publishItems(normalizer.flush(DiagnosticIncompletePseudoToolCall))
				streamErr = e.Err
				if !out.publish(ctx, e) {
					out.finish(AssistantMessage{}, ctx.Err())
					return
				}
			default:
				if !out.publish(ctx, event) {
					out.finish(AssistantMessage{}, ctx.Err())
					return
				}
			}
		}

		if streamErr == nil && !haveFinal {
			// A transport that closes without EventDone still has an
			// authoritative assembled message. Preserve it rather than
			// reporting an empty turn.
			raw, err := stream.Result()
			if err != nil {
				streamErr = err
			} else {
				completionDiagnostics()
				final = normalizeAssistantMessage(raw, completionOpts)
				haveFinal = true
				if !out.publish(ctx, EventDone{Message: final}) {
					out.finish(AssistantMessage{}, ctx.Err())
					return
				}
			}
		}

		if streamErr != nil {
			raw, _ := stream.Result()
			if haveFinal {
				raw = final
			} else {
				completionDiagnostics()
				raw = normalizeAssistantMessage(raw, completionOpts)
			}
			out.finish(raw, streamErr)
			return
		}
		out.finish(final, nil)
	}()

	return out
}

// NormalizeMessage returns a copy of msg whose visible text has been run
// through the same pseudo-tool normalization as NormalizeStream.
//
// It exists for transports and callers that already hold an assembled message;
// the stream wrapper calls it for EventDone. Native tool calls pass through
// unchanged, and mixed native/pseudo content keeps its original order.
func NormalizeMessage(msg AssistantMessage, opts NormalizeOptions) AssistantMessage {
	return normalizeAssistantMessage(msg, opts)
}

// normalizeAssistantMessage rewrites the visible text blocks of one message.
func normalizeAssistantMessage(msg AssistantMessage, opts NormalizeOptions) AssistantMessage {
	normalizer := newTextNormalizer(opts)
	out := make([]ContentBlock, 0, len(msg.Content))

	appendItems := func(items []parsedItem) {
		for _, item := range items {
			if item.isCall {
				out = append(out, item.call)
				continue
			}
			if item.text != "" {
				out = appendTextBlock(out, item.text)
			}
		}
	}

	for _, block := range msg.Content {
		switch b := block.(type) {
		case TextContent:
			// Adjacent text blocks may split one logical run. Keep the parser
			// state across them so a marker split across block boundaries is
			// still recognized.
			appendItems(normalizer.feed(b.Text))
		case ToolCall:
			appendItems(normalizer.flush(DiagnosticMalformedPseudoToolCall))
			out = append(out, b)
		case ThinkingContent:
			appendItems(normalizer.flush(DiagnosticMalformedPseudoToolCall))
			out = append(out, b)
		default:
			appendItems(normalizer.flush(DiagnosticMalformedPseudoToolCall))
			out = append(out, block)
		}
	}
	appendItems(normalizer.flush(DiagnosticIncompletePseudoToolCall))

	msg.Content = out
	if hasToolCall(out) && (msg.StopReason == "" || msg.StopReason == StopEndTurn) {
		// A recognized pseudo call is a tool-use turn even when the transport
		// reported a plain stop: downstream consumers must see the canonical
		// reason, not the representation-specific one.
		msg.StopReason = StopToolUse
	}
	return msg
}

// appendTextBlock merges adjacent text so normalization never inserts the
// newline AssistantMessage.Text uses between blocks.
func appendTextBlock(blocks []ContentBlock, text string) []ContentBlock {
	if text == "" {
		return blocks
	}
	if len(blocks) > 0 {
		if last, ok := blocks[len(blocks)-1].(TextContent); ok {
			last.Text += text
			blocks[len(blocks)-1] = last
			return blocks
		}
	}
	return append(blocks, TextContent{Text: text})
}

// hasToolCall reports whether a content sequence contains any tool call.
func hasToolCall(blocks []ContentBlock) bool {
	for _, block := range blocks {
		if _, ok := block.(ToolCall); ok {
			return true
		}
	}
	return false
}

// parsedItem is one atom the incremental parser can emit.
type parsedItem struct {
	text   string
	call   ToolCall
	isCall bool
}

// pseudoProtocol describes one recognized serialized tool-call envelope.
type pseudoProtocol struct {
	// name identifies the protocol in diagnostics.
	name string
	// open and close delimit the envelope. The parser retains a suffix that
	// could be a prefix of either marker, so fragmentation at any boundary is
	// recognized.
	open  []byte
	close []byte
	// parse turns the inner body into canonical calls. A non-nil error means
	// the envelope is not trusted and must remain visible text.
	parse func(body string, coercer *argumentCoercer) ([]ToolCall, error)
}

// defaultPseudoProtocols returns the built-in serialized protocols.
//
// The XML-invoke envelope is the first observed representation, not the shape
// of the fix. A new provider adds another descriptor here.
func defaultPseudoProtocols() []pseudoProtocol {
	return []pseudoProtocol{
		{
			name:  "xml_invoke",
			open:  []byte("<dotsfunctioncall>"),
			close: []byte("</dotsfunctioncall>"),
			parse: parseXMLInvokeEnvelope,
		},
		{
			name:  "tool_call",
			open:  []byte("<tool_call>"),
			close: []byte("</tool_call>"),
			parse: parseToolCallEnvelope,
		},
	}
}

// textNormalizer is one incremental, bounded protocol scanner.
//
// Invariant: bytes are either emitted as ordinary text or retained in pending
// (a possible marker prefix) or in an in-progress envelope. Nothing is
// discarded; failed recognition returns the retained bytes as text.
type textNormalizer struct {
	protocols   []pseudoProtocol
	coercer     *argumentCoercer
	diagnostic  func(NormalizationDiagnostic)
	maxEnvelope int
	firstBytes  [256]bool

	pending    []byte
	inEnvelope bool
	proto      pseudoProtocol
	body       []byte
	raw        []byte
	calls      int
}

func newTextNormalizer(opts NormalizeOptions) *textNormalizer {
	protocols := opts.protocols
	if protocols == nil {
		protocols = defaultPseudoProtocols()
	}
	maxEnvelope := opts.MaxEnvelopeBytes
	if maxEnvelope <= 0 {
		maxEnvelope = DefaultMaxPseudoEnvelopeBytes
	}

	n := &textNormalizer{
		protocols:   protocols,
		coercer:     newArgumentCoercer(opts.Tools),
		diagnostic:  opts.OnDiagnostic,
		maxEnvelope: maxEnvelope,
	}
	for _, protocol := range protocols {
		if len(protocol.open) > 0 {
			n.firstBytes[protocol.open[0]] = true
		}
	}
	return n
}

// feed consumes one streamed chunk and returns everything that is now
// unambiguous. An empty result means the chunk ended in a possible marker
// prefix; the parser holds it until the next chunk or flush.
func (n *textNormalizer) feed(chunk string) []parsedItem {
	if chunk == "" {
		return nil
	}
	// Fast path: ordinary text with no protocol first byte cannot start an
	// envelope. This keeps the common streaming path allocation-free and does
	// not buffer the answer behind the parser.
	if !n.inEnvelope && len(n.pending) == 0 && !containsFirstByte(chunk, &n.firstBytes) {
		return []parsedItem{{text: chunk}}
	}
	n.pending = append(n.pending, chunk...)
	return n.process()
}

// flush emits every retained byte as ordinary text and resets any in-progress
// envelope. kind, when non-empty, is reported if an envelope was interrupted.
func (n *textNormalizer) flush(kind NormalizationDiagnosticKind) []parsedItem {
	var out []parsedItem
	if !n.inEnvelope {
		if len(n.pending) > 0 {
			out = append(out, parsedItem{text: string(n.pending)})
			n.pending = n.pending[:0]
		}
		return out
	}

	raw := make([]byte, 0, len(n.raw)+len(n.pending))
	raw = append(raw, n.raw...)
	raw = append(raw, n.pending...)
	if len(raw) > 0 {
		out = append(out, parsedItem{text: string(raw)})
	}
	if kind != "" {
		n.report(kind, n.proto.name, "pseudo-tool envelope did not complete", 0)
	}
	n.pending = n.pending[:0]
	n.resetEnvelope()
	return out
}

// process advances the state machine until it needs more input.
func (n *textNormalizer) process() []parsedItem {
	var out []parsedItem
	for {
		if n.inEnvelope {
			if idx := bytes.Index(n.pending, n.proto.close); idx >= 0 {
				end := idx + len(n.proto.close)
				n.body = append(n.body, n.pending[:idx]...)
				n.raw = append(n.raw, n.pending[:end]...)
				n.pending = append(n.pending[:0], n.pending[end:]...)

				if len(n.raw) > n.maxEnvelope {
					out = appendTextItem(out, string(n.raw))
					n.report(DiagnosticOversizedPseudoToolCall, n.proto.name,
						fmt.Sprintf("envelope exceeded %d bytes", n.maxEnvelope), 0)
					n.resetEnvelope()
					continue
				}

				calls, err := n.proto.parse(string(n.body), n.coercer)
				switch {
				case err != nil:
					out = appendTextItem(out, string(n.raw))
					n.report(DiagnosticMalformedPseudoToolCall, n.proto.name, err.Error(), 0)
				case len(calls) == 0:
					out = appendTextItem(out, string(n.raw))
					n.report(DiagnosticMalformedPseudoToolCall, n.proto.name, "envelope contained no tool call", 0)
				default:
					for i := range calls {
						n.calls++
						calls[i].ID = pseudoCallID(n.proto.name, string(n.body), n.calls)
						out = append(out, parsedItem{call: calls[i], isCall: true})
					}
					n.report(DiagnosticPseudoToolCall, n.proto.name, "", len(calls))
				}
				n.resetEnvelope()
				continue
			}

			// Keep only a suffix that could begin the closing marker; the rest
			// is safely part of the payload.
			keep := longestSuffixPrefix(n.pending, n.proto.close)
			cut := len(n.pending) - keep
			if cut > 0 {
				n.body = append(n.body, n.pending[:cut]...)
				n.raw = append(n.raw, n.pending[:cut]...)
				n.pending = append(n.pending[:0], n.pending[cut:]...)
			}
			if len(n.raw) > n.maxEnvelope {
				raw := make([]byte, 0, len(n.raw)+len(n.pending))
				raw = append(raw, n.raw...)
				raw = append(raw, n.pending...)
				out = appendTextItem(out, string(raw))
				n.report(DiagnosticOversizedPseudoToolCall, n.proto.name,
					fmt.Sprintf("envelope exceeded %d bytes", n.maxEnvelope), 0)
				n.pending = n.pending[:0]
				n.resetEnvelope()
				continue
			}
			return out
		}

		idx, protocol := n.earliestOpen()
		if idx >= 0 {
			if idx > 0 {
				out = appendTextItem(out, string(n.pending[:idx]))
			}
			n.inEnvelope = true
			n.proto = *protocol
			n.raw = append(n.raw[:0], protocol.open...)
			n.body = n.body[:0]
			n.pending = append(n.pending[:0], n.pending[idx+len(protocol.open):]...)
			continue
		}

		// Emit everything except a suffix that could still become an opening
		// marker in the next chunk.
		keep := n.retainedSuffix()
		cut := len(n.pending) - keep
		if cut > 0 {
			out = appendTextItem(out, string(n.pending[:cut]))
			n.pending = append(n.pending[:0], n.pending[cut:]...)
		}
		return out
	}
}

func (n *textNormalizer) earliestOpen() (int, *pseudoProtocol) {
	best := -1
	var match *pseudoProtocol
	for i := range n.protocols {
		protocol := &n.protocols[i]
		idx := bytes.Index(n.pending, protocol.open)
		if idx < 0 {
			continue
		}
		if best < 0 || idx < best || (idx == best && len(protocol.open) > len(match.open)) {
			best = idx
			match = protocol
		}
	}
	return best, match
}

func (n *textNormalizer) retainedSuffix() int {
	keep := 0
	for _, protocol := range n.protocols {
		if length := longestSuffixPrefix(n.pending, protocol.open); length > keep {
			keep = length
		}
	}
	return keep
}

func (n *textNormalizer) resetEnvelope() {
	n.inEnvelope = false
	n.proto = pseudoProtocol{}
	n.body = n.body[:0]
	n.raw = n.raw[:0]
}

func (n *textNormalizer) report(kind NormalizationDiagnosticKind, protocol, detail string, calls int) {
	if n.diagnostic == nil {
		return
	}
	n.diagnostic(NormalizationDiagnostic{
		Kind:      kind,
		Protocol:  protocol,
		Detail:    detail,
		ToolCalls: calls,
	})
}

func appendTextItem(out []parsedItem, text string) []parsedItem {
	if text == "" {
		return out
	}
	return append(out, parsedItem{text: text})
}

func containsFirstByte(text string, firstBytes *[256]bool) bool {
	for i := 0; i < len(text); i++ {
		if firstBytes[text[i]] {
			return true
		}
	}
	return false
}

// longestSuffixPrefix returns the length of the longest suffix of buf that is
// a prefix of marker. It only inspects at most len(marker)-1 bytes, so it is
// constant work per chunk.
func longestSuffixPrefix(buf, marker []byte) int {
	limit := len(marker) - 1
	if limit > len(buf) {
		limit = len(buf)
	}
	for length := limit; length > 0; length-- {
		if bytes.HasPrefix(marker, buf[len(buf)-length:]) {
			return length
		}
	}
	return 0
}

// parseXMLInvokeEnvelope parses the invoke/parameter grammar shared by the
// XML-style serialized protocols:
//
//	<invoke name="ls">
//	  <parameter name="path">internal</parameter>
//	</invoke>
//
// The opening/closing envelope markers are stripped by the scanner. Any XML
// error, missing name, or empty body rejects the whole envelope: the caller
// then shows the raw bytes as text rather than guessing at an action.
func parseXMLInvokeEnvelope(body string, coercer *argumentCoercer) ([]ToolCall, error) {
	type parameter struct {
		Name  string `xml:"name,attr"`
		Value string `xml:",chardata"`
	}
	type invoke struct {
		Name       string      `xml:"name,attr"`
		Parameters []parameter `xml:"parameter"`
	}
	var envelope struct {
		Invokes []invoke `xml:"invoke"`
	}
	if err := xml.Unmarshal([]byte("<envelope>"+body+"</envelope>"), &envelope); err != nil {
		return nil, fmt.Errorf("decode invoke envelope: %w", err)
	}
	if len(envelope.Invokes) == 0 {
		return nil, errors.New("envelope contains no invoke")
	}

	calls := make([]ToolCall, 0, len(envelope.Invokes))
	for _, inv := range envelope.Invokes {
		name := strings.TrimSpace(inv.Name)
		if name == "" {
			return nil, errors.New("invoke has no name")
		}
		args := make(map[string]any, len(inv.Parameters))
		for _, param := range inv.Parameters {
			paramName := strings.TrimSpace(param.Name)
			if paramName == "" {
				return nil, errors.New("parameter has no name")
			}
			args[paramName] = coercer.coerce(name, paramName, strings.TrimSpace(param.Value))
		}
		calls = append(calls, ToolCall{Name: name, Arguments: args})
	}
	return calls, nil
}

// parseToolCallEnvelope accepts the two payload shapes commonly carried by a
// generic <tool_call> marker: a JSON function object (one or an array), and
// the XML invoke grammar. Anything else is rejected and stays visible text.
func parseToolCallEnvelope(body string, coercer *argumentCoercer) ([]ToolCall, error) {
	trimmed := strings.TrimSpace(body)
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		return parseJSONToolCallEnvelope(trimmed)
	}
	return parseXMLInvokeEnvelope(body, coercer)
}

// parseJSONToolCallEnvelope decodes the common serialized function-call JSON.
//
// Accepted shapes share one contract: each entry names a tool and may carry an
// object of arguments. The argument object may itself be JSON-encoded as a
// string, which some gateways emit. A payload without a name is rejected.
func parseJSONToolCallEnvelope(body string) ([]ToolCall, error) {
	type wireCall struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
		Function  *struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"function"`
	}

	var entries []wireCall
	if strings.HasPrefix(body, "[") {
		if err := json.Unmarshal([]byte(body), &entries); err != nil {
			return nil, fmt.Errorf("decode tool-call array: %w", err)
		}
	} else {
		var entry wireCall
		if err := json.Unmarshal([]byte(body), &entry); err != nil {
			return nil, fmt.Errorf("decode tool-call object: %w", err)
		}
		entries = []wireCall{entry}
	}
	if len(entries) == 0 {
		return nil, errors.New("tool-call payload is empty")
	}

	calls := make([]ToolCall, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name
		rawArguments := entry.Arguments
		if entry.Function != nil {
			if name == "" {
				name = entry.Function.Name
			}
			if len(rawArguments) == 0 {
				rawArguments = entry.Function.Arguments
			}
		}
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, errors.New("tool call has no name")
		}
		args, err := decodeSerializedArguments(rawArguments)
		if err != nil {
			return nil, err
		}
		calls = append(calls, ToolCall{Name: name, Arguments: args})
	}
	return calls, nil
}

func decodeSerializedArguments(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return map[string]any{}, nil
	}
	if raw[0] == '"' {
		var encoded string
		if err := json.Unmarshal(raw, &encoded); err != nil {
			return nil, fmt.Errorf("decode encoded arguments: %w", err)
		}
		raw = []byte(encoded)
	}
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("decode tool arguments: %w", err)
	}
	if args == nil {
		args = map[string]any{}
	}
	return args, nil
}

// argumentCoercer converts textual XML parameters to the JSON types declared
// by the offered tool schemas. Without this, a number or boolean argument
// would always arrive as a string and fail schema validation even though the
// serialized call expressed the right value.
type argumentCoercer struct {
	types map[string]map[string]string
}

func newArgumentCoercer(specs []ToolSpec) *argumentCoercer {
	if len(specs) == 0 {
		return nil
	}
	coercer := &argumentCoercer{types: make(map[string]map[string]string, len(specs))}
	for _, spec := range specs {
		if len(spec.Parameters) == 0 {
			continue
		}
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(spec.Parameters, &schema); err != nil || len(schema.Properties) == 0 {
			continue
		}
		properties := make(map[string]string, len(schema.Properties))
		for name, raw := range schema.Properties {
			var property struct {
				Type json.RawMessage `json:"type"`
			}
			if err := json.Unmarshal(raw, &property); err != nil {
				continue
			}
			var typ string
			// A union type (["string","null"]) has no single coercion; leave
			// the value as text and let the schema validator decide.
			if err := json.Unmarshal(property.Type, &typ); err != nil {
				continue
			}
			properties[name] = typ
		}
		if len(properties) > 0 {
			coercer.types[spec.Name] = properties
		}
	}
	if len(coercer.types) == 0 {
		return nil
	}
	return coercer
}

func (c *argumentCoercer) coerce(tool, parameter, raw string) any {
	if c == nil {
		return raw
	}
	properties, ok := c.types[tool]
	if !ok {
		return raw
	}
	switch properties[parameter] {
	case "string":
		return raw
	case "integer", "number":
		if value, err := strconv.ParseFloat(raw, 64); err == nil {
			return value
		}
		return raw
	case "boolean":
		if value, err := strconv.ParseBool(raw); err == nil {
			return value
		}
		return raw
	case "array", "object":
		var value any
		if err := json.Unmarshal([]byte(raw), &value); err == nil {
			return value
		}
		return raw
	default:
		return raw
	}
}

// pseudoCallID derives a stable id from the protocol and envelope body.
//
// Stable ids keep replay deterministic and make fragmentation tests meaningful:
// the same logical call gets the same id however the stream was chunked.
func pseudoCallID(protocol, body string, ordinal int) string {
	sum := sha256.Sum256([]byte(protocol + "\x00" + body))
	return fmt.Sprintf("pseudo_%x_%d", sum[:8], ordinal)
}
