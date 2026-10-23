package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// The layout must be exactly the old projection's row sequence. If this
// diverges, scrolling slices a different document than the renderer paints.
func TestLayoutRowsMatchRenderedTranscriptRows(t *testing.T) {
	s := syntheticDocument(60, 72)
	s.Apply(TerminalResized{Width: 72, Height: 24})
	want := splitLines(renderTranscriptBody(s, PaletteFor(s.Theme), ComputeLayout(s)))
	l := layoutFor(s)
	got := l.AllRows()
	if len(got) != len(want) {
		t.Fatalf("layout rows = %d, rendered rows = %d", len(got), len(want))
	}
	for i := range want {
		if ansi.Strip(got[i].Styled) != ansi.Strip(want[i]) {
			t.Fatalf("row %d = %q, want %q", i, ansi.Strip(got[i].Styled), ansi.Strip(want[i]))
		}
	}
}

// A logical line and a visual row are different units. A line one cell past
// the width must wrap; a line exactly at the width must not.
func TestWrapBoundaryIsCellExact(t *testing.T) {
	cases := []struct {
		name  string
		text  string
		width int
		want  int
	}{
		{"exact width stays one row", strings.Repeat("x", 40), 40, 1},
		{"one cell over wraps", strings.Repeat("x", 41), 40, 2},
		{"two CJK glyphs fit four cells", "你好", 4, 1},
		{"wide glyphs wrap on cells not runes", "你好界", 4, 2},
		{"combining mark rides its base", "e\u0301", 1, 1},
		{"empty final line is trimmed like the projection", "a\n", 20, 1},
		{"blank middle line is a row", "a\n\nb", 20, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := New()
			s.Apply(Notice{Text: tc.text})
			layout := ComputeLayout(s)
			// Exercise the requested content width directly: ComputeLayout's
			// tier rules would otherwise turn a 40-cell request into 41 for a
			// narrow terminal with a two-column inset.
			layout.Width = tc.width + 2
			layout.ContentWidth = tc.width
			layout.Mode = LayoutWide
			l := &TranscriptLayout{}
			l.Refresh(&s, layout)
			// BlockHeight already excludes the final separator row; it is the
			// wrapped body's visual height.
			got := l.BlockHeight(0)
			if got != tc.want {
				t.Fatalf("height = %d rows, want %d (%q at %d cells)", got, tc.want, tc.text, tc.width)
			}
		})
	}
}

// A regex/parser boundary may never make the last row of one block inherit the
// first row of the next. Each row must map to exactly one block and one local
// row.
func TestLineBoundaryNeverLeaksAcrossBlocks(t *testing.T) {
	for _, text := range []string{
		"line A\nline B",
		"line A\n",
		"line A\r\nline B",
		"line ending exactly at width: 0123456789",
		"",
		"\x1b[31mstyled line A\x1b[0m\nline B",
	} {
		t.Run(strings.ReplaceAll(text, "\n", `\n`), func(t *testing.T) {
			s := New()
			s.Apply(TerminalResized{Width: 32, Height: 20})
			s.Apply(Notice{Text: text})
			s.Apply(Notice{Text: "second block"})
			l := layoutFor(s)

			firstID := blockIDAt(s.Entries, 0)
			secondID := blockIDAt(s.Entries, 1)
			entries := l.Entries()
			if len(entries) != 2 {
				t.Fatalf("layout entries = %d, want 2", len(entries))
			}
			if entries[0].BlockID == entries[1].BlockID {
				t.Fatal("two blocks share an identity")
			}
			rows := l.AllRows()
			for _, row := range rows {
				if row.BlockID != firstID && row.BlockID != secondID {
					t.Fatalf("row %d has unknown block %q", row.DocRow, row.BlockID)
				}
				if row.BlockID == firstID && row.BlockIndex != 0 {
					t.Fatalf("row %d maps block index %d, want 0", row.DocRow, row.BlockIndex)
				}
				if row.BlockID == secondID && row.BlockIndex != 1 {
					t.Fatalf("row %d maps block index %d, want 1", row.DocRow, row.BlockIndex)
				}
			}
			// The second block's text exists exactly once and only on rows
			// owned by the second block.
			for _, row := range rows {
				if strings.Contains(row.Plain(), "second block") && row.BlockID != secondID {
					t.Fatalf("second block text leaked into block %q at row %d", row.BlockID, row.DocRow)
				}
			}
		})
	}
}

// Trailing newlines are terminators, not phantom visual rows. This is the
// line-boundary class where a naive split adds a row that the renderer never
// paints, desynchronising the viewport from the screen.
func TestTrailingNewlinesDoNotAddRows(t *testing.T) {
	for _, text := range []string{"alpha", "alpha\n", "alpha\n\n\n"} {
		s := New()
		s.Apply(TerminalResized{Width: 40, Height: 20})
		s.Apply(Notice{Text: text})
		l := layoutFor(s)
		if got := l.TotalRows(); got != 1 {
			t.Fatalf("text %q has %d rows, want 1", text, got)
		}
	}
}

