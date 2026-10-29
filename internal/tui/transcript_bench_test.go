package tui

// Performance contracts for the owned viewport.
//
// These benchmarks are measurements, not assertions: they report p50/p95/p99,
// allocations, bytes, and the number of block projections the operation
// actually rendered. A viewport operation must report zero rendered blocks no
// matter how much history exists; a streaming delta or expansion must report
// one.

import (
	"fmt"
	"sort"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func benchBase(n int) State {
	state := syntheticDocument(n, 120)
	state.Apply(TerminalResized{Width: 120, Height: 40})
	return state
}

func reportLatencies(b *testing.B, samples []time.Duration) {
	b.Helper()
	if len(samples) == 0 {
		return
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	at := func(percent int) time.Duration {
		i := percent * len(samples) / 100
		if i >= len(samples) {
			i = len(samples) - 1
		}
		return samples[i]
	}
	b.ReportMetric(float64(at(50).Nanoseconds()), "p50-ns")
	b.ReportMetric(float64(at(95).Nanoseconds()), "p95-ns")
	b.ReportMetric(float64(at(99).Nanoseconds()), "p99-ns")
}

func BenchmarkViewport(b *testing.B) {
	for _, n := range []int{100, 1000, 10000} {
		n := n
		b.Run(fmt.Sprintf("blocks=%d", n), func(b *testing.B) {
			runViewportBench(b, n)
		})
	}
}

func runViewportBench(b *testing.B, n int) {
	b.Run("scroll", func(b *testing.B) {
		state := benchBase(n)
		index := &TranscriptLayout{}
		index.Refresh(&state, ComputeLayout(state))
		tracker := &ViewportTracker{}
		tracker.Update(true, 0, 0, transcriptBudget(state, ComputeLayout(state)), index)
		state.Apply(ScrollMoved{Delta: 5})
		samples := make([]time.Duration, 0, b.N)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			delta := 3
			if i%2 == 1 {
				delta = -3
			}
			start := time.Now()
			state.Apply(ScrollMoved{Delta: delta})
			index.Refresh(&state, ComputeLayout(state))
			tracker.Update(state.Follow, state.scrollIntent, state.ScrollSeq, transcriptBudget(state, ComputeLayout(state)), index)
			samples = append(samples, time.Since(start))
		}
		b.StopTimer()
		reportLatencies(b, samples)
		b.ReportMetric(float64(index.LastRendered), "rendered/op")
	})

	b.Run("expand-middle", func(b *testing.B) {
		state := benchBase(n)
		index := &TranscriptLayout{}
		index.Refresh(&state, ComputeLayout(state))
		tracker := &ViewportTracker{}
		tracker.Update(true, 0, 0, transcriptBudget(state, ComputeLayout(state)), index)
		id := middleCollapsedTool(state)
		if id == "" {
			b.Skip("fixture has no collapsed tool")
		}
		samples := make([]time.Duration, 0, b.N)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			start := time.Now()
			state.Apply(ToolToggled{ID: id})
			layout := ComputeLayout(state)
			index.Refresh(&state, layout)
			tracker.Update(state.Follow, state.scrollIntent, state.ScrollSeq, transcriptBudget(state, layout), index)
			index.Rows(tracker.Value().Offset, tracker.Value().Height)
			samples = append(samples, time.Since(start))
		}
		b.StopTimer()
		reportLatencies(b, samples)
		b.ReportMetric(float64(index.LastRendered), "rendered/op")
	})

	b.Run("collapse-middle", func(b *testing.B) {
		state := benchBase(n)
		index := &TranscriptLayout{}
		index.Refresh(&state, ComputeLayout(state))
		tracker := &ViewportTracker{}
		tracker.Update(true, 0, 0, transcriptBudget(state, ComputeLayout(state)), index)
		id := middleCollapsedTool(state)
		if id == "" {
			b.Skip("fixture has no collapsed tool")
		}
		state.Apply(ToolToggled{ID: id}) // start expanded
		index.Refresh(&state, ComputeLayout(state))
		samples := make([]time.Duration, 0, b.N)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			start := time.Now()
			state.Apply(ToolToggled{ID: id})
			layout := ComputeLayout(state)
			index.Refresh(&state, layout)
			tracker.Update(state.Follow, state.scrollIntent, state.ScrollSeq, transcriptBudget(state, layout), index)
			index.Rows(tracker.Value().Offset, tracker.Value().Height)
			samples = append(samples, time.Since(start))
		}
		b.StopTimer()
		reportLatencies(b, samples)
		b.ReportMetric(float64(index.LastRendered), "rendered/op")
	})

	b.Run("resize", func(b *testing.B) {
		state := benchBase(n)
		index := &TranscriptLayout{}
		index.Refresh(&state, ComputeLayout(state))
		tracker := &ViewportTracker{}
		tracker.Update(true, 0, 0, transcriptBudget(state, ComputeLayout(state)), index)
		samples := make([]time.Duration, 0, b.N)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			width := 80
			if i%2 == 1 {
				width = 120
			}
			state.Width = width
			start := time.Now()
			layout := ComputeLayout(state)
			index.Refresh(&state, layout)
			tracker.Update(state.Follow, state.scrollIntent, state.ScrollSeq, transcriptBudget(state, layout), index)
			samples = append(samples, time.Since(start))
		}
		b.StopTimer()
		reportLatencies(b, samples)
		b.ReportMetric(float64(index.LastRendered), "rendered/op")
	})

	b.Run("hit-test", func(b *testing.B) {
		state := benchBase(n)
		index := &TranscriptLayout{}
		index.Refresh(&state, ComputeLayout(state))
		tracker := &ViewportTracker{}
		vp := tracker.Update(true, 0, 0, transcriptBudget(state, ComputeLayout(state)), index)
		rows := index.Rows(vp.Offset, vp.Height)
		if len(rows) == 0 {
			b.Skip("no visible rows")
		}
		b.ReportAllocs()
		samples := make([]time.Duration, 0, b.N)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			docRow := vp.Offset + i%vp.Height
			start := time.Now()
			entry, local, ok := index.BlockForRow(docRow)
			if ok {
				_, _ = index.BlockRow(entry.BlockIndex, local)
			}
			samples = append(samples, time.Since(start))
		}
		b.StopTimer()
		reportLatencies(b, samples)
	})

	b.Run("selection", func(b *testing.B) {
		state := benchBase(n)
		index := &TranscriptLayout{}
		index.Refresh(&state, ComputeLayout(state))
		tracker := &ViewportTracker{}
		vp := tracker.Update(true, 0, 0, transcriptBudget(state, ComputeLayout(state)), index)
		rows := index.Rows(vp.Offset, vp.Height)
		if len(rows) < 3 {
			b.Skip("not enough visible rows")
		}
		sel := &Selection{
			Start: Position{BlockID: rows[0].BlockID, Row: rows[0].Row, Column: 0},
			End:   Position{BlockID: rows[2].BlockID, Row: rows[2].Row, Column: rows[2].Width},
		}
		b.ReportAllocs()
		samples := make([]time.Duration, 0, b.N)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			start := time.Now()
			_, _ = CopySelection(index, sel)
			samples = append(samples, time.Since(start))
		}
		b.StopTimer()
		reportLatencies(b, samples)
	})

	b.Run("stream-delta", func(b *testing.B) {
		state := benchBase(n)
		// Find or create an open assistant block at the live edge.
		state.Apply(AssistantDelta{Text: "streaming start"})
		index := &TranscriptLayout{}
		index.Refresh(&state, ComputeLayout(state))
		tracker := &ViewportTracker{}
		tracker.Update(true, 0, 0, transcriptBudget(state, ComputeLayout(state)), index)
		samples := make([]time.Duration, 0, b.N)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			start := time.Now()
			state.Apply(AssistantDelta{Text: " chunk"})
			layout := ComputeLayout(state)
			index.Refresh(&state, layout)
			tracker.Update(state.Follow, state.scrollIntent, state.ScrollSeq, transcriptBudget(state, layout), index)
			samples = append(samples, time.Since(start))
		}
		b.StopTimer()
		reportLatencies(b, samples)
		b.ReportMetric(float64(index.LastRendered), "rendered/op")
	})

	b.Run("append-block", func(b *testing.B) {
		state := benchBase(n)
		index := &TranscriptLayout{}
		index.Refresh(&state, ComputeLayout(state))
		tracker := &ViewportTracker{}
		tracker.Update(true, 0, 0, transcriptBudget(state, ComputeLayout(state)), index)
		samples := make([]time.Duration, 0, b.N)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			start := time.Now()
			state.Apply(Notice{Text: fmt.Sprintf("appended %d", i)})
			layout := ComputeLayout(state)
			index.Refresh(&state, layout)
			tracker.Update(state.Follow, state.scrollIntent, state.ScrollSeq, transcriptBudget(state, layout), index)
			samples = append(samples, time.Since(start))
		}
		b.StopTimer()
		reportLatencies(b, samples)
		b.ReportMetric(float64(index.LastRendered), "rendered/op")
	})
}

