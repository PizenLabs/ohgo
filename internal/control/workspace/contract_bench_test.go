package workspace

import "testing"

// BenchmarkBuildIntentContract measures the deterministic extraction cost. It
// runs on every turn, including read-only turns, so it must stay small.
func BenchmarkBuildIntentContract(b *testing.B) {
	cases := []struct {
		name string
		text string
	}{
		{"read", "Explain what GrepTool returns."},
		{"mutation", "Fix GrepTool in tools/grep.go so `go test ./tools` passes."},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = BuildIntentContract(tc.text)
			}
		})
	}
}
