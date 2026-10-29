package main

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/PizenLabs/ohgo/internal/config"
	"github.com/PizenLabs/ohgo/internal/prompt"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/provider/catalog"
)

// supMaxTokens bounds one $sup completion.
//
// The system prompt asks for the shortest answer that fully answers the
// question (~128 tokens); this cap is a guardrail against a runaway reply, not
// the target. It is deliberately far below the model's coding-agent ceiling —
// an answer surface has no file writes or long plans to fit.
const supMaxTokens = 1024

// supportBundle is one resolved $sup invocation target: the shared provider
// transport plus the model and effort to send through it.
//
// It carries a transport function rather than a built agent loop because $sup
// is an answer surface, not a workspace: no tools, no repository context, no
// execution semantics. A loop would be dead weight between the question and the
// provider (§1, §4).
type supportBundle struct {
	// stream opens one completion through the shared provider abstraction.
	stream ai.StreamFn
	// model is the resolved SUP-role model the request is sent to.
	model ai.Model
	// ref is the canonical "provider/id" the request is sent to.
	ref string
	// effort is the thinking effort the request runs with ("", "off", or a
	// ladder level).
	effort string
}

// supportSurface is the ephemeral $sup conversation.
//
// $sup is an interaction surface on the SUP model role, not a workspace
// (§10.1): the surface creates no authority and never changes the workspace
// the session runs under. It is a dedicated, context-light semantic path — a
// minimal system instruction plus the query, sent straight through the shared
// provider transport (§1). It gets no repository, workspace, tool, or main
// history context (§2, §3, §4), and an exchange cannot pollute the main task
// context because it is never appended to the main history or the session
// store (§10.2, I17).
//
// The bundle is resolved on every ask rather than cached, so a /model
// reassignment of SUP applies to the next $sup invocation with no restart.
type supportSurface struct {
	build func() (supportBundle, error)

	mu sync.Mutex
	// msgs is the surface's own ephemeral thread. It is the only history $sup
	// sees: never the main conversation, and it is discarded on close.
	msgs []ai.Message
}

// newSupportSurface wraps a bundle builder.
func newSupportSurface(build func() (supportBundle, error)) *supportSurface {
	return &supportSurface{build: build}
}

// ask runs one support question, continuing the ephemeral conversation.
//
// The returned answer is the concatenated assistant text. Nothing is
// persisted and no session state is touched. Deltas stream through onDelta
// (nil disables streaming); the returned string always holds the complete
// answer for transports that never token-stream.
func (s *supportSurface) ask(ctx context.Context, question string, onDelta func(string)) (string, error) {
	if s == nil {
		return "", fmt.Errorf("support is unavailable")
	}
	// A greeting or acknowledgement carries no question, so it is answered
	// locally without spending a provider request (§7). Canned exchanges are
	// not appended to the thread: they are not model turns, and keeping them
	// would grow the one context $sup does own.
	if answer, ok := supCanned(question); ok {
		return answer, nil
	}
	bundle, err := s.build()
	if err != nil {
		return "", err
	}

	s.mu.Lock()
	request := append(append([]ai.Message(nil), s.msgs...), supUserMessage(question))
	s.mu.Unlock()

	enabled, level := supReasoning(bundle.effort)
	stream, err := bundle.stream(ctx, ai.Request{
		Model:           bundle.model,
		System:          []ai.SystemPromptBlock{{Text: prompt.Sup}},
		Messages:        request,
		MaxTokens:       supMaxTokens,
		Reasoning:       enabled,
		ReasoningEffort: level,
	})
	if err != nil {
		return "", err
	}
	answer, err := ai.DrainStream(ctx, stream, onDelta)
	if err != nil {
		return "", err
	}

	s.mu.Lock()
	s.msgs = append(s.msgs, supUserMessage(question), answer)
	s.mu.Unlock()
	return answer.Text(), nil
}

// supUserMessage wraps a question as the provider user turn.
func supUserMessage(question string) ai.UserMessage {
	return ai.UserMessage{Content: []ai.ContentBlock{ai.TextContent{Text: question}}}
}

// supCanned returns a local answer for a greeting or acknowledgement, and
// whether the query matched.
//
// $sup is a quick-answer surface and these queries ask nothing, so sending them
// to a provider spends a request to be told hello. The table is a fixed literal
// set matched on the normalized whole query — deliberately not an intent
// classifier, embedding lookup, or rule engine (§7). Anything unlisted reaches
// the model unchanged.
func supCanned(query string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(query)) {
	case "hi", "hello", "hey":
		return "Hi.", true
	case "thanks", "thank you", "thx":
		return "You're welcome.", true
	case "ok", "okay":
		return "Got it.", true
	default:
		return "", false
	}
}

// close discards the ephemeral conversation.
func (s *supportSurface) close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.msgs = nil
	s.mu.Unlock()
}

// Support runs an ephemeral $sup query through the attached surface.
func (h *harness) Support(ctx context.Context, question string) (string, error) {
	return h.SupportStream(ctx, question, nil)
}

