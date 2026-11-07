package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/PizenLabs/ohgo/internal/control/budget"
	"github.com/PizenLabs/ohgo/internal/prompt"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/tui"
)

// sessionTitleMaxWords bounds a derived title to the table-cell contract. The
// prompt asks for 3–6 words; the bound is enforced here too so a verbose model
// answer cannot stretch the roster column.
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
// picker. The model is resolved from the session's own snapshot rather than
// the live configuration, so a /model switch cannot retitle an existing
// session through a different model. The request charges the root budget when
// one is supplied, exactly like compaction: background titling is still a
// model call and must not become an unmetered side channel.
func newSessionTitleGenerator(stream ai.StreamFn, resolve func(context.Context, string) (ai.Model, error), holders ...*budget.Holder) tui.SessionTitleGenerator {
	return func(ctx context.Context, modelRef, firstPrompt string) (string, error) {
		if stream == nil {
			return "", errors.New("session title: provider transport is unavailable")
		}
		if resolve == nil {
			return "", errors.New("session title: model resolver is unavailable")
		}
		firstPrompt = strings.TrimSpace(firstPrompt)
		if firstPrompt == "" {
			return "", errors.New("session title: first prompt is empty")
		}
		if len(holders) > 0 && holders[0] != nil {
			if err := holders[0].Get().Admit(budget.DimensionModelCalls); err != nil {
				return "", err
			}
		}
		model, err := resolve(ctx, modelRef)
		if err != nil {
			return "", fmt.Errorf("session title: resolve %q: %w", modelRef, err)
		}
		response, err := stream(ctx, ai.Request{
			Model: model,
			System: []ai.SystemPromptBlock{
				{Text: prompt.SessionTitleInstruction, Cache: true},
			},
			Messages: []ai.Message{ai.UserMessage{Content: []ai.ContentBlock{
				ai.TextContent{Text: firstPrompt},
			}}},
			MaxTokens: 64,
		})
		if err != nil {
			return "", fmt.Errorf("session title: %w", err)
		}
		msg, err := response.Result()
		if err != nil {
			return "", fmt.Errorf("session title: %w", err)
		}
		title := normalizeSessionTitle(msg.Text())
		if title == "" {
			return "", errors.New("session title: model returned no usable title")
		}
		return title, nil
	}
}

// normalizeSessionTitle converts a model answer into one bounded label.
//
// It keeps the first line, strips a label prefix, surrounding quotes, and
// trailing punctuation, then collapses whitespace and caps the word count.
// Navigation chrome must never carry a markdown block or a multi-line answer.
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
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return ""
	}
	if len(fields) > sessionTitleMaxWords {
		fields = fields[:sessionTitleMaxWords]
	}
	title := strings.Join(fields, " ")
	runes := []rune(title)
	if len(runes) > sessionTitleMaxRunes {
		title = strings.TrimSpace(string(runes[:sessionTitleMaxRunes]))
	}
	return title
}
