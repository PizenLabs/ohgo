package main

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/PizenLabs/ohgo/agent"
)

// supportSurface is the ephemeral $sup conversation.
//
// $sup is a capability of ASK, not a workspace (§10.1): the surface does not
// create an authority and does not change the workspace the session runs
// under. Its messages live only here — they are never appended to the main
// history and never written to the session store — so a support exchange
// cannot pollute the main task context (§10.2, I17).
//
// The loop is built lazily on first use, so a session that never calls $sup
// pays nothing for it.
type supportSurface struct {
	build func() (*agent.Loop, error)

	once sync.Once
	loop *agent.Loop
	err  error

	mu   sync.Mutex
	msgs []agent.Message
}

// newSupportSurface wraps a lazy loop builder.
func newSupportSurface(build func() (*agent.Loop, error)) *supportSurface {
	return &supportSurface{build: build}
}

// ask runs one support question, continuing the ephemeral conversation.
//
// The returned answer is the concatenated assistant text. Nothing is persisted
// and no session state is touched.
func (s *supportSurface) ask(ctx context.Context, question string) (string, error) {
	if s == nil {
		return "", fmt.Errorf("support is unavailable")
	}
	s.once.Do(func() { s.loop, s.err = s.build() })
	if s.err != nil {
		return "", s.err
	}

	s.mu.Lock()
	request := append(append([]agent.Message(nil), s.msgs...), agent.UserMessage{Text: question})
	s.mu.Unlock()

	turns, err := s.loop.Run(ctx, request)
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
	if h.support == nil {
		return "", fmt.Errorf("support is unavailable")
	}
	return h.support.ask(ctx, question)
}

// CloseSupport discards the ephemeral conversation without touching the main
// session.
func (h *harness) CloseSupport() {
	if h.support != nil {
		h.support.close()
	}
}
