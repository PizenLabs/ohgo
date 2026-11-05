package tui

// Transcript layout: the geometry authority for the owned viewport.
//
// A block's visual height is derived from its rendered projection at a given
// content width, then stored as a row count. Rendering the whole document on
// every frame is not required: each block is rendered at most once per
// revision/width, and the visible region is sliced from cached rows. Regex is
// never consulted for geometry; the same wrap functions that produce the rows
// determine their count.
//
// Layout is computed in logical document rows. It knows nothing about the
// terminal cursor, scrollback, or absolute screen position.

import (
	"errors"
	"hash/fnv"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// LayoutEntry is the geometry of one block in document-row coordinates.
type LayoutEntry struct {
	BlockID    BlockID
	BlockIndex int
	Kind       EntryKind
	StartRow   int
	Height     int
	Expanded   bool
}

// LayoutRow is one visual row of the document.
type LayoutRow struct {
	BlockID    BlockID
	BlockIndex int
	Row        int
	DocRow     int
	Styled     string
	Width      int
}

// Plain returns the row's text with styling and right padding removed.
func (r LayoutRow) Plain() string {
	return strings.TrimRight(expandTabsStyled(ansi.Strip(r.Styled)), " ")
}

// TranscriptLayout is the retained visual index of a document.
//
// It is owned by the UI goroutine and mutated only through Refresh. The zero
// value is ready to use.
type TranscriptLayout struct {
	key    layoutKey
	blocks []blockLayout
	rows   *fenwick
	byID   map[BlockID]int
	// activity is the transcript position of the one running tool that owns
	// the current activity, or -1. Tracking the previous owner lets an append
	// re-render the old live row when a newer call takes over: two concurrent
	// tools must not both keep the prominent running projection.
	activity int
	// tick is the shared animation/elapsed frame last folded into the cached
	// projections, and tickValid distinguishes "frame zero" from "not yet
	// seen". A changed tick re-renders the one live clock, never history.
	tick      int
	tickValid bool
	// LastRendered is the number of block projections produced by the most
	// recent Refresh. It exists for benchmarks and diagnostics: viewport
	// interaction must not grow this with total document size.
	LastRendered int
	// Reflowed reports that the most recent Refresh re-projected every block
	// because the geometry key changed (width, tier, theme). Instrumentation
	// uses it; viewport interaction never sets it.
	Reflowed bool
}

type layoutKey struct {
	width        int
	contentWidth int
	mode         LayoutMode
	theme        string
}

type blockLayout struct {
	id       BlockID
	index    int
	rev      uint64
	tick     int
	current  bool
	kind     EntryKind
	expanded bool
	// groupEnd records whether this block closes a compact action group.
	// Consecutive tool calls share one visual stream; only the last call in
	// the run carries the expand hint. Adjacency is part of the cached
	// projection, so appending a tool invalidates the previous tool's hint.
	groupEnd bool
	// leadingGap records that this block owns the gap after a tool run.
	// The gap is never owned by the tool, so appending another tool does not
	// change the previous block's height.
	leadingGap bool
	lines      []string
	widths     []int
}

func keyFor(state State, layout Layout) layoutKey {
	return layoutKey{
		width:        layout.Width,
		contentWidth: layout.ContentWidth,
		mode:         layout.Mode,
		theme:        state.Theme,
	}
}

// Refresh brings the index up to date with the document.
//
// Width, theme, and tier changes reflow every block because every projection
// may change. A height-only resize does not: block rows depend on width and
// tier, and the viewport height is applied when slicing. Otherwise only blocks
// whose revision (or running spinner) moved are re-rendered; untouched history
// keeps its cached rows.
func (l *TranscriptLayout) Refresh(state *State, layout Layout) {
	if state == nil {
		return
	}
	defer state.clearDirty()
	key := keyFor(*state, layout)
	entries := state.Entries
	tick := state.Status.Spinner
	current := latestRunningToolIndex(*state)
	tickChanged := l.tickValid && l.tick != tick
	l.tick = tick
	l.tickValid = true
	l.LastRendered = 0
	l.Reflowed = false
	keyChanged := l.rows != nil && l.key != key

	full := l.rows == nil || keyChanged || len(l.blocks) > len(entries) || state.dirtyReset

	if full {
		pal := PaletteFor(state.Theme)
		l.Reflowed = keyChanged
		l.key = key
		l.activity = current
		l.blocks = make([]blockLayout, len(entries))
		l.byID = make(map[BlockID]int, len(entries))
		for i := range entries {
			id := blockIDAt(entries, i)
			l.blocks[i] = l.renderBlock(entries[i], i, id, pal, layout, tick, current == i, entryGroupEnd(entries, i), entryLeadingGap(entries, i))
			l.byID[id] = i
			l.LastRendered++
		}
		heights := make([]int, len(l.blocks))
		for i := range l.blocks {
			heights[i] = len(l.blocks[i].lines)
		}
		if l.rows == nil || l.rows.len() != len(heights) {
			l.rows = newFenwick(len(heights))
		}
		l.rows.reset(heights)
		return
	}

	// Same geometry key. The state's dirty hints name every block Apply
	// changed, so this path never scans the whole history: untouched blocks
	// keep their cached rows and their cached heights.
	grown := len(entries) - len(l.blocks)
	dirty := state.dirty
	previousActivity := l.activity
	l.activity = current
	if previousActivity != current {
		// The live row is a projection of "who owns the activity", not only of
		// the entry it belongs to. Re-render the old owner when a newer call
		// takes over, so exactly one row stays visually live.
		if previousActivity >= 0 && previousActivity < len(entries) {
			dirty = append(append([]int(nil), dirty...), previousActivity)
		}
		if current >= 0 && current < len(entries) {
			dirty = append(dirty, current)
		}
	}
	if tickChanged {
		// Only the one live clock and an open reasoning head depend on the
		// tick. Appending just those indices keeps settled history cached.
		if current >= 0 && current < len(entries) {
			dirty = append(dirty, current)
		}
		if open := state.openReasoning(); open >= 0 {
			dirty = append(dirty, open)
		}
	}
	if grown == 0 && len(dirty) == 0 {
		return
	}
	pal := PaletteFor(state.Theme)
	if grown > 0 {
		l.blocks = append(l.blocks, make([]blockLayout, grown)...)
	}
	changed := make([]int, 0, len(dirty)+grown)
	seen := make(map[int]struct{}, len(dirty))
	for _, i := range dirty {
		if i < 0 || i >= len(entries) {
			continue
		}
		if _, ok := seen[i]; ok {
			continue
		}
		seen[i] = struct{}{}
		if i >= len(l.blocks)-grown && i < len(entries) {
			// Newly appended blocks are rendered below, whether or not the
			// hint list already names them.
			continue
		}
		if l.blocks[i].id == blockIDAt(entries, i) && !blockStale(l.blocks[i], entries[i], entryTick(entries[i], tick, current == i), current == i, entryGroupEnd(entries, i), entryLeadingGap(entries, i)) {
			continue
		}
		oldID := l.blocks[i].id
		id := blockIDAt(entries, i)
		l.blocks[i] = l.renderBlock(entries[i], i, id, pal, layout, tick, current == i, entryGroupEnd(entries, i), entryLeadingGap(entries, i))
		if l.byID != nil && oldID != id {
			delete(l.byID, oldID)
			l.byID[id] = i
		}
		l.LastRendered++
		changed = append(changed, i)
	}
	if grown > 0 {
		newStart := len(l.blocks) - grown
		// The first appended block may close a previous tool group. Its
		// adjacency changed from "end of document" to "followed by a tool",
		// so its cached separator must be recomputed even though Apply never
		// named it dirty.
		if newStart > 0 {
			prev := newStart - 1
			if l.blocks[prev].groupEnd != entryGroupEnd(entries, prev) {
				l.blocks[prev] = l.renderBlock(entries[prev], prev, blockIDAt(entries, prev), pal, layout, tick, current == prev, entryGroupEnd(entries, prev), entryLeadingGap(entries, prev))
				l.LastRendered++
				changed = append(changed, prev)
			}
		}
		for i := newStart; i < len(entries); i++ {
			id := blockIDAt(entries, i)
			l.blocks[i] = l.renderBlock(entries[i], i, id, pal, layout, tick, current == i, entryGroupEnd(entries, i), entryLeadingGap(entries, i))
			if l.byID == nil {
				l.byID = make(map[BlockID]int, len(entries))
				for j := 0; j < i; j++ {
					l.byID[l.blocks[j].id] = j
				}
			}
			l.byID[id] = i
			l.LastRendered++
		}
		// Appending extends the Fenwick tree in place; capacity doubles
		// occasionally, so ordinary block appends stay O(log n).
		for i := newStart; i < len(entries); i++ {
			l.rows.append(len(l.blocks[i].lines))
		}
		// Dirty existing blocks still need their new heights recorded.
		for _, i := range changed {
			if i >= newStart {
				continue
			}
			if err := l.rows.set(i, len(l.blocks[i].lines)); err != nil {
				l.rebuildRowIndex()
				break
			}
		}
		return
	}
	for _, i := range changed {
		if err := l.rows.set(i, len(l.blocks[i].lines)); err != nil {
			// The tree shape can only disagree if the slice was replaced
			// behind Refresh's back; rebuild defensively.
			l.rebuildRowIndex()
			return
		}
	}
}

func blockStale(cached blockLayout, entry Entry, tick int, current bool, groupEnd, leadingGap bool) bool {
	adjacency := cached.groupEnd != groupEnd || cached.leadingGap != leadingGap
	if entry.rev == 0 {
		return cached.rev != fixtureRevision(entry) || cached.tick != tick || cached.current != current ||
			cached.kind != entry.Kind || cached.expanded != entryExpanded(entry) || adjacency
	}
	return cached.rev != entry.rev || cached.tick != tick || cached.current != current ||
		cached.kind != entry.Kind || cached.expanded != entryExpanded(entry) || adjacency
}

// entryTick isolates the one presentation input that changes a running block
// without changing its entry: the live elapsed time. Only the current activity
// owns that clock, so concurrent calls keep stable rows and only one of them is
// visually live.
func entryTick(entry Entry, tick int, current bool) int {
	if current {
		return tick
	}
	// An open reasoning head carries an elapsed time, so it also needs the
	// tick while the runtime is still producing the trace. It is not the
	// prominent activity (the reasoning glyph is deliberately subordinate),
	// but its measured duration must keep advancing.
	if entry.Kind == EntryReasoning && entry.Reasoning != nil && !entry.Reasoning.Closed {
		return tick
	}
	return 0
}

// fixtureRevision is the fallback dirty token for entries that did not pass
// through State.Apply. Production entries carry an allocated revision, so this
// content hash only runs for hand-built fixtures.
func fixtureRevision(entry Entry) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(entry.Kind))
	_, _ = h.Write([]byte(entry.ID))
	_, _ = h.Write([]byte(entry.UserText))
	_, _ = h.Write([]byte(entry.AssistantText))
	if entry.Reasoning != nil {
		_, _ = h.Write([]byte(entry.Reasoning.Text))
		if entry.Reasoning.Expanded {
			_, _ = h.Write([]byte{1})
		}
		if entry.Reasoning.Closed {
			_, _ = h.Write([]byte{2})
		}
	}
	_, _ = h.Write([]byte(entry.Notice))
	_, _ = h.Write([]byte(entry.SupportText))
	if entry.Tool != nil {
		_, _ = h.Write([]byte(entry.Tool.ID))
		_, _ = h.Write([]byte(entry.Tool.Name))
		_, _ = h.Write([]byte(entry.Tool.Status))
		_, _ = h.Write([]byte(entry.Tool.Invocation))
		_, _ = h.Write([]byte(entry.Tool.Summary))
		_, _ = h.Write([]byte(entry.Tool.Text))
		_, _ = h.Write([]byte(entry.Tool.Diff))
		_, _ = h.Write([]byte(entry.Tool.Path))
		_, _ = h.Write([]byte(entry.Tool.Operation))
		if entry.Tool.Streaming {
			_, _ = h.Write([]byte{3})
		}
		if entry.Tool.Expanded {
			_, _ = h.Write([]byte{1})
		}
	}
	if entry.Shell != nil {
		_, _ = h.Write([]byte(entry.Shell.Command))
		_, _ = h.Write([]byte(entry.Shell.Output))
		_, _ = h.Write([]byte(entry.Shell.Status))
	}
	if entry.Task != nil {
		_, _ = h.Write([]byte(entry.Task.ID))
		_, _ = h.Write([]byte(entry.Task.Status))
		_, _ = h.Write([]byte(entry.Task.Objective))
		for _, item := range entry.Task.Items {
			_, _ = h.Write([]byte(item.State))
			_, _ = h.Write([]byte(item.Label))
		}
	}
	_, _ = h.Write([]byte(entry.Recap))
	return h.Sum64()
}

