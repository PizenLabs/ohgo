package agent

import (
	"hash/fnv"
	"reflect"
	"strings"

	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// Cache-locality accounting.
//
// The provider owns prompt caching; Ohgo owns nothing here. What Ohgo controls
// is whether the next request repeats the bytes the previous one sent, because
// a provider cache matches a prefix: the first changed byte invalidates every
// cached token after it. These types measure that property — the request shape
// and where it diverged — and pair it with the provider's own reported cache
// reads, so a regression can be attributed instead of guessed at.

// RequestShape describes how one outbound request relates to the request before
// it. It is structural, not measured: it says which regions changed, never what
// the provider cached.
type RequestShape struct {
	// SystemStable reports the system prefix is byte-identical to the previous
	// request.
	SystemStable bool
	// ToolsStable reports the offered tool definitions are byte-identical.
	ToolsStable bool
	// ModelStable reports the model identity is unchanged.
	ModelStable bool
	// StableMessages counts the leading conversation messages that are
	// byte-identical to the previous request. It is the reused history: every
	// token after it was re-sent.
	StableMessages int
	// Messages is this request's conversation length.
	Messages int
	// Rewrites counts projector attempts to change an already-sent message.
	// The loop suppresses them, so they are a defect signal, not a loss.
	Rewrites int
	// Rewound reports the conversation shrank: compaction, a restored session,
	// or a projection that dropped messages.
	Rewound bool
}

// Causes names the regions that changed since the previous request, in the
// order that matters for cost. An empty result means the request repeated the
// previous prefix exactly.
func (s RequestShape) Causes() []string {
	var causes []string
	if !s.SystemStable {
		causes = append(causes, "system")
	}
	if !s.ToolsStable {
		causes = append(causes, "tools")
	}
	if !s.ModelStable {
		causes = append(causes, "model")
	}
	if s.Rewound {
		causes = append(causes, "rewind")
	}
	if s.Rewrites > 0 {
		causes = append(causes, "history_rewrite_suppressed")
	}
	return causes
}

// CacheSample is one measured request: the provider's reported usage plus the
// shape that produced it.
type CacheSample struct {
	// Turn is the completed-turn index the request preceded.
	Turn int
	// Input, Output, Reasoning, CacheRead, and CacheWrite are the provider's
	// reported counts. A provider that reports no cache support leaves both
	// cache fields zero.
	Input      int
	Output     int
	Reasoning  int
	CacheRead  int
	CacheWrite int
	// Shape is the structural comparison with the previous request.
	Shape RequestShape
}

// CachedInput is the prompt total: what the provider read, cached or not.
func (s CacheSample) CachedInput() int { return s.CacheRead }

// UncachedInput is the input the provider had to process. It is the number that
// costs latency and money when cache locality is lost.
func (s CacheSample) UncachedInput() int { return s.Input }

// CacheRatio is the cached share of the prompt, or zero when the provider
// reported no prompt at all.
func (s CacheSample) CacheRatio() float64 {
	prompt := s.Input + s.CacheRead
	if prompt <= 0 {
		return 0
	}
	return float64(s.CacheRead) / float64(prompt)
}

// Cache verdicts.
const (
	// CacheStable means the cached share did not fall.
	CacheStable = "STABLE"
	// CacheExpected means the cached share fell and the request shape explains
	// why: a different prefix was sent.
	CacheExpected = "EXPECTED"
	// CacheSuspicious means the cached share fell while the request repeated
	// the previous prefix, or while the runtime had to suppress a rewrite.
	// Either way the loss is not semantics.
	CacheSuspicious = "SUSPICIOUS"
)

// CacheRegression is the classified difference between two measured requests.
type CacheRegression struct {
	// Delta is the change in cached tokens; negative means fewer were reused.
	Delta int
	// Class is one of the Cache* verdicts.
	Class string
	// Causes names the regions that changed.
	Causes []string
	// Detail explains a verdict in one line.
	Detail string
}

// ClassifyCache compares two consecutive measured requests.
//
// The verdict is about the *request*, never about provider health: a provider
// is not failing when the client changed the prefix, and a suspicious verdict
// names a client-side defect (an unstable prefix or a suppressed rewrite), not
// a cache policy.
func ClassifyCache(previous, current CacheSample) (CacheRegression, bool) {
	if previous.Input+previous.CacheRead <= 0 {
		return CacheRegression{}, false
	}
	delta := current.CacheRead - previous.CacheRead
	regression := CacheRegression{Delta: delta, Causes: current.Shape.Causes()}
	switch {
	case delta >= 0:
		regression.Class = CacheStable
		return regression, true
	case len(regression.Causes) == 0:
		regression.Class = CacheSuspicious
		regression.Detail = "the request repeated the previous prefix but the provider reused less of it"
	case current.Shape.Rewrites > 0:
		regression.Class = CacheSuspicious
		regression.Detail = "a projection rewrote already-sent history; the append-only boundary suppressed it"
	default:
		regression.Class = CacheExpected
		regression.Detail = "the request prefix changed: " + strings.Join(regression.Causes, ", ")
	}
	return regression, true
}

// joinRuntime renders the runtime state blocks into the request's trailing
// message.
//
// The order is the caller's, because the caller owns which block is most stable:
// a caller that puts durable orientation before transient status keeps a longer
// identical prefix inside the volatile tail itself.
func joinRuntime(blocks []string) string {
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if trimmed := strings.TrimSpace(block); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return strings.Join(parts, "\n\n")
}

// identicalWireMessage reports two outbound messages would serialize identically.
func identicalWireMessage(a, b ai.Message) bool {
	return reflect.DeepEqual(a, b)
}

// sameKind reports two outbound messages carry the same role, so restoring the
// earlier one cannot change the transcript's structure.
func sameKind(a, b ai.Message) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Role() == b.Role()
}

