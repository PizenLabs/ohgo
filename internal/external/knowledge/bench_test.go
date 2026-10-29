package knowledge

import (
	"strings"
	"testing"
)

func BenchmarkBoundTextOneMiB(b *testing.B) {
	text := strings.Repeat("abcdefghij", 100_000)
	b.ReportAllocs()
	b.SetBytes(int64(len(text)))
	for i := 0; i < b.N; i++ {
		if _, cut := BoundText(text, 32_000); !cut {
			b.Fatal("text was not bounded")
		}
	}
}

func BenchmarkEnvelopeTenThousandLines(b *testing.B) {
	body := strings.Repeat("untrusted line\n", 10_000)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if out := Envelope("capability: test\n", body); len(out) == 0 {
			b.Fatal("empty envelope")
		}
	}
}
