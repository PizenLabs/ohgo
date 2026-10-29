package tools

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
)

func BenchmarkExtractLinksBounded(b *testing.B) {
	var html strings.Builder
	for i := 0; i < 1000; i++ {
		fmt.Fprintf(&html, `<a href="/page/%d">Link %d</a>`, i, i)
	}
	base, _ := url.Parse("https://example.com/")
	page := html.String()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		links := extractLinks(page, base, 20, func(string) bool { return true })
		if len(links) != 20 {
			b.Fatalf("links = %d, want the bound", len(links))
		}
	}
}
