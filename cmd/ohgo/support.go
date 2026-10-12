package main

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/ai"
	"github.com/PizenLabs/ohgo/catalog"
	"github.com/PizenLabs/ohgo/config"
)

// supportBundle is one resolved SUP runtime: the loop plus the canonical
// identity it was built from.
type supportBundle struct {
	loop *agent.Loop
	// ref is the canonical "provider/id" the loop calls.
	ref string
	// effort is the thinking effort the loop runs with ("", "off", or a
	// ladder level).
	effort string
}

// supportSurface is the ephemeral $sup conversation.
//
// $sup is an interaction surface on the SUP model role, not a workspace
// (§10.1): the surface creates no authority and never changes the workspace
// the session runs under. The loop runs under ASK authority (read-only),
// but its model and effort resolve through the SUP role. Messages live only
// here — they are never appended to the main history and never written to
// the session store — so a support exchange cannot pollute the main task
// context (§10.2, I17).
//
// The bundle is resolved on every ask rather than cached, so a /model
// reassignment of SUP applies to the next $sup invocation with no restart.
type supportSurface struct {
	build func() (supportBundle, error)

	mu   sync.Mutex
	msgs []agent.Message
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
	bundle, err := s.build()
	if err != nil {
		return "", err
	}
	if onDelta != nil {
		bundle.loop.SetTextDeltaHook(func(delta string) {
			if delta != "" {
				onDelta(delta)
			}
		})
	} else {
		bundle.loop.SetTextDeltaHook(nil)
	}

	s.mu.Lock()
	request := append(append([]agent.Message(nil), s.msgs...), agent.UserMessage{Text: question})
	s.mu.Unlock()

	turns, err := bundle.loop.Run(ctx, request)
	if err != nil {
		return "", err
	}

	var answer strings.Builder
	s.mu.Lock()
	for _, turn := range turns {
		s.msgs = append(s.msgs, agent.AssistantMessage{Message: turn.Assistant})
		if text := turn.Assistant.Text(); text != "" {
			answer.WriteString(text)
		}
	}
	s.mu.Unlock()
	return answer.String(), nil
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
		return resolved.Provider + "/" + resolved.ID, eff
	}
	if ref, eff, _ := settings.RoleModel(string(config.RoleSup)); strings.TrimSpace(ref) != "" {
		return strings.TrimSpace(ref), strings.TrimSpace(eff)
	}
	fallback := catalog.Default()
	return fallback.Provider + "/" + fallback.ID, ""
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
	ref, roleEffort, _ := settings.RoleModel(string(config.RoleSup))
	ref = strings.TrimSpace(ref)
	roleEffort = strings.TrimSpace(roleEffort)
	effort, validate := supEffectiveEffort(settings, roleEffort)
	if ref == "" {
		return catalog.Default(), effort, nil
	}
	model, err := resolve(ref)
	if err != nil {
		return ai.Model{}, "", err
	}
	canonical := model.Provider + "/" + model.ID
	if validate && effort != "" && effort != "off" {
		if !catalog.ThinkingFor(canonical).Supports(effort) {
			return ai.Model{}, "", fmt.Errorf("$sup: %s does not expose thinking effort %q", canonical, effort)
		}
	}
	return model, effort, nil
}

// supEffectiveEffort resolves the SUP thinking effort and whether it is a
// claim that must be validated against the bound model.
//
// An explicit role effort is a claim, so it is validated. An unset role effort
// inherits the global default only when thinking is enabled globally; when it
// is disabled the provider never receives an effort, so validating one would
// refuse a configuration that runs fine. That mirrors the workspace run path,
// where the global default does not enable thinking by itself.
func supEffectiveEffort(settings config.Settings, roleEffort string) (effort string, validate bool) {
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
