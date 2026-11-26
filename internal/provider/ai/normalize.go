package ai

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
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

// maxNormalizationEvidenceBytes bounds the raw provider fragment retained for
// one failed normalization. The full envelope can be much larger; evidence is
// for audit and the beginning is enough to identify the representation.
const maxNormalizationEvidenceBytes = 4096

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
// Diagnostics never include the raw model payload in user-visible text. The
// bounded RawFragment field exists for an explicit debug/audit sink; the
// default is silence. Independently of the sink, a failed decision is attached
// to the assembled AssistantMessage as structured
// ToolNormalizationFailure evidence.
type NormalizationDiagnostic struct {
	Kind      NormalizationDiagnosticKind
	Protocol  string
	Detail    string
	ToolCalls int

	// Status is the canonical normalization state for this decision.
	Status NormalizationStatus
	// Provider is the provider name that produced the stream, when known.
	Provider string
	// ToolName is the envelope-declared tool name, when it could be read.
	ToolName string
	// RawFragment is bounded provider evidence. It is audit-only.
	RawFragment string
	// Recoverable reports whether another model turn can plausibly correct the
	// representation without changing authority or tools.
	Recoverable bool
}

// NormalizeOptions configures stream normalization.
type NormalizeOptions struct {
	// Tools are the tools offered on this request. Their schemas let the
	// normalizer convert textual XML parameters into the JSON types the tool
	// expects (number, boolean, array, object). Missing or unhelpful schemas
	// leave the parameter as a string.
	Tools []ToolSpec
	// Provider names the wire provider for structured failure evidence. It is
	// descriptive metadata and never changes parsing.
	Provider string
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
	msg.NormalizationFailures = append([]ToolNormalizationFailure(nil), normalizer.failures...)
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
		msg.NormalizationFailures = append(msg.NormalizationFailures, ToolNormalizationFailure{
			Status:      NormalizationUnsupported,
			Provider:    opts.Provider,
			Protocol:    "dots_call_family",
			Reason:      "unrecognized serialized tool-call spelling",
			Recoverable: false,
		})
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
			if isFamilyTagPrefix(lower[i:]) {
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
		if isFamilyTag(tag) {
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

// callFamily names a serialized tool-call tag family whose exact registered
// spelling may be decorated by the provider, usually with a call id.
//
// The registry decodes exact spellings. A decorated spelling is still the same
// protocol family, so the scanner consumes the envelope instead of publishing
// it as prose. When parse is set, the body is decoded with the same grammar the
// registered spelling uses; the decoration itself is ignored, so it can grant
// no authority. A family without a parser cannot safely decode a body and is
// reported as unsupported rather than fabricated. A provider that decorates a
// registered envelope adds its prefix here, not a branch in the TUI or the
// agent loop.
type callFamily struct {
	// name identifies the family in diagnostics.
	name string
	// open and close are the lower-case tag prefixes.
	open  string
	close string
	// require, when set, is a substring the tag name must contain. The dots
	// family historically requires "call" so an ordinary word that merely
	// starts with the prefix is not consumed as protocol.
	require string
	// parse decodes the envelope body when the family has a known grammar.
	// Nil means the spelling is contained but never decoded.
	parse func(body string, coercer *argumentCoercer) ([]ToolCall, error)
}

// callFamilies lists the prefixes this build can contain. The exact registered
// protocol wins when both match; the family handles decorated spellings.
var callFamilies = []callFamily{
	// The dots family checks first so an unknown dots spelling that opens
	// before a trailing '<' is still recognized as a complete envelope; a
	// later family's partial tag must not shadow it.
	{name: "dots_call_family", open: "<dots", close: "</dots", require: "call"},
	// The id-decorated spellings must precede the bare family: their opening
	// tags share its prefix, and only the longer marker closes at the real
	// wrapper instead of the first inner </tool_call:...>.
	{name: "tool_calls_id_family", open: "<tool_calls:", close: "</tool_calls:", parse: parseCallIDEnvelope},
	{name: "tool_call_id_family", open: "<tool_call:", close: "</tool_call:", parse: parseCallIDEnvelope},
	{name: "tool_call_family", open: "<tool_call", close: "</tool_call", parse: parseToolCallEnvelope},
	{name: "toolcall_family", open: "<toolcall", close: "</toolcall", parse: parseToolCallEnvelope},
}

// familyAt returns the family whose opening prefix starts buf.
func familyAt(buf []byte) (callFamily, bool) {
	lower := bytes.ToLower(buf)
	for _, family := range callFamilies {
		if bytes.HasPrefix(lower, []byte(family.open)) {
			return family, true
		}
	}
	return callFamily{}, false
}

// familyTagName returns the lower-case name inside an opening or closing tag.
func familyTagName(tag string) string {
	tag = strings.ToLower(strings.TrimSpace(tag))
	tag = strings.TrimPrefix(tag, "</")
	tag = strings.TrimPrefix(tag, "<")
	return strings.TrimSuffix(tag, ">")
}

// isFamilyOpenTag reports whether tag is a complete opening tag in the family.
func (f callFamily) isOpenTag(tag string) bool {
	tag = strings.ToLower(strings.TrimSpace(tag))
	if !strings.HasPrefix(tag, "<") || strings.HasPrefix(tag, "</") {
		return false
	}
	if !strings.HasPrefix(tag, f.open) {
		return false
	}
	return f.require == "" || strings.Contains(familyTagName(tag), f.require)
}

// isFamilyTag reports whether a complete tag belongs to any call family.
func isFamilyTag(tag string) bool {
	for _, family := range callFamilies {
		if family.isOpenTag(tag) {
			return true
		}
		if strings.HasPrefix(strings.ToLower(tag), family.close) &&
			(family.require == "" || strings.Contains(familyTagName(tag), family.require)) {
			return true
		}
	}
	return false
}

// isFamilyTagPrefix reports whether a partial tag could become one. It is used
// only by the residue fallback, where the normalizer has already had the chance
// to consume a complete tag.
func isFamilyTagPrefix(tag string) bool {
	lower := strings.ToLower(tag)
	for _, family := range callFamilies {
		if !strings.HasPrefix(lower, family.open) && !strings.HasPrefix(lower, family.close) {
			continue
		}
		if family.require == "" || strings.Contains(familyTagName(lower), family.require) {
			return true
		}
	}
	return false
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
	// familyClose reports that the close side is any tag in family rather than
	// one exact spelling. Decorative spellings use it: the family is
	// recognizable even when this build has never seen the exact separator, and
	// the body can still be consumed instead of being published as assistant
	// prose.
	familyClose bool
	// family is the tag family an unknown spelling belongs to. It is only set
	// when familyClose is true.
	family callFamily
	// unsupported marks a recognized family spelling this build cannot decode.
	// It changes the failure classification, never the containment behaviour.
	unsupported bool
	// parse turns the inner body into canonical calls. A non-nil error means
	// the envelope is not trusted and must not become assistant prose.
	parse func(body string, coercer *argumentCoercer) ([]ToolCall, error)
}

// familyProtocol describes an opening tag in a call family that is not one of
// the registered spellings.
//
// A family with a parser decodes the body with the same grammar as the
// registered tag: a decoration is transport noise, not authority, and the call
// still crosses grounding, authorization, schema validation, budget, and the
// execution gate. A family without a parser is not parsed; no action may be
// fabricated from a representation the runtime does not understand. Either
// way the descriptor exists so the bounded scanner consumes the envelope
// through its family closing tag and never leaks raw protocol into the live
// transcript or persisted answer.
func familyProtocol(open []byte, family callFamily) pseudoProtocol {
	opened := make([]byte, len(open))
	copy(opened, open)
	protocol := pseudoProtocol{
		name:        family.name,
		open:        opened,
		familyClose: true,
		family:      family,
	}
	if family.parse != nil {
		// The family has a grammar, so a closed body that fails to parse is
		// malformed and an unterminated envelope is incomplete. The decoration
		// itself is not unsupported evidence.
		protocol.parse = family.parse
		return protocol
	}
	protocol.unsupported = true
	protocol.parse = func(string, *argumentCoercer) ([]ToolCall, error) {
		return nil, errors.New("unrecognized " + family.name + " tool-call spelling")
	}
	return protocol
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
			// Baidu ERNIE emits an XML invoke envelope under an ernie:
			// namespace. It reached the runtime as final assistant text on a
			// free gateway and was published as an answer until this spelling
			// was registered.
			name:  "ernie_tool_call",
			open:  []byte("<ernie:tool_call>"),
			close: []byte("</ernie:tool_call>"),
			parse: parseXMLInvokeEnvelope,
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
	provider    string
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
	// failures is the structured evidence of every failed decision, in stream
	// order. It survives resetEnvelope so the completed message can carry the
	// canonical failure state.
	failures []ToolNormalizationFailure
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
		provider:    opts.Provider,
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
		n.reportFailure(kind, n.proto.name, n.proto.unsupported, "pseudo-tool envelope did not complete", raw)
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
					n.reportFailure(DiagnosticOversizedPseudoToolCall, n.proto.name, false,
						fmt.Sprintf("envelope exceeded %d bytes", n.maxEnvelope), n.raw)
					n.resetEnvelope()
					continue
				}

				calls, err := n.proto.parse(string(n.body), n.coercer)
				switch {
				case err != nil:
					out = appendTextItem(out, protocolFailureText(DiagnosticMalformedPseudoToolCall, n.proto.name))
					n.residue = true
					n.reportFailure(DiagnosticMalformedPseudoToolCall, n.proto.name, n.proto.unsupported, err.Error(), n.raw)
				case len(calls) == 0:
					out = appendTextItem(out, protocolFailureText(DiagnosticMalformedPseudoToolCall, n.proto.name))
					n.residue = true
					n.reportFailure(DiagnosticMalformedPseudoToolCall, n.proto.name, n.proto.unsupported,
						"envelope contained no tool call", n.raw)
				default:
					for i := range calls {
						n.calls++
						calls[i].ID = pseudoCallID(n.proto.name, string(n.body), n.calls)
						calls[i].Origin = ToolCallSerialized
						calls[i].Protocol = n.proto.name
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
					n.reportFailure(DiagnosticOversizedPseudoToolCall, n.proto.name, false,
						fmt.Sprintf("envelope exceeded %d bytes", n.maxEnvelope), n.raw)
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
			n.reportFailure(DiagnosticMalformedPseudoToolCall, "", false,
				"closing marker appeared before an opening marker", n.pending[closeIdx:closeIdx+length])
			n.pending = append(n.pending[:0], n.pending[closeIdx+length:]...)
			continue
		}

		if idx >= 0 && partial {
			// The bytes before the possible tag are unambiguous text. Hold the
			// tag until its terminator (or the bounded fallback) resolves it.
			out = n.emitText(out, string(n.pending[:idx]))
			n.pending = append(n.pending[:0], n.pending[idx:]...)
			if family, ok := familyAt(n.pending); ok && len(n.pending) >= maxOpenTagBytes {
				// A family tag that has not terminated within the bound is
				// protocol residue, not unbounded prose. Consume its bytes and
				// look for the family close with the normal envelope caps.
				open := append([]byte(nil), n.pending...)
				n.enterEnvelope(familyProtocol(open, family), open)
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

	for _, family := range callFamilies {
		familyIdx, state, familyEnd := n.earliestFamilyOpen(mask, family)
		switch state {
		case familyTagPartial:
			if best < 0 || familyIdx < best {
				return familyIdx, 0, pseudoProtocol{}, true
			}
		case familyTagComplete:
			if best >= 0 && best <= familyIdx {
				continue
			}
			tag := n.pending[familyIdx:familyEnd]
			return familyIdx, len(tag), familyProtocol(tag, family), false
		}
	}
	if best >= 0 {
		return best, bestLen, match, false
	}
	return -1, 0, pseudoProtocol{}, false
}

// earliestFamilyOpen finds the earliest live opening tag in one family even
// when its exact spelling is not registered. A complete tag must satisfy the
// family's own rule; a tag shorter than the bound but without its terminator is
// partial and must be retained.
func (n *textNormalizer) earliestFamilyOpen(mask []bool, family callFamily) (int, familyTagState, int) {
	prefix := []byte(family.open)
	for i := 0; i < len(n.pending); i++ {
		if mask[i] || n.pending[i] != '<' {
			continue
		}
		if i+1 < len(n.pending) && n.pending[i+1] == '/' {
			continue
		}
		rest := bytes.ToLower(n.pending[i:])
		if len(rest) < len(prefix) {
			if bytes.HasPrefix(prefix, rest) {
				return i, familyTagPartial, -1
			}
			continue
		}
		if !bytes.HasPrefix(rest, prefix) {
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
		if family.isOpenTag(string(n.pending[i:end])) {
			return i, familyTagComplete, end
		}
		i = end - 1
	}
	return -1, familyTagNone, -1
}

// earliestStrayClose finds a live closing marker with no envelope to close.
func (n *textNormalizer) earliestStrayClose(mask []bool) (int, int) {
	best, bestLen := indexAnyLive(n.pending, mask, n.closes)
	for _, family := range callFamilies {
		if idx, length := indexFamilyCloseLive(n.pending, mask, family.close); idx >= 0 && (best < 0 || idx < best) {
			best, bestLen = idx, length
		}
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
	for _, family := range callFamilies {
		if length := longestSuffixPrefixLive(bytes.ToLower(n.pending), mask, []byte(family.open)); length > keep {
			keep = length
		}
		if length := familyCloseLiveSuffix(n.pending, mask, family.close); length > keep {
			keep = length
		}
	}
	if length := longestSuffixPrefixAnyLive(n.pending, mask, n.closes); length > keep {
		keep = length
	}
	return keep
}

// findClose returns the next closing marker for the active envelope.
func (n *textNormalizer) findClose(buf []byte) (int, int) {
	if n.proto.familyClose {
		return indexFamilyClose(buf, n.proto.family.close)
	}
	return indexAny(buf, n.proto.closingMarkers())
}

// retainedCloseSuffix holds only a suffix that could begin the active close.
func (n *textNormalizer) retainedCloseSuffix(buf []byte) int {
	if n.proto.familyClose {
		return familyCloseSuffix(buf, n.proto.family.close)
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

// indexFamilyClose finds any closing tag in the family. It is used for an
// unknown family spelling whose exact close was never registered.
func indexFamilyClose(buf []byte, prefix string) (int, int) {
	lower := bytes.ToLower(buf)
	closePrefix := []byte(prefix)
	offset := 0
	for offset < len(lower) {
		rel := bytes.Index(lower[offset:], closePrefix)
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
func indexFamilyCloseLive(buf []byte, mask []bool, prefix string) (int, int) {
	lower := bytes.ToLower(buf)
	closePrefix := []byte(prefix)
	for i := 0; i < len(lower); i++ {
		if mask[i] || lower[i] != '<' {
			continue
		}
		if i+1 >= len(lower) || lower[i+1] != '/' {
			continue
		}
		if !bytes.HasPrefix(lower[i:], closePrefix) {
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
		if bytes.Contains(lower[i:end], []byte("call")) {
			return i, end - i
		}
	}
	return -1, 0
}

// familyCloseSuffix is the retained close prefix for an unknown family
// envelope.
func familyCloseSuffix(buf []byte, prefix string) int {
	lower := bytes.ToLower(buf)
	closePrefix := []byte(prefix)
	for i := len(buf) - 1; i >= 0; i-- {
		if buf[i] != '<' {
			continue
		}
		tail := lower[i:]
		if len(tail) <= len(closePrefix) {
			if bytes.HasPrefix(closePrefix, tail) {
				return len(tail)
			}
			return 0
		}
		if !bytes.HasPrefix(tail, closePrefix) {
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
func familyCloseLiveSuffix(buf []byte, mask []bool, prefix string) int {
	lower := bytes.ToLower(buf)
	closePrefix := []byte(prefix)
	for i := len(buf) - 1; i >= 0; i-- {
		if mask[i] || buf[i] != '<' {
			continue
		}
		tail := lower[i:]
		if len(tail) <= len(closePrefix) {
			if bytes.HasPrefix(closePrefix, tail) {
				return len(tail)
			}
			return 0
		}
		if !bytes.HasPrefix(tail, closePrefix) {
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
	status := NormalizationValid
	if kind != DiagnosticPseudoToolCall {
		status = statusForDiagnostic(kind)
	}
	if n.diagnostic != nil {
		n.diagnostic(NormalizationDiagnostic{
			Kind:      kind,
			Protocol:  protocol,
			Detail:    detail,
			ToolCalls: calls,
			Status:    status,
			Provider:  n.provider,
		})
	}
}

// reportFailure records one failed normalization as both a bounded diagnostic
// and durable structured evidence on the message. The raw fragment is bounded
// and audit-only; it is never emitted as prose or executed.
func (n *textNormalizer) reportFailure(kind NormalizationDiagnosticKind, protocol string, unsupported bool, detail string, raw []byte) {
	status := statusForDiagnostic(kind)
	if unsupported {
		status = NormalizationUnsupported
	}
	fragment := boundedEvidenceFragment(raw)
	failure := ToolNormalizationFailure{
		Status:      status,
		Provider:    n.provider,
		Protocol:    protocol,
		RawFragment: fragment,
		Reason:      detail,
		Recoverable: status != NormalizationUnsupported,
	}
	n.failures = append(n.failures, failure)
	if n.diagnostic == nil {
		return
	}
	n.diagnostic(NormalizationDiagnostic{
		Kind:        kind,
		Protocol:    protocol,
		Detail:      detail,
		Status:      status,
		Provider:    n.provider,
		RawFragment: fragment,
		Recoverable: failure.Recoverable,
	})
}

// statusForDiagnostic maps the transport diagnostic vocabulary onto the
// canonical normalization state.
func statusForDiagnostic(kind NormalizationDiagnosticKind) NormalizationStatus {
	switch kind {
	case DiagnosticIncompletePseudoToolCall:
		return NormalizationIncomplete
	case DiagnosticMalformedPseudoToolCall, DiagnosticOversizedPseudoToolCall:
		return NormalizationInvalid
	default:
		return NormalizationValid
	}
}

// boundedEvidenceFragment keeps the beginning and end of a failed envelope so
// a debug sink can identify the representation without retaining an unbounded
// model payload.
func boundedEvidenceFragment(raw []byte) string {
	if len(raw) <= maxNormalizationEvidenceBytes {
		return string(raw)
	}
	half := maxNormalizationEvidenceBytes / 2
	return string(raw[:half]) + "\n…[truncated]…\n" +
		string(raw[len(raw)-half:])
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

// parseToolCallEnvelope accepts the payload shapes commonly carried by a
// generic <tool_call> marker (including a provider-decorated tag): a JSON
// function object (one or an array), the XML invoke grammar, and the
// <function=name><parameter=key>value</parameter></function> grammar.
// Anything else is rejected and stays visible text.
func parseToolCallEnvelope(body string, coercer *argumentCoercer) ([]ToolCall, error) {
	trimmed := strings.TrimSpace(body)
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		return parseJSONToolCallEnvelope(trimmed)
	}
	if strings.HasPrefix(trimmed, functionOpenTag) {
		return parseFunctionEnvelope(trimmed, coercer)
	}
	return parseXMLInvokeEnvelope(body, coercer)
}

const (
	functionOpenTag   = "<function="
	functionCloseTag  = "</function>"
	parameterOpenTag  = "<parameter="
	parameterCloseTag = "</parameter>"
)

// parseFunctionEnvelope parses the equals-delimited function grammar a free
// gateway emits inside a <tool_call> envelope:
//
//	<function=todo>
//	<parameter=op>done</parameter>
//	<parameter=task>summarize the project</parameter>
//	</function>
//
// The tag name is the tool name and each parameter tag name is the argument
// key. The grammar is deliberately narrow: a missing terminator, empty name, or
// non-parameter content rejects the whole envelope, so malformed serialization
// can never be fabricated into an executable call. Values are trimmed like the
// XML invoke grammar and coerced against the offered tool schema.
func parseFunctionEnvelope(body string, coercer *argumentCoercer) ([]ToolCall, error) {
	rest := strings.TrimSpace(body)
	calls := make([]ToolCall, 0, 1)
	for rest != "" {
		if !strings.HasPrefix(rest, functionOpenTag) {
			return nil, fmt.Errorf("expected %s", functionOpenTag)
		}
		rest = rest[len(functionOpenTag):]
		end := strings.IndexByte(rest, '>')
		if end < 0 {
			return nil, errors.New("function tag is unterminated")
		}
		name := strings.TrimSpace(rest[:end])
		if name == "" {
			return nil, errors.New("function has no name")
		}
		if strings.ContainsAny(name, " \t\r\n<>=") {
			return nil, fmt.Errorf("function name %q is not a bare identifier", name)
		}
		rest = rest[end+1:]

		args := make(map[string]any)
		for {
			rest = strings.TrimSpace(rest)
			if strings.HasPrefix(rest, functionCloseTag) {
				rest = rest[len(functionCloseTag):]
				break
			}
			if rest == "" {
				return nil, errors.New("function is unterminated")
			}
			if !strings.HasPrefix(rest, parameterOpenTag) {
				return nil, fmt.Errorf("expected %s or %s", parameterOpenTag, functionCloseTag)
			}
			rest = rest[len(parameterOpenTag):]
			end = strings.IndexByte(rest, '>')
			if end < 0 {
				return nil, errors.New("parameter tag is unterminated")
			}
			key := strings.TrimSpace(rest[:end])
			if key == "" {
				return nil, errors.New("parameter has no name")
			}
			rest = rest[end+1:]
			closeAt := strings.Index(rest, parameterCloseTag)
			if closeAt < 0 {
				return nil, fmt.Errorf("parameter %q is unterminated", key)
			}
			value := strings.TrimSpace(rest[:closeAt])
			rest = rest[closeAt+len(parameterCloseTag):]
			args[key] = coercer.coerce(name, key, value)
		}
		calls = append(calls, ToolCall{Name: name, Arguments: args})
	}
	if len(calls) == 0 {
		return nil, errors.New("envelope contains no function")
	}
	return calls, nil
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

// parseCallIDEnvelope parses the call-id-decorated grammar a free gateway
// emits:
//
//	<tool_calls:6124c78e>
//	  <tool_call:6124c78e>read
//	    <arg_key:6124c78e>path</arg_key:6124c78e>
//	    <arg_value:6124c78e>backend/layout/http_admin_products.go</arg_value:6124c78e>
//	  </tool_call:6124c78e>
//	</tool_calls:6124c78e>
//
// Every tag carries the same fabricated id after a colon. The id is transport
// decoration: it is ignored and grants no authority. A single call may appear
// without the plural wrapper, so the body is either one tool_call element or
// the bare name/argument body of one. The grammar is strict: an unterminated
// tag, a missing value, or prose between calls rejects the whole envelope
// rather than fabricating an executable call.
func parseCallIDEnvelope(body string, coercer *argumentCoercer) ([]ToolCall, error) {
	rest := strings.TrimSpace(body)
	if rest == "" {
		return nil, errors.New("envelope contains no tool call")
	}
	if strings.HasPrefix(strings.ToLower(rest), "<tool_call") {
		calls := make([]ToolCall, 0, 1)
		for {
			rest = strings.TrimSpace(rest)
			if rest == "" {
				break
			}
			tag, after, err := cutCallIDTag(rest)
			if err != nil {
				return nil, err
			}
			if tag.closing || tag.name != "tool_call" {
				return nil, fmt.Errorf("expected an opening tool_call tag, got %q", tag.raw)
			}
			call, next, err := parseCallIDBody(after, coercer, true)
			if err != nil {
				return nil, err
			}
			calls = append(calls, call)
			rest = next
		}
		if len(calls) == 0 {
			return nil, errors.New("envelope contains no tool call")
		}
		return calls, nil
	}
	// Without the plural wrapper the scanner has already consumed the single
	// call's closing tag, so its body has no inner terminator.
	call, _, err := parseCallIDBody(rest, coercer, false)
	if err != nil {
		return nil, err
	}
	return []ToolCall{call}, nil
}

// callIDTag is one parsed tag of the id-decorated grammar. The decoration
// suffix is kept only for diagnostics; matching uses the local name.
type callIDTag struct {
	name    string
	raw     string
	closing bool
}

// cutCallIDTag consumes one tag and returns the remaining body.
func cutCallIDTag(s string) (callIDTag, string, error) {
	if !strings.HasPrefix(s, "<") {
		return callIDTag{}, "", errors.New("expected a tag")
	}
	closing := strings.HasPrefix(s, "</")
	offset := 1
	if closing {
		offset = 2
	}
	end := strings.IndexByte(s[offset:], '>')
	if end < 0 {
		return callIDTag{}, "", errors.New("tag is unterminated")
	}
	end += offset
	raw := strings.TrimSpace(s[offset:end])
	if raw == "" {
		return callIDTag{}, "", errors.New("tag has no name")
	}
	if strings.ContainsAny(raw, " \t\r\n") {
		return callIDTag{}, "", fmt.Errorf("tag %q carries attributes", raw)
	}
	name, _, _ := strings.Cut(raw, ":")
	if name == "" {
		return callIDTag{}, "", errors.New("tag has no name")
	}
	return callIDTag{name: strings.ToLower(name), raw: raw, closing: closing}, s[end+1:], nil
}

// cutCallIDText returns the character data before the next tag.
func cutCallIDText(s string) (string, string, error) {
	at := strings.IndexByte(s, '<')
	if at < 0 {
		return "", "", errors.New("text is unterminated")
	}
	return s[:at], s[at:], nil
}

// parseCallIDBody parses one tool_call body: the bare tool name followed by
// arg_key/arg_value pairs. A body inside the plural wrapper ends at its own
// tool_call tag; a body whose closing tag the scanner already consumed ends at
// end of input.
func parseCallIDBody(body string, coercer *argumentCoercer, closed bool) (ToolCall, string, error) {
	at := strings.IndexByte(body, '<')
	name := strings.TrimSpace(html.UnescapeString(body))
	if at >= 0 {
		name = strings.TrimSpace(html.UnescapeString(body[:at]))
	}
	if name == "" {
		return ToolCall{}, "", errors.New("tool call has no name")
	}
	// A bare tool name is required so arbitrary prose cannot be fabricated
	// into a call when the envelope is malformed.
	if strings.ContainsAny(name, " \t\r\n<>{}=") {
		return ToolCall{}, "", fmt.Errorf("tool name %q is not a bare identifier", name)
	}

	rest := ""
	if at >= 0 {
		rest = body[at:]
	}
	args := make(map[string]any)
	for {
		if strings.TrimSpace(rest) == "" {
			if closed {
				return ToolCall{}, "", errors.New("tool call is unterminated")
			}
			return ToolCall{Name: name, Arguments: args}, "", nil
		}
		tag, after, err := cutCallIDTag(rest)
		if err != nil {
			return ToolCall{}, "", err
		}
		if tag.closing {
			if tag.name != "tool_call" {
				return ToolCall{}, "", fmt.Errorf("unexpected closing tag %q", tag.raw)
			}
			return ToolCall{Name: name, Arguments: args}, after, nil
		}
		if tag.name != "arg_key" {
			return ToolCall{}, "", fmt.Errorf("expected arg_key, got %q", tag.raw)
		}

		keyText, afterKey, err := cutCallIDText(after)
		if err != nil {
			return ToolCall{}, "", err
		}
		key := strings.TrimSpace(html.UnescapeString(keyText))
		if key == "" {
			return ToolCall{}, "", errors.New("argument has no name")
		}
		closeKey, afterCloseKey, err := cutCallIDTag(afterKey)
		if err != nil {
			return ToolCall{}, "", err
		}
		if !closeKey.closing || closeKey.name != "arg_key" {
			return ToolCall{}, "", fmt.Errorf("argument %q has no closing arg_key tag", key)
		}
		valueTag, afterValueTag, err := cutCallIDTag(afterCloseKey)
		if err != nil {
			return ToolCall{}, "", err
		}
		if valueTag.closing || valueTag.name != "arg_value" {
			return ToolCall{}, "", fmt.Errorf("argument %q has no arg_value tag", key)
		}
		valueText, afterValue, err := cutCallIDText(afterValueTag)
		if err != nil {
			return ToolCall{}, "", err
		}
		closeValue, afterCloseValue, err := cutCallIDTag(afterValue)
		if err != nil {
			return ToolCall{}, "", err
		}
		if !closeValue.closing || closeValue.name != "arg_value" {
			return ToolCall{}, "", fmt.Errorf("argument %q has no closing arg_value tag", key)
		}
		if _, exists := args[key]; exists {
			return ToolCall{}, "", fmt.Errorf("argument %q appears more than once", key)
		}
		args[key] = coercer.coerce(name, key, strings.TrimSpace(html.UnescapeString(valueText)))
		rest = afterCloseValue
	}
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