// BenchmarkFullTranscriptRender is the old ownership model: project every
// block every frame. It exists only as the comparison point for the retained
// viewport metrics above.
func BenchmarkFullTranscriptRender(b *testing.B) {
	for _, n := range []int{100, 1000, 10000} {
		state := benchBase(n)
		layout := ComputeLayout(state)
		pal := PaletteFor(state.Theme)
		b.Run(fmt.Sprintf("blocks=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = renderTranscriptBody(state, pal, layout)
			}
		})
	}
}

// BenchmarkProgramFrame measures a complete frame after a wheel tick: layout
// refresh, viewport resolution, and chrome rendering.
func BenchmarkProgramFrame(b *testing.B) {
	for _, n := range []int{100, 1000, 10000} {
		state := benchBase(n)
		p := NewProgram(nil, "bench", 0)
		p.state = state
		p.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
		b.Run(fmt.Sprintf("blocks=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			samples := make([]time.Duration, 0, b.N)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				p.state.Apply(ScrollMoved{Delta: 3})
				if i%2 == 1 {
					p.state.Apply(ScrollMoved{Delta: -3})
				}
				start := time.Now()
				_ = p.View()
				samples = append(samples, time.Since(start))
			}
			b.StopTimer()
			reportLatencies(b, samples)
		})
	}
}

func middleCollapsedTool(state State) string {
	target := len(state.Entries) / 2
	if target < 0 {
		target = 0
	}
	for offset := 0; offset < len(state.Entries); offset++ {
		for _, i := range []int{target + offset, target - offset} {
			if i < 0 || i >= len(state.Entries) {
				continue
			}
			entry := state.Entries[i]
			if entry.Kind == EntryTool && entry.Tool != nil && !entry.Tool.Expanded && toolExpandable(entry.Tool) {
				return entry.Tool.ID
			}
		}
	}
	return ""
}