func (l *TranscriptLayout) renderBlock(entry Entry, index int, id BlockID, pal *Palette, layout Layout, tick int, current bool, groupEnd, leadingGap bool) blockLayout {
	b := blockLayout{
		id:         id,
		index:      index,
		rev:        entry.rev,
		tick:       entryTick(entry, tick, current),
		current:    current,
		kind:       entry.Kind,
		expanded:   entryExpanded(entry),
		groupEnd:   groupEnd,
		leadingGap: leadingGap,
	}
	if entry.rev == 0 {
		b.rev = fixtureRevision(entry)
	}
	var sb strings.Builder
	renderEntryGrouped(&sb, entry, pal, layout, current, groupEnd, leadingGap, tick)
	b.lines = entryLines(sb.String())
	b.widths = make([]int, len(b.lines))
	for i, line := range b.lines {
		b.widths[i] = CellWidth(line)
	}
	return b
}

// entryGroupEnd reports whether index closes a compact action group.
//
// A run of adjacent tool calls is one visual stream: only the last call keeps
// the separator row. Everything else is its own block and keeps its existing
// spacing. Adjacency is a projection fact, not semantic state, so it lives
// with the layout rather than on the Entry.
func entryGroupEnd(entries []Entry, index int) bool {
	if index < 0 || index >= len(entries) {
		return false
	}
	if entries[index].Kind != EntryTool {
		return false
	}
	return index+1 >= len(entries) || entries[index+1].Kind != EntryTool
}

