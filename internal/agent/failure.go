package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// CallSignature returns the stable identity of one tool invocation.
//
// It is derived from the tool name and the canonical JSON encoding of the
// arguments, so a model that repeats the same call with a new call id is
// recognised as repeating the same proposal. It is an identity, not an
// authorization: a refusal keyed by it only narrows what may execute again.
func CallSignature(tool string, args map[string]any) string {
	normalized := args
	if normalized == nil {
		normalized = map[string]any{}
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		// Arguments decoded from a provider stream are JSON, so this is a
		// programming error rather than a model input. Keep the identity
		// distinct without inventing an order-dependent map rendering.
		return tool + "\x00unencodable"
	}
	sum := sha256.Sum256(append([]byte(tool+"\x00"), encoded...))
	return tool + "\x00" + hex.EncodeToString(sum[:12])
}

// CallTarget returns the principal coordinate a call names.
//
// Two invocations of one capability against the same target are related even
// when their secondary arguments differ: re-reading a file from a different
// offset observes the same path. The target is how a later success resolves an
// earlier failure for that coordinate. It is audit metadata, never authority.
func CallTarget(tool string, args map[string]any) string {
	if args == nil {
		return tool
	}
	for _, key := range []string{"path", "file", "file_path", "target"} {
		if value := stringArg(args, key); value != "" {
			return tool + "\x00path:" + value
		}
	}
	if value := stringArg(args, "url"); value != "" {
		return tool + "\x00url:" + value
	}
	if value := stringArg(args, "command"); value != "" {
		return tool + "\x00command:" + value
	}
	if value := stringArg(args, "capability"); value != "" {
		return tool + "\x00capability:" + value
	}
	if server := stringArg(args, "server"); server != "" {
		if name := stringArg(args, "tool"); name != "" {
			return tool + "\x00server:" + server + ":" + name
		}
		return tool + "\x00server:" + server
	}
	if value := stringArg(args, "prompt"); value != "" {
		return tool + "\x00prompt:" + value
	}
	if value := stringArg(args, "pattern"); value != "" {
		return tool + "\x00pattern:" + value
	}
	return tool
}

// stringArg reads a trimmed string argument without panicking on another type.
func stringArg(args map[string]any, key string) string {
	value, _ := args[key].(string)
	return strings.TrimSpace(value)
}