// prefixState is the previous request's structural identity.
type prefixState struct {
	seen     bool
	system   uint64
	tools    uint64
	model    string
	messages int
	// rewound records a deliberate rewrite since the previous request, so the
	// next request reports the rewind even when the rebuilt conversation is
	// as long as the one it replaced.
	rewound bool
}

// freezePrefix keeps the outbound conversation append-only and reports how this
// request relates to the previous one.
//
// A projection may elide a tool result, but the form a result was first sent in
// is the form the provider cached. Recomputing that form per request — a moving
// "recent results" window, a total-size budget that a growing conversation
// reallocates — silently rewrites history and invalidates every cached token
// after the rewrite. The boundary restores the earlier bytes instead, so
// reduction happens at compaction, where it is a deliberate state transition
// rather than a per-turn side effect.
func (l *Loop) freezePrefix(messages []ai.Message, system []ai.SystemPromptBlock, specs []ai.ToolSpec) RequestShape {
	shape := RequestShape{
		Messages:     len(messages),
		SystemStable: l.prefix.seen && l.prefix.system == digestSystem(system),
		ToolsStable:  l.prefix.seen && l.prefix.tools == digestTools(specs),
		ModelStable:  l.prefix.seen && l.prefix.model == l.model().ID,
		Rewound:      l.prefix.seen && (len(messages) < l.prefix.messages || l.prefix.rewound),
	}
	// Restoring earlier bytes is only safe while the two lists describe the
	// same conversation: every earlier position must still carry the same
	// role, or the host reconstructed a different transcript and a positional
	// restore would move content between turns.
	// A host TransformContext owns message rewriting: restoring earlier bytes
	// would silently undo whatever the transform was asked to remove, so the
	// conversation is reported but never repaired.
	structuralPrefix := l.cfg.TransformContext == nil &&
		l.prefix.seen && !shape.Rewound && len(l.frozen) == l.prefix.messages
	if structuralPrefix {
		for i := range l.prefix.messages {
			if !sameKind(l.frozen[i], messages[i]) {
				structuralPrefix = false
				shape.Rewound = true
				break
			}
		}
	}
	if structuralPrefix {
		for i := range l.prefix.messages {
			if identicalWireMessage(l.frozen[i], messages[i]) {
				shape.StableMessages++
				continue
			}
			messages[i] = l.frozen[i]
			shape.Rewrites++
		}
	}
	l.frozen = append(l.frozen[:0], messages...)
	l.prefix = prefixState{
		seen:     true,
		system:   digestSystem(system),
		tools:    digestTools(specs),
		model:    l.model().ID,
		messages: len(messages),
	}
	return shape
}

// observeRequest records one request's cache sample.
func (l *Loop) observeRequest(turn int, shape RequestShape, usage ai.Usage) {
	if l.cfg.Usage == nil {
		return
	}
	l.cfg.Usage.Observe(CacheSample{
		Turn:       turn,
		Input:      usage.Input,
		Output:     usage.Output,
		Reasoning:  usage.Reasoning,
		CacheRead:  usage.CacheRead,
		CacheWrite: usage.CacheWrite,
		Shape:      shape,
	})
}

// digestSystem fingerprints the system prefix.
func digestSystem(blocks []ai.SystemPromptBlock) uint64 {
	digest := fnv.New64a()
	for _, block := range blocks {
		_, _ = digest.Write([]byte(block.Text))
		_, _ = digest.Write([]byte{0})
		if block.Cache {
			_, _ = digest.Write([]byte{1})
		} else {
			_, _ = digest.Write([]byte{0})
		}
	}
	return digest.Sum64()
}

// digestTools fingerprints the offered tool surface.
func digestTools(specs []ai.ToolSpec) uint64 {
	digest := fnv.New64a()
	for _, spec := range specs {
		_, _ = digest.Write([]byte(spec.Name))
		_, _ = digest.Write([]byte{0})
		_, _ = digest.Write([]byte(spec.Description))
		_, _ = digest.Write([]byte{0})
		_, _ = digest.Write(spec.Parameters)
		_, _ = digest.Write([]byte{0})
	}
	return digest.Sum64()
}

// forgetFrozenHistory drops the append-only message baseline after a
// deliberate rewrite.
//
// The system, tool, and model digests survive: compaction changes the
// conversation, never the authority contract, so losing the digests would
// report a compaction as a system change and hide the real cause.
func (l *Loop) forgetFrozenHistory() {
	l.frozen = nil
	l.prefix.rewound = true
}