// entryLeadingGap reports whether this block owns the gap after a tool run.
//
// A tool never writes a trailing separator. The next non-tool block writes the
// leading gap instead, so appending a tool to a run cannot change the height
// of a block that is already on screen.
func entryLeadingGap(entries []Entry, index int) bool {
	if index <= 0 || index >= len(entries) {
		return false
	}
	return entries[index-1].Kind == EntryTool && entries[index].Kind != EntryTool
}

// entryLines turns one entry's rendered projection into its visual rows.
//
// renderEntry terminates every row with "\n" and adds a separator newline
// between blocks. Removing exactly one trailing terminator preserves the
// separator row while never inventing a row for an empty block.
func entryLines(rendered string) []string {
	if rendered == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(rendered, "\n"), "\n")
}

// Rows returns up to height visual rows starting at the document row offset.
func (l *TranscriptLayout) Rows(offset, height int) []LayoutRow {
	if l == nil || l.rows == nil || height <= 0 {
		return nil
	}
	total := l.TotalRows()
	if total == 0 {
		return nil
	}
	if offset < 0 {
		offset = 0
	}
	if offset >= total {
		return nil
	}
	end := offset + height
	if end > total {
		end = total
	}
	out := make([]LayoutRow, 0, end-offset)
	block, local := l.rows.find(offset)
	for docRow := offset; docRow < end; {
		if block < 0 || block >= len(l.blocks) {
			break
		}
		lines := l.blocks[block].lines
		for local < len(lines) && docRow < end {
			out = append(out, LayoutRow{
				BlockID:    l.blocks[block].id,
				BlockIndex: block,
				Row:        local,
				DocRow:     docRow,
				Styled:     lines[local],
				Width:      l.blocks[block].widths[local],
			})
			local++
			docRow++
		}
		block++
		local = 0
	}
	return out
}