// ANSI styling is zero-width and cannot inflate geometry; stripping it must
// leave the next row unaffected.
func TestStyledContentDoesNotChangeGeometry(t *testing.T) {
	plain := New()
	plain.Apply(TerminalResized{Width: 30, Height: 20})
	plain.Apply(Notice{Text: "alpha\nbeta"})

	styled := New()
	styled.Apply(TerminalResized{Width: 30, Height: 20})
	styled.Apply(Notice{Text: "\x1b[1;31malpha\x1b[0m\n\x1b[32mbeta\x1b[0m"})

	lp, ls := layoutFor(plain), layoutFor(styled)
	if lp.TotalRows() != ls.TotalRows() {
		t.Fatalf("styled rows = %d, plain rows = %d", ls.TotalRows(), lp.TotalRows())
	}
	for _, row := range ls.AllRows() {
		if strings.Contains(row.Plain(), "\x1b") {
			t.Fatalf("plain projection still contains an escape: %q", row.Plain())
		}
	}
	if entry, _, _ := ls.BlockForRow(1); entry.BlockIndex != 0 {
		t.Fatalf("row 1 mapped to block %d, want 0", entry.BlockIndex)
	}
}

// Layout is deterministic: the same document and width produce identical rows.
func TestLayoutIsDeterministic(t *testing.T) {
	s := syntheticDocument(40, 64)
	a := layoutFor(s)
	b := layoutFor(s)
	if a.TotalRows() != b.TotalRows() {
		t.Fatalf("height %d != %d", a.TotalRows(), b.TotalRows())
	}
	ra, rb := a.AllRows(), b.AllRows()
	for i := range ra {
		if ra[i].Styled != rb[i].Styled || ra[i].BlockID != rb[i].BlockID || ra[i].Row != rb[i].Row {
			t.Fatalf("row %d differs between builds", i)
		}
	}
}

// Ordinary viewport interaction must not re-render untouched history. A
// streaming delta re-renders one block; a scroll re-renders none.
func TestIncrementalRefreshRendersOnlyDirtyBlocks(t *testing.T) {
	s := syntheticDocument(200, 80)
	l := &TranscriptLayout{}
	l.Refresh(&s, ComputeLayout(s))

	l.Refresh(&s, ComputeLayout(s))
	if l.LastRendered != 0 {
		t.Fatalf("unchanged refresh rendered %d blocks, want 0", l.LastRendered)
	}
	l.LastRendered = 0
	l.Rows(0, 20)
	if l.LastRendered != 0 {
		t.Fatalf("scrolling rendered %d blocks, want 0", l.LastRendered)
	}

	before := l.TotalRows()
	s.Apply(AssistantDelta{Text: " streamed"})
	l.Refresh(&s, ComputeLayout(s))
	if l.LastRendered != 1 {
		t.Fatalf("streaming rendered %d blocks, want 1", l.LastRendered)
	}
	if l.TotalRows() < before {
		t.Fatalf("streaming shrank the document: %d -> %d", before, l.TotalRows())
	}

	// Expand a tool block in the middle: exactly one projection changes.
	id := ""
	for _, entry := range s.Entries {
		if entry.Kind == EntryTool && entry.Tool != nil && !entry.Tool.Expanded {
			id = entry.Tool.ID
			break
		}
	}
	if id == "" {
		t.Fatal("fixture has no collapsed tool block")
	}
	s.Apply(ToolToggled{ID: id})
	l.Refresh(&s, ComputeLayout(s))
	if l.LastRendered != 1 {
		t.Fatalf("expanding rendered %d blocks, want 1", l.LastRendered)
	}
}

// Expansion is local to the block: first, middle, last, and consecutive
// expansions change only the toggled block's height.
func TestExpansionAtDocumentEdgesChangesOnlyThatBlock(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 80, Height: 30})
	for i := 0; i < 9; i++ {
		id := fmt.Sprintf("t%d", i)
		s.Apply(ToolStarted{ID: id, Name: "read", Summary: "read " + id})
		s.Apply(ToolFinished{ID: id, Text: "one\ntwo\nthree", Summary: "3 lines"})
	}
	index := &TranscriptLayout{}
	index.Refresh(&s, ComputeLayout(s))
	before := index.Entries()
	heights := func() []int {
		out := make([]int, len(before))
		for i := range before {
			out[i] = index.BlockHeight(i)
		}
		return out
	}
	base := heights()
	for _, target := range []int{0, 4, 8, 7} { // first, middle, last, then consecutive
		s.Apply(ToolToggled{ID: fmt.Sprintf("t%d", target)})
		index.Refresh(&s, ComputeLayout(s))
		after := heights()
		for i := range after {
			wantChanged := i == target
			changed := after[i] != base[i]
			if changed != wantChanged {
				t.Fatalf("expand %d: block %d height %d -> %d, changed=%v want %v",
					target, i, base[i], after[i], changed, wantChanged)
			}
		}
		base = after
	}
}

// The Fenwick index must agree with a naive prefix sum for every height,
// including zero-height blocks.
func TestFenwickRowIndexMatchesNaivePrefix(t *testing.T) {
	heights := []int{0, 3, 1, 0, 7, 2, 0, 0, 5, 4}
	f := newFenwick(len(heights))
	f.reset(heights)
	total := 0
	for _, h := range heights {
		total += h
	}
	if f.total() != total {
		t.Fatalf("total = %d, want %d", f.total(), total)
	}
	prefix := 0
	for i, h := range heights {
		if f.prefix(i) != prefix {
			t.Fatalf("prefix(%d) = %d, want %d", i, f.prefix(i), prefix)
		}
		for local := 0; local < h; local++ {
			block, gotLocal := f.find(prefix + local)
			if block != i || gotLocal != local {
				t.Fatalf("find(%d) = (%d,%d), want (%d,%d)", prefix+local, block, gotLocal, i, local)
			}
		}
		prefix += h
	}
}
