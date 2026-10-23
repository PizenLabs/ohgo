package tui

// Clipboard transport.
//
// The owned viewport copies logical text with OSC 52. No external process is
// spawned, no platform clipboard package is consulted, and no rendered ANSI is
// ever encoded. A terminal that does not implement OSC 52 ignores the sequence;
// the program reports nothing false about the clipboard's contents.
//
// tmux and screen sit between the application and the terminal. They forward
// OSC 52 only when configured to, so their escape wrappers are emitted when the
// environment names them. This changes packaging, not the payload.

import (
	"encoding/base64"
	"strings"
)

// osc52Prefix opens a system-clipboard set operation.
const osc52Prefix = "\x1b]52;c;"

// osc52Suffix terminates an OSC sequence.
const osc52Suffix = "\x07"

// OSC52Sequence encodes text as a plain OSC 52 clipboard sequence.
//
// Empty text yields no sequence rather than clearing the clipboard.
func OSC52Sequence(text string) string {
	if text == "" {
		return ""
	}
	return osc52Prefix + base64.StdEncoding.EncodeToString([]byte(text)) + osc52Suffix
}

// OSC52SequenceFor wraps the sequence for the multiplexer in the path.
//
// tmux requires a DCS passthrough wrapper and `allow-passthrough on`; screen
// uses a DCS wrapper split into 76-byte chunks. Both are protocol packaging
// only: the base64 payload is the same logical text.
func OSC52SequenceFor(text string, tmux bool, term string) string {
	sequence := OSC52Sequence(text)
	if sequence == "" {
		return ""
	}
	if tmux {
		return "\x1bPtmux;\x1b" + sequence + "\x1b\\"
	}
	if strings.HasPrefix(strings.ToLower(term), "screen") {
		payload := strings.TrimSuffix(strings.TrimPrefix(sequence, osc52Prefix), osc52Suffix)
		var chunks []string
		for len(payload) > 76 {
			chunks = append(chunks, payload[:76])
			payload = payload[76:]
		}
		if payload != "" {
			chunks = append(chunks, payload)
		}
		return "\x1bP" + osc52Prefix + strings.Join(chunks, "\x1b\\\x1bP") + osc52Suffix + "\x1b\\"
	}
	return sequence
}

// ClipboardAvailable reports whether the environment can plausibly accept an
// OSC 52 write. A dumb terminal has no clipboard protocol; an unset TERM is
// treated as unavailable so a redirected run never invents a terminal.
//
// The check is deliberately conservative. A terminal that reports a real TERM
// but ignores OSC 52 silently does nothing, which is the defined unsupported
// behaviour: no fallback process and no false success claim.
func ClipboardAvailable(term string) bool {
	term = strings.TrimSpace(strings.ToLower(term))
	if term == "" || term == "dumb" {
		return false
	}
	return true
}
