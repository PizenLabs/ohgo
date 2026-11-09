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
			residue   residueDetector
		)
		publishText := func(text string) bool {
			if text == "" {
				return true
			}
			if sanitized, changed := residue.sanitize(text); changed {
				// An unregistered spelling reached the live path. Replace only
				// the protocol marker here, at the provider boundary, so the
				// renderer never sees protocol bytes and surrounding prose is
				// preserved.
				text = sanitized
				normalizer.residue = true
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
				// A stream can complete while the live scanner still holds an
				// ambiguous prefix or an incomplete envelope. Flush it now so
				// the live transcript tells the same story as the authoritative
				// final message instead of silently dropping protocol bytes.
				if !publishItems(normalizer.flush(DiagnosticIncompletePseudoToolCall)) {
					out.finish(AssistantMessage{}, ctx.Err())
					return
				}
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
				if !publishItems(normalizer.flush(DiagnosticIncompletePseudoToolCall)) {
					out.finish(AssistantMessage{}, ctx.Err())
					return
				}
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
	msg.ProtocolResidue = normalizer.residue
	// A spelling the registry does not know still belongs to the protocol
	// family when it names a dots/tool_call envelope. It reached this point as
	// text because there was no descriptor to parse it; replace it with the
	// same truthful diagnostic rather than persist it as an answer.
	//
	// Quoted examples (fenced code, inline code, backslash escapes) are
	// assistant content, not residue. The detector carries quote state across
	// text blocks so a fence split across blocks is classified once.
	var detector residueDetector
	for i, block := range msg.Content {
		text, ok := block.(TextContent)
		if !ok {
			continue
		}
		sanitized, changed := detector.sanitize(text.Text)
		if !changed {
			continue
		}
		msg.Content[i] = TextContent{Text: sanitized}
		msg.ProtocolResidue = true
	}
	if hasToolCall(out) && (msg.StopReason == "" || msg.StopReason == StopEndTurn) {
		// A recognized pseudo call is a tool-use turn even when the transport
		// reported a plain stop: downstream consumers must see the canonical
		// reason, not the representation-specific one.
		msg.StopReason = StopToolUse
	}
	return msg
}

// protocolFailureText is the user-visible record of a recognized envelope that
// could not be normalized.
//
// It names the failure without reproducing the provider's serialization. The
// raw bytes are still observed through the diagnostic sink for debugging, but
// they are not assistant prose and must not be persisted as such.
func protocolFailureText(kind NormalizationDiagnosticKind, protocol string) string {
	label := string(kind)
	if label == "" {
		label = string(DiagnosticMalformedPseudoToolCall)
	}
	if protocol == "" {
		protocol = "serialized-tool-call"
	}
	return fmt.Sprintf("[provider tool-call syntax could not be normalized: %s in %s; the call was not executed]", label, protocol)
}

// residueDetector classifies protocol-looking text across streamed fragments.
//
// The normalizer already distinguishes quoted content from live protocol while
// scanning. This detector is the defence-in-depth check applied to emitted
// text, so it must carry the same quote state across deltas; otherwise a code
// fence opened in one delta and closed in another would be mistaken for an
// unquoted envelope.
type residueDetector struct {
	quotes quoteState
}

// sanitize replaces unquoted protocol markers with one bounded diagnostic
// while preserving ordinary prose around them.
func (d *residueDetector) sanitize(text string) (string, bool) {
	mask, next := d.quotes.scan([]byte(text))
	d.quotes = next
	lower := strings.ToLower(text)
	var b strings.Builder
	b.Grow(len(text))
	changed := false
	for i := 0; i < len(text); {
		if mask[i] || text[i] != '<' {
			b.WriteByte(text[i])
			i++
			continue
		}
		limit := i + maxOpenTagBytes
		if limit > len(lower) {
			limit = len(lower)
		}
		rel := strings.IndexByte(lower[i:limit], '>')
		if rel < 0 {
			tag := lower[i:]
			if isDotsCallTagPrefix(tag) ||
				strings.HasPrefix(tag, "<tool_call") ||
				strings.HasPrefix(tag, "</tool_call") {
				b.WriteString(protocolFailureText(DiagnosticMalformedPseudoToolCall, "serialized-tool-call"))
				changed = true
				break
			}
			b.WriteByte(text[i])
			i++
			continue
		}
		end := i + rel + 1
		tag := lower[i:end]
		if isDotsCallOpenTag(tag) || isDotsCallCloseTag(tag) ||
			tag == "<tool_call>" || tag == "</tool_call>" {
			b.WriteString(protocolFailureText(DiagnosticMalformedPseudoToolCall, "serialized-tool-call"))
			changed = true
			i = end
			continue
		}
		b.WriteString(text[i:end])
		i = end
	}
	if !changed {
		return text, false
	}
	return b.String(), true
}

// isDotsCallOpenTag reports whether tag is an opening tag in the dots-call
// family, independent of the exact separator spelling.
func isDotsCallOpenTag(tag string) bool {
	tag = strings.ToLower(strings.TrimSpace(tag))
	if !strings.HasPrefix(tag, "<") || strings.HasPrefix(tag, "</") {
		return false
	}
	name := strings.TrimSuffix(strings.TrimPrefix(tag, "<"), ">")
	return strings.HasPrefix(name, "dots") && strings.Contains(name, "call")
}

// isDotsCallCloseTag reports whether tag is a closing tag in the family.
func isDotsCallCloseTag(tag string) bool {
	tag = strings.ToLower(strings.TrimSpace(tag))
	if !strings.HasPrefix(tag, "</") {
		return false
	}
	name := strings.TrimSuffix(strings.TrimPrefix(tag, "</"), ">")
	return strings.HasPrefix(name, "dots") && strings.Contains(name, "call")
}

// isDotsCallTagPrefix reports whether a partial tag could become a dots-call
// opening or closing tag. It is used only by the residue fallback, where the
// normalizer has already had the chance to consume a complete tag.
func isDotsCallTagPrefix(tag string) bool {
	if !strings.HasPrefix(tag, "<") {
		return false
	}
	name := strings.TrimPrefix(tag, "<")
	name = strings.TrimPrefix(name, "/")
	if !strings.HasPrefix(name, "dots") {
		return false
	}
	return strings.Contains(name, "call")
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

// maxOpenTagBytes bounds a family opening tag that has not terminated yet.
//
// A model that starts a dots-family tag and never closes it must not make the
// normalizer buffer unbounded text. Once a possible open tag exceeds this
// bound its bytes are consumed as protocol residue and diagnosed.
const maxOpenTagBytes = 256

// pseudoProtocol describes one recognized serialized tool-call envelope.
type pseudoProtocol struct {
	// name identifies the protocol in diagnostics.
	name string
	// open and close delimit the envelope. The parser retains a suffix that
	// could be a prefix of either marker, so fragmentation at any boundary is
	// recognized.
	open  []byte
	close []byte
	// extraCloses are alternative closing markers for the same opening. The
	// provider has emitted `</dotsfunctioncall>` and `</dots_function_call>`
	// for one envelope shape; requiring an exact pair would leak the envelope
	// as prose on the other spelling.
	extraCloses [][]byte
	// familyClose reports that the close side is any `</dots...call>` tag
	// rather than one exact spelling. Unknown dots-family openings use it: the
	// family is recognizable even when this build has never seen the exact
	// separator spelling, and the body can still be consumed instead of being
	// published as assistant prose.
	familyClose bool
	// parse turns the inner body into canonical calls. A non-nil error means
	// the envelope is not trusted and must not become assistant prose.
	parse func(body string, coercer *argumentCoercer) ([]ToolCall, error)
}

// unknownFamilyProtocol describes an opening tag in the dots-call family that
// is not one of the registered spellings.
//
// It is not parsed: no action may be fabricated from a representation the
// runtime does not understand. The descriptor exists so the bounded scanner
// can consume the envelope through its family closing tag and report one
// truthful diagnostic instead of leaking raw protocol into the live transcript
// or persisted answer.
func unknownFamilyProtocol(open []byte) pseudoProtocol {
	opened := make([]byte, len(open))
	copy(opened, open)
	return pseudoProtocol{
		name:        "dots_call_family",
		open:        opened,
		familyClose: true,
		parse: func(string, *argumentCoercer) ([]ToolCall, error) {
			return nil, errors.New("unrecognized dots-family tool-call spelling")
		},
	}
}

// closingMarkers returns every accepted close for the protocol.
func (p pseudoProtocol) closingMarkers() [][]byte {
	if len(p.extraCloses) == 0 {
		return [][]byte{p.close}
	}
	out := make([][]byte, 0, 1+len(p.extraCloses))
	out = append(out, p.close)
	return append(out, p.extraCloses...)
}

// defaultPseudoProtocols returns the built-in serialized protocols.
//
// The XML-invoke envelope is the first observed representation, not the shape
// of the fix. A new provider adds another descriptor here.
func defaultPseudoProtocols() []pseudoProtocol {
	closeStandard := []byte("</dotsfunctioncall>")
	closeUnderscored := []byte("</dots_function_call>")
	return []pseudoProtocol{
		{
			name:        "xml_invoke",
			open:        []byte("<dotsfunctioncall>"),
			close:       closeStandard,
			extraCloses: [][]byte{closeUnderscored},
			parse:       parseXMLInvokeEnvelope,
		},
		{
			name:        "xml_invoke_underscored",
			open:        []byte("<dots_function_call>"),
			close:       closeUnderscored,
			extraCloses: [][]byte{closeStandard},
			parse:       parseXMLInvokeEnvelope,
		},
		{
			name:        "xml_invoke_functioncall",
			open:        []byte("<dots_functioncall>"),
			close:       closeUnderscored,
			extraCloses: [][]byte{closeStandard},
			parse:       parseXMLInvokeEnvelope,
		},
		{
			name:        "xml_invoke_function_call",
			open:        []byte("<dotsfunction_call>"),
			close:       closeUnderscored,
			extraCloses: [][]byte{closeStandard},
			parse:       parseXMLInvokeEnvelope,
		},
		{
			name:  "tool_call",
			open:  []byte("<tool_call>"),
			close: []byte("</tool_call>"),
			parse: parseToolCallEnvelope,
		},
		{
			// LongCat-Flash-Chat wraps a JSON function object; LongCat 2.5
			// emits a bare tool name followed by key/value elements. Both
			// spellings use this envelope.
			name:  "longcat_tool_call",
			open:  []byte("<longcat_tool_call>"),
			close: []byte("</longcat_tool_call>"),
			parse: parseLongCatEnvelope,
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
	closes      [][]byte
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
	// residue records that a recognized envelope could not be normalized.
	residue bool
	// discarding means an oversized envelope is being consumed without
	// retaining or publishing its body. The close marker is still recognized
	// so ordinary text after the envelope can resume.
	discarding bool
	// quoted tracks markdown code/escape context across chunks so a protocol
	// example inside documentation stays content.
	quoted quoteState
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
		n.closes = append(n.closes, protocol.closingMarkers()...)
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
	// not buffer the answer behind the parser. Quote state still advances so a
	// fence opened before a later protocol-looking delta is honored.
	if !n.inEnvelope && len(n.pending) == 0 && !containsFirstByte(chunk, &n.firstBytes) {
		n.advanceQuotes(chunk)
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
			text := string(n.pending)
			n.advanceQuotes(text)
			out = append(out, parsedItem{text: text})
			n.pending = n.pending[:0]
		}
		return out
	}

	if n.discarding {
		// The oversized-envelope diagnostic was already published; the
		// remaining bytes are discarded protocol, not prose.
		n.pending = n.pending[:0]
		n.resetEnvelope()
		return out
	}

	raw := make([]byte, 0, len(n.raw)+len(n.pending))
	raw = append(raw, n.raw...)
	raw = append(raw, n.pending...)
	if len(raw) > 0 {
		// The bytes are recognized protocol, not prose. Keep the fact that
		// normalization failed, but never publish the serialization as an
		// ordinary assistant sentence or persist it into history.
		out = append(out, parsedItem{text: protocolFailureText(kind, n.proto.name)})
		n.residue = true
	}
	if kind != "" {
		n.report(kind, n.proto.name, "pseudo-tool envelope did not complete", 0)
	}
	n.pending = n.pending[:0]
	n.resetEnvelope()
	return out
}

// advanceQuotes updates the markdown quote state for text that is about to be
// published as ordinary content. Protocol bytes are never passed here.
func (n *textNormalizer) advanceQuotes(text string) {
	if text == "" {
		return
	}
	_, n.quoted = n.quoted.scan([]byte(text))
}

// emitText publishes a prefix of pending as ordinary text and advances the
// quote state by exactly the bytes emitted. The retained suffix is re-scanned
// with the updated state on the next iteration.
func (n *textNormalizer) emitText(out []parsedItem, text string) []parsedItem {
	if text == "" {
		return out
	}
	n.advanceQuotes(text)
	return append(out, parsedItem{text: text})
}

// familyTagState is how much of a possible dots-family opening tag is present.
type familyTagState int

const (
	familyTagNone familyTagState = iota
	familyTagPartial
	familyTagComplete
)

// process advances the state machine until it needs more input.
func (n *textNormalizer) process() []parsedItem {
	var out []parsedItem
	for {
		if n.inEnvelope {
			if idx, closeLen := n.findClose(n.pending); idx >= 0 {
				end := idx + closeLen
				if !n.discarding {
					n.body = append(n.body, n.pending[:idx]...)
					n.raw = append(n.raw, n.pending[:end]...)
				}
				n.pending = append(n.pending[:0], n.pending[end:]...)

				if n.discarding {
					n.resetEnvelope()
					continue
				}
				if len(n.raw) > n.maxEnvelope {
					out = appendTextItem(out, protocolFailureText(DiagnosticOversizedPseudoToolCall, n.proto.name))
					n.residue = true
					n.report(DiagnosticOversizedPseudoToolCall, n.proto.name,
						fmt.Sprintf("envelope exceeded %d bytes", n.maxEnvelope), 0)
					n.resetEnvelope()
					continue
				}

				calls, err := n.proto.parse(string(n.body), n.coercer)
				switch {
				case err != nil:
					out = appendTextItem(out, protocolFailureText(DiagnosticMalformedPseudoToolCall, n.proto.name))
					n.residue = true
					n.report(DiagnosticMalformedPseudoToolCall, n.proto.name, err.Error(), 0)
				case len(calls) == 0:
					out = appendTextItem(out, protocolFailureText(DiagnosticMalformedPseudoToolCall, n.proto.name))
					n.residue = true
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

			// Keep only a suffix that could begin a closing marker; the rest
			// is safely part of the payload.
			keep := n.retainedCloseSuffix(n.pending)
			cut := len(n.pending) - keep
			if cut > 0 && !n.discarding {
				n.body = append(n.body, n.pending[:cut]...)
				n.raw = append(n.raw, n.pending[:cut]...)
			}
			if n.discarding || len(n.raw) > n.maxEnvelope {
				if !n.discarding {
					out = appendTextItem(out, protocolFailureText(DiagnosticOversizedPseudoToolCall, n.proto.name))
					n.residue = true
					n.report(DiagnosticOversizedPseudoToolCall, n.proto.name,
						fmt.Sprintf("envelope exceeded %d bytes", n.maxEnvelope), 0)
					n.discarding = true
					n.body = n.body[:0]
					n.raw = n.raw[:0]
				}
				// Discard everything except a possible close prefix. The
				// envelope body is not prose and must never be published just
				// because the envelope exceeded the retained bound.
				n.pending = append(n.pending[:0], n.pending[cut:]...)
				return out
			}
			if cut > 0 {
				n.pending = append(n.pending[:0], n.pending[cut:]...)
			}
			return out
		}

		mask, _ := n.quoted.scan(n.pending)
		idx, openLen, protocol, partial := n.earliestOpen(mask)
		// A closing marker that appears before any opening marker has no
		// envelope to close. It is still protocol, not prose; drop it and
		// record that the message is not a clean answer.
		if closeIdx, length := n.earliestStrayClose(mask); closeIdx >= 0 && (idx < 0 || closeIdx < idx) {
			out = n.emitText(out, string(n.pending[:closeIdx]))
			n.residue = true
			n.pending = append(n.pending[:0], n.pending[closeIdx+length:]...)
			continue
		}

		if idx >= 0 && partial {
			// The bytes before the possible tag are unambiguous text. Hold the
			// tag until its terminator (or the bounded fallback) resolves it.
			out = n.emitText(out, string(n.pending[:idx]))
			n.pending = append(n.pending[:0], n.pending[idx:]...)
			if bytes.HasPrefix(bytes.ToLower(n.pending), []byte("<dots")) && len(n.pending) >= maxOpenTagBytes {
				// A family tag that has not terminated within the bound is
				// protocol residue, not unbounded prose. Consume its bytes and
				// look for the family close with the normal envelope caps.
				open := append([]byte(nil), n.pending...)
				n.enterEnvelope(unknownFamilyProtocol(open), open)
				n.pending = n.pending[:0]
				continue
			}
			return out
		}

		if idx >= 0 {
			out = n.emitText(out, string(n.pending[:idx]))
			open := n.pending[idx : idx+openLen]
			n.enterEnvelope(protocol, open)
			n.pending = append(n.pending[:0], n.pending[idx+openLen:]...)
			continue
		}

		// Emit everything except a suffix that could still become an opening
		// marker in the next chunk.
		keep := n.retainedSuffix(mask)
		cut := len(n.pending) - keep
		if cut > 0 {
			out = n.emitText(out, string(n.pending[:cut]))
			n.pending = append(n.pending[:0], n.pending[cut:]...)
		}
		return out
	}
}

// enterEnvelope starts a bounded envelope at the current pending offset.
func (n *textNormalizer) enterEnvelope(protocol pseudoProtocol, open []byte) {
	n.inEnvelope = true
	n.proto = protocol
	n.raw = append(n.raw[:0], open...)
	n.body = n.body[:0]
}

// earliestOpen finds the earliest live opening marker. It returns partial=true
// when the only match is a possible dots-family tag that has not terminated
// yet; the caller holds it rather than publishing it.
func (n *textNormalizer) earliestOpen(mask []bool) (idx, openLen int, protocol pseudoProtocol, partial bool) {
	best := -1
	bestLen := 0
	var match pseudoProtocol
	for i := range n.protocols {
		candidate := &n.protocols[i]
		at := indexLive(n.pending, mask, candidate.open)
		if at < 0 {
			continue
		}
		if best < 0 || at < best || (at == best && len(candidate.open) > bestLen) {
			best = at
			bestLen = len(candidate.open)
			match = *candidate
		}
	}

	familyIdx, state, familyEnd := n.earliestFamilyOpen(mask)
	switch state {
	case familyTagPartial:
		if best < 0 || familyIdx < best {
			return familyIdx, 0, pseudoProtocol{}, true
		}
	case familyTagComplete:
		if best >= 0 && best <= familyIdx {
			return best, bestLen, match, false
		}
		tag := n.pending[familyIdx:familyEnd]
		return familyIdx, len(tag), unknownFamilyProtocol(tag), false
	}
	if best >= 0 {
		return best, bestLen, match, false
	}
	return -1, 0, pseudoProtocol{}, false
}

// earliestFamilyOpen finds the earliest live dots-family opening tag even when
// its separator spelling is not registered. A complete tag must contain
// "call"; a tag shorter than the bound but without its terminator is partial
// and must be retained.
func (n *textNormalizer) earliestFamilyOpen(mask []bool) (int, familyTagState, int) {
	const prefix = "<dots"
	for i := 0; i < len(n.pending); i++ {
		if mask[i] || n.pending[i] != '<' {
			continue
		}
		if i+1 < len(n.pending) && n.pending[i+1] == '/' {
			continue
		}
		rest := bytes.ToLower(n.pending[i:])
		if len(rest) < len(prefix) {
			if bytes.HasPrefix([]byte(prefix), rest) {
				return i, familyTagPartial, -1
			}
			continue
		}
		if !bytes.HasPrefix(rest, []byte(prefix)) {
			continue
		}
		limit := i + maxOpenTagBytes
		if limit > len(n.pending) {
			limit = len(n.pending)
		}
		rel := bytes.IndexByte(n.pending[i:limit], '>')
		if rel < 0 {
			return i, familyTagPartial, -1
		}
		end := i + rel + 1
		if isDotsCallOpenTag(string(n.pending[i:end])) {
			return i, familyTagComplete, end
		}
		i = end - 1
	}
	return -1, familyTagNone, -1
}

// earliestStrayClose finds a live closing marker with no envelope to close.
func (n *textNormalizer) earliestStrayClose(mask []bool) (int, int) {
	best, bestLen := indexAnyLive(n.pending, mask, n.closes)
	if idx, length := indexFamilyCloseLive(n.pending, mask); idx >= 0 && (best < 0 || idx < best) {
		return idx, length
	}
	return best, bestLen
}

// retainedSuffix returns the bytes that might begin an opening or closing
// marker, never including quoted text.
func (n *textNormalizer) retainedSuffix(mask []bool) int {
	keep := 0
	for _, protocol := range n.protocols {
		if length := longestSuffixPrefixLive(n.pending, mask, protocol.open); length > keep {
			keep = length
		}
	}
	if length := longestSuffixPrefixLive(bytes.ToLower(n.pending), mask, []byte("<dots")); length > keep {
		keep = length
	}
	if length := longestSuffixPrefixAnyLive(n.pending, mask, n.closes); length > keep {
		keep = length
	}
	if length := familyCloseLiveSuffix(n.pending, mask); length > keep {
		keep = length
	}
	return keep
}

// findClose returns the next closing marker for the active envelope.
func (n *textNormalizer) findClose(buf []byte) (int, int) {
	if n.proto.familyClose {
		return indexFamilyClose(buf)
	}
	return indexAny(buf, n.proto.closingMarkers())
}

// retainedCloseSuffix holds only a suffix that could begin the active close.
func (n *textNormalizer) retainedCloseSuffix(buf []byte) int {
	if n.proto.familyClose {
		return familyCloseSuffix(buf)
	}
	return longestSuffixPrefixAny(buf, n.proto.closingMarkers())
}

// indexAny returns the earliest occurrence of any marker and the matched
// marker's length. When several start at the same index the longest wins, so a
// shared prefix cannot end the envelope early.
func indexAny(buf []byte, markers [][]byte) (int, int) {
	best := -1
	bestLen := 0
	for _, marker := range markers {
		if len(marker) == 0 {
			continue
		}
		idx := bytes.Index(buf, marker)
		if idx < 0 {
			continue
		}
		if best < 0 || idx < best || (idx == best && len(marker) > bestLen) {
			best = idx
			bestLen = len(marker)
		}
	}
	return best, bestLen
}

// longestSuffixPrefixAny is longestSuffixPrefix across several markers.
func longestSuffixPrefixAny(buf []byte, markers [][]byte) int {
	keep := 0
	for _, marker := range markers {
		if length := longestSuffixPrefix(buf, marker); length > keep {
			keep = length
		}
	}
	return keep
}

// indexLive finds marker in buf only when every byte of the match is outside a
// quoted range.
func indexLive(buf []byte, mask []bool, marker []byte) int {
	if len(marker) == 0 {
		return -1
	}
	for i := 0; i+len(marker) <= len(buf); i++ {
		if !bytes.HasPrefix(buf[i:], marker) {
			continue
		}
		live := true
		for j := i; j < i+len(marker); j++ {
			if mask[j] {
				live = false
				break
			}
		}
		if live {
			return i
		}
	}
	return -1
}

// indexAnyLive is indexAny restricted to live bytes.
func indexAnyLive(buf []byte, mask []bool, markers [][]byte) (int, int) {
	best := -1
	bestLen := 0
	for _, marker := range markers {
		if len(marker) == 0 {
			continue
		}
		idx := indexLive(buf, mask, marker)
		if idx < 0 {
			continue
		}
		if best < 0 || idx < best || (idx == best && len(marker) > bestLen) {
			best = idx
			bestLen = len(marker)
		}
	}
	return best, bestLen
}

// longestSuffixPrefixLive is longestSuffixPrefix restricted to live bytes.
func longestSuffixPrefixLive(buf []byte, mask []bool, marker []byte) int {
	limit := len(marker) - 1
	if limit > len(buf) {
		limit = len(buf)
	}
	for length := limit; length > 0; length-- {
		start := len(buf) - length
		live := true
		for i := start; i < len(buf); i++ {
			if mask[i] {
				live = false
				break
			}
		}
		if live && bytes.HasPrefix(marker, buf[start:]) {
			return length
		}
	}
	return 0
}

// longestSuffixPrefixAnyLive is longestSuffixPrefixLive across markers.
func longestSuffixPrefixAnyLive(buf []byte, mask []bool, markers [][]byte) int {
	keep := 0
	for _, marker := range markers {
		if length := longestSuffixPrefixLive(buf, mask, marker); length > keep {
			keep = length
		}
	}
	return keep
}

// indexFamilyClose finds any `</dots...call>` closing tag. It is used for an
// unknown family spelling whose exact close was never registered.
func indexFamilyClose(buf []byte) (int, int) {
	lower := bytes.ToLower(buf)
	offset := 0
	for offset < len(lower) {
		rel := bytes.Index(lower[offset:], []byte("</dots"))
		if rel < 0 {
			return -1, 0
		}
		start := offset + rel
		endRel := bytes.IndexByte(lower[start:], '>')
		if endRel < 0 {
			return -1, 0
		}
		end := start + endRel + 1
		if bytes.Contains(lower[start:end], []byte("call")) {
			return start, end - start
		}
		offset = start + 2
	}
	return -1, 0
}

// indexFamilyCloseLive is indexFamilyClose restricted to live bytes.
func indexFamilyCloseLive(buf []byte, mask []bool) (int, int) {
	lower := bytes.ToLower(buf)
	for i := 0; i < len(lower); i++ {
		if mask[i] || lower[i] != '<' {
			continue
		}
		if i+1 >= len(lower) || lower[i+1] != '/' {
			continue
		}
		if !bytes.HasPrefix(lower[i:], []byte("</dots")) {
			continue
		}
		limit := i + maxOpenTagBytes
		if limit > len(lower) {
			limit = len(lower)
		}
		rel := bytes.IndexByte(lower[i:limit], '>')
		if rel < 0 {
			continue
		}
		end := i + rel + 1
		if mask[end-1] {
			continue
		}
		if isDotsCallCloseTag(string(lower[i:end])) {
			return i, end - i
		}
	}
	return -1, 0
}

// familyCloseSuffix is the retained close prefix for an unknown family
// envelope.
func familyCloseSuffix(buf []byte) int {
	lower := bytes.ToLower(buf)
	for i := len(buf) - 1; i >= 0; i-- {
		if buf[i] != '<' {
			continue
		}
		tail := lower[i:]
		if len(tail) <= len("</dots") {
			if bytes.HasPrefix([]byte("</dots"), tail) {
				return len(tail)
			}
			return 0
		}
		if !bytes.HasPrefix(tail, []byte("</dots")) {
			return 0
		}
		if bytes.IndexByte(tail, '>') < 0 && len(tail) < maxOpenTagBytes {
			return len(tail)
		}
		return 0
	}
	return 0
}

// familyCloseLiveSuffix is familyCloseSuffix restricted to live bytes.
func familyCloseLiveSuffix(buf []byte, mask []bool) int {
	lower := bytes.ToLower(buf)
	for i := len(buf) - 1; i >= 0; i-- {
		if mask[i] || buf[i] != '<' {
			continue
		}
		tail := lower[i:]
		if len(tail) <= len("</dots") {
			if bytes.HasPrefix([]byte("</dots"), tail) {
				return len(tail)
			}
			return 0
		}
		if !bytes.HasPrefix(tail, []byte("</dots")) {
			return 0
		}
		if bytes.IndexByte(tail, '>') < 0 && len(tail) < maxOpenTagBytes {
			return len(tail)
		}
		return 0
	}
	return 0
}

// quoteState is the lexical markdown context used to distinguish quoted
// protocol examples from live provider output.
//
// It is deliberately not a renderer: it only answers "is this byte inside a
// fence, inline code, or a backslash escape?". That deterministic boundary
// lets documentation mention the protocol without turning into a completion
// failure, while raw provider bytes remain protocol residue.
type quoteState struct {
	started   bool
	fence     byte
	fenceLen  int
	inline    int
	escaped   bool
	lineStart bool
	indent    int
}

// scan returns a mask of quoted bytes and the resulting state.
func (q quoteState) scan(p []byte) ([]bool, quoteState) {
	mask := make([]bool, len(p))
	if len(p) == 0 {
		return mask, q
	}
	if !q.started {
		q.started = true
		q.lineStart = true
	}
	for i := 0; i < len(p); {
		c := p[i]
		if q.escaped {
			mask[i] = true
			q.escaped = false
			q.lineStart = c == '\n'
			q.indent = 0
			i++
			continue
		}
		if q.fence != 0 {
			mask[i] = true
			if q.lineStart {
				if c == ' ' && q.indent < 3 {
					q.indent++
					i++
					continue
				}
				if c == q.fence {
					j := i
					for j < len(p) && p[j] == q.fence {
						j++
					}
					for k := i; k < j; k++ {
						mask[k] = true
					}
					if j-i >= q.fenceLen {
						q.fence = 0
						q.fenceLen = 0
					}
					i = j
					q.lineStart = false
					q.indent = 0
					continue
				}
				q.lineStart = false
				q.indent = 0
			}
			if c == '\n' {
				q.lineStart = true
				q.indent = 0
			}
			i++
			continue
		}
		if q.inline > 0 {
			mask[i] = true
			if c == '`' {
				j := i
				for j < len(p) && p[j] == '`' {
					j++
				}
				for k := i; k < j; k++ {
					mask[k] = true
				}
				if j-i == q.inline {
					q.inline = 0
				}
				i = j
				q.lineStart = false
				q.indent = 0
				continue
			}
			if c == '\n' {
				q.lineStart = true
				q.indent = 0
			} else {
				q.lineStart = false
			}
			i++
			continue
		}
		switch c {
		case '\\':
			q.escaped = true
			q.lineStart = false
			q.indent = 0
			i++
		case '\n':
			q.lineStart = true
			q.indent = 0
			i++
		case ' ', '\t':
			if q.lineStart {
				if c == ' ' && q.indent < 3 {
					q.indent++
				}
				i++
				continue
			}
			q.lineStart = false
			q.indent = 0
			i++
		case '`', '~':
			j := i
			for j < len(p) && p[j] == c {
				j++
			}
			run := j - i
			if q.lineStart && q.indent <= 3 && run >= 3 {
				for k := i; k < j; k++ {
					mask[k] = true
				}
				q.fence = c
				q.fenceLen = run
				q.lineStart = false
				q.indent = 0
				i = j
				continue
			}
			if c == '`' {
				for k := i; k < j; k++ {
					mask[k] = true
				}
				q.inline = run
				q.lineStart = false
				q.indent = 0
				i = j
				continue
			}
			q.lineStart = false
			q.indent = 0
			i = j
		default:
			q.lineStart = false
			q.indent = 0
			i++
		}
	}
	return mask, q
}

func (n *textNormalizer) resetEnvelope() {
	n.inEnvelope = false
	n.discarding = false
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

// LongCat's serialized-call grammar.
//
// LongCat-Flash-Chat documents a JSON function object inside the envelope.
// LongCat 2.5 instead writes the tool name followed by a repeating
// key/value pair:
//
//	<longcat_tool_call>glob
//	<longcat_arg_key>pattern</longcat_arg_key>
//	<longcat_arg_value>**/*.go</longcat_arg_value>
//	</longcat_tool_call>
//
// The template does not XML-escape keys or string values, so this parser
// treats the envelope as a token grammar rather than XML. A well-formed
// envelope with no argument pairs is a real no-argument call.
func parseLongCatEnvelope(body string, coercer *argumentCoercer) ([]ToolCall, error) {
	trimmed := strings.TrimSpace(body)
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		return parseJSONToolCallEnvelope(trimmed)
	}

	const (
		keyOpen    = "<longcat_arg_key>"
		keyClose   = "</longcat_arg_key>"
		valueOpen  = "<longcat_arg_value>"
		valueClose = "</longcat_arg_value>"
	)

	rest := body
	name := ""
	if at := strings.Index(body, keyOpen); at >= 0 {
		name = strings.TrimSpace(body[:at])
		rest = body[at:]
	} else {
		name = strings.TrimSpace(body)
		rest = ""
	}
	if name == "" {
		return nil, errors.New("tool call has no name")
	}
	// A bare tool name is required. Rejecting whitespace and delimiters keeps
	// arbitrary prose from being fabricated into a call when the model emits
	// a malformed envelope.
	if strings.ContainsAny(name, " \t\r\n<>{}") {
		return nil, fmt.Errorf("tool name %q is not a bare identifier", name)
	}

	args := make(map[string]any)
	for {
		rest = strings.TrimLeft(rest, " \t\r\n")
		if rest == "" {
			break
		}
		if !strings.HasPrefix(rest, keyOpen) {
			return nil, fmt.Errorf("expected %s", keyOpen)
		}
		rest = rest[len(keyOpen):]
		end := strings.Index(rest, keyClose)
		if end < 0 {
			return nil, errors.New("argument key is unterminated")
		}
		key := strings.TrimSpace(rest[:end])
		rest = rest[end+len(keyClose):]
		if key == "" {
			return nil, errors.New("argument key is empty")
		}

		rest = strings.TrimLeft(rest, " \t\r\n")
		if !strings.HasPrefix(rest, valueOpen) {
			return nil, fmt.Errorf("argument %q has no value", key)
		}
		rest = rest[len(valueOpen):]
		end = strings.Index(rest, valueClose)
		if end < 0 {
			return nil, fmt.Errorf("argument %q has an unterminated value", key)
		}
		value := strings.TrimSpace(rest[:end])
		rest = rest[end+len(valueClose):]
		if _, exists := args[key]; exists {
			return nil, fmt.Errorf("argument %q appears more than once", key)
		}
		args[key] = coercer.coerce(name, key, value)
	}
	return []ToolCall{{Name: name, Arguments: args}}, nil
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