// AllRows returns every visual row. It is for tests and for the no-size
// render path; the interactive viewport uses Rows.
func (l *TranscriptLayout) AllRows() []LayoutRow {
	if l == nil || l.rows == nil {
		return nil
	}
	return l.Rows(0, l.TotalRows())
}

// TotalRows is the document's visual height.
func (l *TranscriptLayout) TotalRows() int {
	if l == nil || l.rows == nil {
		return 0
	}
	return l.rows.total() - l.trailingBlankRows()
}

// trailingBlankRows counts the separator rows at the end of the final block.
//
// renderEntry terminates each block with a separator newline. It becomes the
// gap before the next block, but at the end of the document the old full-body
// projection trimmed it. Keeping the cached rows intact and subtracting the
// trailing blanks here means appending a block restores the gap without
// re-rendering any earlier projection.
func (l *TranscriptLayout) trailingBlankRows() int {
	if len(l.blocks) == 0 {
		return 0
	}
	lines := l.blocks[len(l.blocks)-1].lines
	n := 0
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(ansi.Strip(lines[i])) != "" {
			break
		}
		n++
	}
	return n
}

// effectiveHeight is a block's height as part of the visible document. The
// final block's trailing separator is not a row.
func (l *TranscriptLayout) effectiveHeight(index int) int {
	if l == nil || index < 0 || index >= len(l.blocks) {
		return 0
	}
	h := len(l.blocks[index].lines)
	if index == len(l.blocks)-1 {
		h -= l.trailingBlankRows()
	}
	if h < 0 {
		h = 0
	}
	return h
}

