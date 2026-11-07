package main

import (
	"github.com/PizenLabs/ohgo/internal/continuity/session"
	"github.com/PizenLabs/ohgo/internal/promptopt"
)

// localSessionTitle derives a session's navigation label from the user's
// prompt. promptopt.SanitizeTitle is the single source of truth: it strips
// conversational noise, preserves technical tokens, and bounds the result at a
// word boundary. No model, network, or clock is involved.
func localSessionTitle(prompt string) string {
	return promptopt.SanitizeTitle(prompt)
}

// applyLocalSessionTitle persists the deterministic label derived from prompt
// when the session still has no assigned title. It runs synchronously, so the
// title is durable before the turn reaches the provider and costs no tokens.
//
// An empty or all-noise prompt leaves the session untitled; an explicit title
// is never overwritten. The returned value is the newly stored title, or ""
// when nothing changed.
func applyLocalSessionTitle(store *session.Store, sess *session.Session, prompt string) string {
	if store == nil || sess == nil || session.TitleAssigned(sess.Title, sess.Model) {
		return ""
	}
	title := localSessionTitle(prompt)
	if title == "" {
		return ""
	}
	if err := store.SetTitle(sess.ID, title); err != nil {
		return ""
	}
	sess.Title = title
	return title
}
