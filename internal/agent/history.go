package agent

import "strings"

// LastUserText returns the latest non-blank user prompt in a history, for
// /retry. Retrying re-sends the human's words, never the model's: the point
// is a second attempt at the same question, not an echo of the last answer.
func LastUserText(history []Message) (string, bool) {
	for i := len(history) - 1; i >= 0; i-- {
		if user, ok := history[i].(UserMessage); ok {
			if text := strings.TrimSpace(user.Text); text != "" {
				return text, true
			}
		}
	}
	return "", false
}