// Entries returns the full layout index. It computes starts on demand; the
// render path never calls it.
func (l *TranscriptLayout) Entries() []LayoutEntry {
	if l == nil || l.rows == nil {
		return nil
	}
	out := make([]LayoutEntry, len(l.blocks))
	for i := range l.blocks {
		out[i] = LayoutEntry{
			BlockID:    l.blocks[i].id,
			BlockIndex: i,
			Kind:       l.blocks[i].kind,
			StartRow:   l.rows.prefix(i),
			Height:     l.effectiveHeight(i),
			Expanded:   l.blocks[i].expanded,
		}
	}
	return out
}

// BlockIndexForID resolves a stable block ID to its current position.
func (l *TranscriptLayout) BlockIndexForID(id BlockID) (int, bool) {
	if l == nil {
		return 0, false
	}
	index, ok := l.byID[id]
	return index, ok
}

// rebuildRowIndex reconstructs the Fenwick tree after a defensive mismatch.
func (l *TranscriptLayout) rebuildRowIndex() {
	heights := make([]int, len(l.blocks))
	for i := range l.blocks {
		heights[i] = len(l.blocks[i].lines)
	}
	l.rows = newFenwick(len(heights))
	l.rows.reset(heights)
}

// BlockStart returns the first document row of a block.
func (l *TranscriptLayout) BlockStart(index int) int {
	if l == nil || l.rows == nil || index < 0 || index >= len(l.blocks) {
		return 0
	}
	return l.rows.prefix(index)
}

// BlockHeight returns a block's visual height.
func (l *TranscriptLayout) BlockHeight(index int) int {
	return l.effectiveHeight(index)
}

// BlockRow returns the styled and plain text of one row within a block.
func (l *TranscriptLayout) BlockRow(index, row int) (LayoutRow, bool) {
	if l == nil || l.rows == nil || index < 0 || index >= len(l.blocks) {
		return LayoutRow{}, false
	}
	lines := l.blocks[index].lines
	if row < 0 || row >= l.effectiveHeight(index) {
		return LayoutRow{}, false
	}
	return LayoutRow{
		BlockID:    l.blocks[index].id,
		BlockIndex: index,
		Row:        row,
		DocRow:     l.rows.prefix(index) + row,
		Styled:     lines[row],
		Width:      l.blocks[index].widths[row],
	}, true
}

// BlockForRow resolves a document row to its block and local row.
func (l *TranscriptLayout) BlockForRow(docRow int) (LayoutEntry, int, bool) {
	if l == nil || l.rows == nil || docRow < 0 || docRow >= l.TotalRows() {
		return LayoutEntry{}, 0, false
	}
	index, local := l.rows.find(docRow)
	if index < 0 || index >= len(l.blocks) {
		return LayoutEntry{}, 0, false
	}
	return LayoutEntry{
		BlockID:    l.blocks[index].id,
		BlockIndex: index,
		Kind:       l.blocks[index].kind,
		StartRow:   l.rows.prefix(index),
		Height:     l.effectiveHeight(index),
		Expanded:   l.blocks[index].expanded,
	}, local, true
}