// SupportStream runs an ephemeral $sup query, streaming answer chunks
// through onDelta (nil disables streaming).
func (h *harness) SupportStream(ctx context.Context, question string, onDelta func(string)) (string, error) {
	if h.support == nil {
		return "", fmt.Errorf("support is unavailable")
	}
	return h.support.ask(ctx, question, onDelta)
}

// SupportInfo reports the effective SUP model reference and effort from
// the canonical configuration authority. It resolves the same way the
// runtime does, so the overlay displays what $sup will actually call.
// Unknown stays unknown rather than fabricated.
func (h *harness) SupportInfo() (model, effort string) {
	settings := h.settingsOrDefault()
	resolved, eff, err := resolveSupModel(settings, catalog.Resolve)
	if err == nil {
		return resolved.Ref(), eff
	}
	if ref, eff, _ := settings.RoleModel(string(config.RoleSup)); strings.TrimSpace(ref) != "" {
		return strings.TrimSpace(ref), strings.TrimSpace(eff)
	}
	fallback := catalog.Default()
	return fallback.Ref(), ""
}

// CloseSupport discards the ephemeral conversation without touching the main
// session.
func (h *harness) CloseSupport() {
	if h.support != nil {
		h.support.close()
	}
}

// resolveSupModel resolves the SUP role through the canonical authority
// chain: SUP role profile, then legacy layers, then the catalog default —
// exactly what Settings.RoleModel reports. Effort follows the same rule
// as the workspace run path: the role's configured effort, else the global
// default. An effort the bound model does not expose is refused rather
// than stored or silently downgraded, through the same Supports check
// the /model assignment seam uses.
//
// resolve looks references up; production passes live discovery, tests
// pass catalog.Resolve to stay offline and deterministic.
func resolveSupModel(settings config.Settings, resolve func(string) (ai.Model, error)) (ai.Model, string, error) {
	return resolveRoleModel(settings, config.RoleSup, "$sup", resolve)
}

// resolveRoleModel resolves one model role through the canonical authority
// chain: role profile, then legacy layers, then the catalog default — exactly
// what Settings.RoleModel reports. Effort follows the same rule as the
// workspace run path: the role's configured effort, else the global default.
// An effort the bound model does not expose is refused rather than stored or
// silently downgraded, through the same Supports check the /model assignment
// seam uses.
//
// label names the role in an error. resolve looks references up; production
// passes live discovery, tests pass catalog.Resolve to stay offline.
func resolveRoleModel(settings config.Settings, role config.Role, label string, resolve func(string) (ai.Model, error)) (ai.Model, string, error) {
	ref, roleEffort, _ := settings.RoleModel(string(role))
	ref = strings.TrimSpace(ref)
	roleEffort = strings.TrimSpace(roleEffort)
	effort, validate := roleEffectiveEffort(settings, roleEffort)
	if ref == "" {
		return catalog.Default(), effort, nil
	}
	model, err := resolve(ref)
	if err != nil {
		return ai.Model{}, "", err
	}
	canonical := model.Ref()
	if validate && effort != "" && effort != "off" {
		if !catalog.ThinkingFor(canonical).Supports(effort) {
			return ai.Model{}, "", fmt.Errorf("%s: %s does not expose thinking effort %q", label, canonical, effort)
		}
	}
	return model, effort, nil
}

// roleEffectiveEffort resolves a non-workspace role's thinking effort and
// whether it is a claim that must be validated against the bound model.
//
// An explicit role effort is a claim, so it is validated. An unset role effort
// inherits the global default only when thinking is enabled globally; when it
// is disabled the provider never receives an effort, so validating one would
// refuse a configuration that runs fine. That mirrors the workspace run path,
// where the global default does not enable thinking by itself.
func roleEffectiveEffort(settings config.Settings, roleEffort string) (effort string, validate bool) {
	switch roleEffort {
	case "off":
		return "off", false
	case "":
		if !settings.Reasoning {
			return "", false
		}
		return strings.TrimSpace(settings.ReasoningEffort), true
	default:
		return roleEffort, true
	}
}

// supReasoning maps an effective effort to the loop's thinking settings.
// "off" and "" both leave thinking disabled: the resolver only ever returns a
// level when thinking is explicitly requested.
func supReasoning(effort string) (bool, string) {
	switch effort {
	case "off", "":
		return false, ""
	default:
		return true, effort
	}
}

// supportBuilder returns a builder for the ephemeral $sup runtime.
//
// The model and effort resolve through the SUP role on every invocation, so a
// /model reassignment applies to the next $sup with no restart. The runtime is
// only the shared provider transport plus the resolved model: $sup is an answer
// surface, so it is built without the agent loop, the tool set, the repository
// context, and the compaction machinery the workspaces carry (§1, §2, §4).
//
// It is built lazily on first use: resolving a second model eagerly would cost
// a round trip for a capability most sessions never invoke.
func supportBuilder(opts options, registry *ai.Registry) func() (supportBundle, error) {
	return func() (supportBundle, error) {
		supModel, effort, err := resolveSupModel(opts.settings, dynamicResolver(context.Background(), registry))
		if err != nil {
			return supportBundle{}, err
		}
		return supportBundle{
			stream: registry.StreamFn(),
			model:  supModel,
			ref:    supModel.Ref(),
			effort: effort,
		}, nil
	}
}
