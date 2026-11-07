package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/PizenLabs/ohgo/internal/config"
	"github.com/PizenLabs/ohgo/internal/control/budget"
	"github.com/PizenLabs/ohgo/internal/prompt"
	"github.com/PizenLabs/ohgo/internal/promptopt"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/tui"
)

// sessionTitleMaxWords bounds a derived title to the table-cell contract. The
// prompt asks for 2–4 words; the bound leaves a little headroom so a slightly
// verbose answer is truncated rather than rejected.
const sessionTitleMaxWords = 6

// sessionTitleMaxRunes bounds a single pathological token.
const sessionTitleMaxRunes = 80

// newSessionTitleGenerator builds the automatic session-titling seam over a
// provider transport.
//
// The stream and resolver are parameters rather than a registry so a test can
// substitute stubs and assert on the request the titler actually sends, the
// same shape as newSummarizer. The resolver must handle every model the
// running session can actually use — including live-discovered ids such as
// "openrouter/stealth/crouton-alpha" that the static catalog never saw — or
// titling silently disappears for exactly the models chosen from the live
// picker.
//
// Titling is a cheap, latency-sensitive job, so it prefers the ASK role's
// model over the session's execution model: a title request is not execution
// work and must not consume the BUILD/FUZZ budget. When no ASK role model is
// configured, modelRef (the session's own snapshot) remains the fallback so a
// later /model switch still cannot silently retitle an existing session
// through a different model. The request charges the root budget when one is
// supplied, exactly like compaction: background titling is still a model call
// and must not become an unmetered side channel.
//
// A failed, timed-out, or empty model answer returns the deterministic
// promptopt label when one can be derived, so titling never depends on provider
// availability. Shutdown cancellation stays an error because the UI that would
// persist the label is already closing.
func newSessionTitleGenerator(stream ai.StreamFn, resolve func(context.Context, string) (ai.Model, error), roleModel func() string, holders ...*budget.Holder) tui.SessionTitleGenerator {
	return func(ctx context.Context, modelRef, firstPrompt string) (string, error) {
		firstPrompt = strings.TrimSpace(firstPrompt)
		if firstPrompt == "" {
			return "", errors.New("session title: first prompt is empty")
		}
		// The local label is the primary fallback: it costs nothing, uses the
		// same word-boundary rules as the pending roster, and is preferable to
		// leaving the session untitled when the ASK model is slow or absent.
		fallback := promptopt.SanitizeTitle(firstPrompt)
		if stream == nil {
			return titleFallback(fallback, errors.New("session title: provider transport is unavailable"))
		}
		if resolve == nil {
			return titleFallback(fallback, errors.New("session title: model resolver is unavailable"))
		}
		if len(holders) > 0 && holders[0] != nil {
			if err := holders[0].Get().Admit(budget.DimensionModelCalls); err != nil {
				return titleFallback(fallback, err)
			}
		}
		ref := strings.TrimSpace(modelRef)
		if roleModel != nil {
			if ask := strings.TrimSpace(roleModel()); ask != "" {
				ref = ask
			}
		}
		model, err := resolve(ctx, ref)
		if err != nil {
			return titleFallback(fallback, fmt.Errorf("session title: resolve %q: %w", ref, err))
		}
		response, err := stream(ctx, ai.Request{
			Model: model,
			System: []ai.SystemPromptBlock{
				{Text: prompt.SessionTitleInstruction, Cache: true},
			},
			Messages: []ai.Message{ai.UserMessage{Content: []ai.ContentBlock{
				ai.TextContent{Text: firstPrompt},
			}}},
			// The prompt asks for 2-4 words; a small output budget keeps a
			// provider from reporting a length truncation for a one-line label.
			MaxTokens: 25,
		})
		if err != nil {
			return titleStreamFailure(ctx, fallback, fmt.Errorf("session title: %w", err))
		}
		msg, err := response.Result()
		if err != nil {
			return titleStreamFailure(ctx, fallback, fmt.Errorf("session title: %w", err))
		}
		title := normalizeSessionTitle(msg.Text())
		if title == "" {
			if fallback != "" {
				return fallback, nil
			}
			return "", errors.New("session title: model returned no usable title")
		}
		return title, nil
	}
}

// titleFallback prefers the deterministic local label over a failed model call.
// Only a prompt with no usable words is allowed to surface the provider error.
func titleFallback(fallback string, cause error) (string, error) {
	if fallback != "" {
		return fallback, nil
	}
	return "", cause
}

// titleStreamFailure keeps shutdown cancellation an error: the UI is closing,
// so persisting a fallback label would outlive the surface that requested it.
// A deadline or transport failure still gets the local label.
func titleStreamFailure(ctx context.Context, fallback string, cause error) (string, error) {
	if errors.Is(ctx.Err(), context.Canceled) {
		return "", cause
	}
	return titleFallback(fallback, cause)
}

// askRoleModelRef returns the model bound to the ASK role, or "" when no role
// model is configured. It is the one reader titling uses: the control plane's
// role assignment and the background title pass must agree on which model a
// title request spends, and the ASK role is the cheap reasoning model.
//
// The legacy Models map is included because RoleModel treats it as the same
// assignment layer; a file that predates Roles keeps working.
func askRoleModelRef(settings config.Settings) string {
	ref, _, source := settings.RoleModel(string(config.RoleAsk))
	if source == "builtin" {
		return ""
	}
	return strings.TrimSpace(ref)
}

// normalizeSessionTitle converts a model answer into one bounded label.
//
// It keeps the first line, strips a label prefix, surrounding quotes, and
// trailing punctuation, then collapses whitespace. The shared truncator cuts
// only between words and drops a trailing article or preposition, so navigation
// chrome never carries a markdown block, a half word, or a dangling "the".
func normalizeSessionTitle(raw string) string {
	line := strings.TrimSpace(raw)
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = strings.TrimSpace(line[:i])
	}
	lower := strings.ToLower(line)
	for _, prefix := range []string{"session title:", "title:"} {
		if strings.HasPrefix(lower, prefix) {
			line = strings.TrimSpace(line[len(prefix):])
			break
		}
	}
	// Models occasionally ignore the no-markdown rule; a leading heading
	// marker is decoration, not part of the title.
	for strings.HasPrefix(line, "#") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "#"))
	}
	line = strings.Trim(line, "\"'`“”‘’")
	line = strings.TrimSpace(line)
	line = strings.TrimRight(line, ".,:;!?—-–")
	line = strings.TrimSpace(line)
	line = strings.Trim(line, "\"'`“”‘’")
	line = strings.TrimSpace(line)
	return promptopt.TruncateTitle(line, sessionTitleMaxWords, sessionTitleMaxRunes)
}
