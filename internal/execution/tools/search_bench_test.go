package tools

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/budget"
	"github.com/PizenLabs/ohgo/internal/external/search"
)

func BenchmarkSearchPublishBoundedPage(b *testing.B) {
	hits := make([]search.Hit, 0, search.MaxResultsLimit)
	snippet := strings.Repeat("bounded snippet ", 80)
	for i := 0; i < search.MaxResultsLimit; i++ {
		hits = append(hits, search.Hit{
			Title:       "Result",
			Source:      "example.com",
			URL:         "https://example.com/document",
			Snippet:     snippet,
			RetrievedAt: time.Unix(1, 0).UTC(),
		})
	}
	response := search.Response{Hits: hits, Provider: "brave", Bytes: 8192, RetrievedAt: time.Unix(1, 0).UTC()}
	tool := &SearchTool{}
	ctx := agent.Context{Ctx: context.Background(), Budget: budget.New(budget.Limits{ExternalBytes: 1 << 20})}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		result := tool.publish(ctx, map[string]any{}, "request", "brave", "query", response)
		if result.IsError || len(result.Text) == 0 {
			b.Fatal("publish failed")
		}
	}
}