// fenwick is a row-sum index: heights are leaves, internal nodes hold sums, so
// expanding one block updates O(log n) entries and resolving a row is O(log n).
// Capacity grows geometrically, so appending a block does not rebuild the
// index over the whole document.
type fenwick struct {
	n       int
	tree    []int
	heights []int
}

func newFenwick(n int) *fenwick {
	capacity := n
	if capacity < 8 {
		capacity = 8
	}
	return &fenwick{n: n, tree: make([]int, capacity+1), heights: make([]int, n)}
}

func (f *fenwick) len() int { return f.n }

func (f *fenwick) reset(heights []int) {
	f.n = len(heights)
	f.heights = append(f.heights[:0], heights...)
	f.rebuild()
}

// rebuild reconstructs the tree over the whole allocated capacity, treating
// leaves past f.n as zero.
//
// Nodes above f.n must still hold the sum of the real leaves in their range:
// the next append only adds its own height and walks the ancestor chain, so a
// node left at zero would drop every earlier leaf it covers. That is the
// boundary where a following viewport silently loses blocks and a later full
// reflow (a resize) makes them appear again.
func (f *fenwick) rebuild() {
	capacity := f.n
	if capacity < 8 {
		capacity = 8
	}
	f.rebuildWithCapacity(capacity)
}

// append adds one leaf at the end.
func (f *fenwick) append(height int) {
	if f == nil {
		return
	}
	f.heights = append(f.heights, height)
	f.n = len(f.heights)
	capacity := len(f.tree) - 1
	if capacity < f.n {
		capacity *= 2
		if capacity < 8 {
			capacity = 8
		}
		// Growth is amortised: this rebuild happens only when capacity
		// doubles, and keeps every ancestor exact across the new boundary.
		f.rebuildWithCapacity(capacity)
		return
	}
	f.tree[f.n] += height
	for i := f.n + (f.n & -f.n); i <= capacity; i += i & -i {
		f.tree[i] += height
	}
}

func (f *fenwick) rebuildWithCapacity(capacity int) {
	f.tree = make([]int, capacity+1)
	for i := 0; i < capacity; i++ {
		var h int
		if i < len(f.heights) {
			h = f.heights[i]
		}
		f.tree[i+1] += h
		parent := i + 1 + ((i + 1) & -(i + 1))
		if parent <= capacity {
			f.tree[parent] += f.tree[i+1]
		}
	}
}

func (f *fenwick) set(index, height int) error {
	if index < 0 || index >= f.n {
		return errRowIndexRange
	}
	delta := height - f.heights[index]
	if delta == 0 {
		return nil
	}
	f.heights[index] = height
	capacity := len(f.tree) - 1
	for i := index + 1; i <= capacity; i += i & -i {
		f.tree[i] += delta
	}
	return nil
}

// prefix returns the number of rows before block index.
func (f *fenwick) prefix(index int) int {
	if f == nil || f.n == 0 {
		return 0
	}
	if index > f.n {
		index = f.n
	}
	sum := 0
	for i := index; i > 0; i -= i & -i {
		sum += f.tree[i]
	}
	return sum
}

func (f *fenwick) total() int { return f.prefix(f.n) }

// find resolves a zero-based document row to a block and its local row.
func (f *fenwick) find(row int) (int, int) {
	if f == nil || f.n == 0 || row < 0 || row >= f.total() {
		return -1, 0
	}
	target := row + 1
	index, step := 0, highestPowerOfTwo(f.n)
	for step > 0 {
		next := index + step
		if next <= f.n && f.tree[next] < target {
			index = next
			target -= f.tree[next]
		}
		step >>= 1
	}
	// index is the count of blocks whose cumulative height is below the
	// target, which is exactly the block containing the row.
	local := row - f.prefix(index)
	return index, local
}

func highestPowerOfTwo(n int) int {
	if n <= 0 {
		return 0
	}
	p := 1
	for p<<1 <= n {
		p <<= 1
	}
	return p
}

// errRowIndexRange reports a row-index mutation outside the indexed range.
// Refresh treats it as "the tree no longer matches the blocks" and rebuilds.
var errRowIndexRange = errors.New("transcript row index out of range")
