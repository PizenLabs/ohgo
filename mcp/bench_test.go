package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

func BenchmarkParseToolResultBounded(b *testing.B) {
	items := make([]any, 0, 32)
	text := strings.Repeat("x", 12_000)
	for i := 0; i < 32; i++ {
		items = append(items, map[string]any{"type": "text", "text": text})
	}
	raw, err := json.Marshal(map[string]any{"content": items})
	if err != nil {
		b.Fatal(err)
	}
	cfg := ServerConfig{MaxItems: 32, MaxTextChars: 12_000}
	b.ReportAllocs()
	b.SetBytes(int64(len(raw)))
	for i := 0; i < b.N; i++ {
		result, err := parseToolResult(raw, len(raw), cfg)
		if err != nil {
			b.Fatal(err)
		}
		if len([]rune(result.Text)) > 12_000+32 {
			b.Fatalf("result was not bounded: %d runes", len([]rune(result.Text)))
		}
	}
}
