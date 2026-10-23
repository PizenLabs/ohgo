package tui

// Virtual viewport and scroll state.
//
// A Viewport is a window of visual document rows. Its Offset is never a
// terminal row or a scrollback line: it indexes TranscriptLayout's row space.
// Two states are explicit — FOLLOWING the live edge and DETACHED at a logical
// anchor. The anchor is a block ID plus a row inside that block, so appended
// output, expansion above the window, and width reflow cannot silently move the
// content the user is reading.

// Anchor identifies a logical reading position without a screen coordinate.
type Anchor struct {
	BlockID BlockID
	Row     int
}

// Viewport is the resolved scroll window for one frame.
type Viewport struct {
	// Offset is the first visible content row in document-row coordinates.
	Offset int
	// Height is the number of content rows the window shows. When Marker is
	// true this is one row less than the transcript budget, because the
	// scrolled indicator occupies a row.
	Height int
	// TotalRows is the document's visual height.
	TotalRows int
	// MaxOffset is the largest legal Offset.
	MaxOffset int
	// Marker reports the "lines above" indicator is shown.
	Marker bool
	// Following reports the viewport is anchored to the live edge.
	Following bool
	// ScrollAbove is the distance from the window's bottom to the document's
	// bottom, used by the indicator. It is 0 in following mode.
	ScrollAbove int
	// Anchor is the current top reading position.
	Anchor Anchor
}

// ViewportTracker is the single mutation point for derived viewport state.
//
// It belongs to the UI goroutine. Update is idempotent for unchanged input and
// distinguishes a user scroll (ScrollSeq changed) from a reflow (content moved
// while the intent stayed the same).
type ViewportTracker struct {
	value      Viewport
	valid      bool
	lastIntent int
	lastSeq    int
	following  bool
}

// Update resolves a viewport from the current intent and layout.
//
// follow is the canonical FOLLOWING flag; scrollIntent is the user's distance
// above the live edge; seq changes only on an explicit scroll event; budget is
// the transcript area's row budget (before the marker takes its row).
func (v *ViewportTracker) Update(follow bool, scrollIntent, seq, budget int, index *TranscriptLayout) Viewport {
	total := 0
	if index != nil {
		total = index.TotalRows()
	}
	vp := Viewport{TotalRows: total, Following: follow}
	if budget <= 0 {
		v.value = vp
		v.valid = true
		v.following = follow
		v.lastIntent = scrollIntent
		v.lastSeq = seq
		return vp
	}

	if total <= budget {
		// The document fits. There is nothing to scroll and no marker to
		// show; a stale detached flag must not invent one.
		vp.Offset = 0
		vp.Height = budget
		vp.MaxOffset = 0
		vp.Anchor = anchorAt(index, 0)
		v.value = vp
		v.valid = true
		v.following = follow
		v.lastIntent = scrollIntent
		v.lastSeq = seq
		return vp
	}

	if follow || scrollIntent <= 0 {
		vp.Following = true
		vp.Height = budget
		vp.Offset = total - budget
		vp.MaxOffset = vp.Offset
		vp.Anchor = anchorAt(index, vp.Offset)
		v.value = vp
		v.valid = true
		v.following = true
		v.lastIntent = scrollIntent
		v.lastSeq = seq
		return vp
	}

	vp.Height = budget - 1
	maxOffset := total - vp.Height
	if maxOffset < 0 {
		maxOffset = 0
	}
	intent := scrollIntent
	if intent < 1 {
		intent = 1
	}
	if intent > maxOffset {
		intent = maxOffset
	}

	offset := -1
	if !v.valid || v.following {
		// Entering DETACHED (or the first resolution): translate the intent
		// once. There is no earlier anchor to preserve.
		offset = maxOffset - intent
	} else if seq != v.lastSeq {
		// A user scroll is relative to the position they were reading, not to
		// the document's total height, which may have grown since.
		delta := scrollIntent - v.lastIntent
		offset = v.value.Offset - delta
	} else if a, ok := offsetForAnchor(index, v.value.Anchor); ok {
		// Reflow, append, or expansion with no new intent: hold the content
		// under the reading anchor.
		offset = a
	} else if v.valid {
		offset = v.value.Offset
	}
	if offset < 0 {
		offset = maxOffset - intent
	}
	if offset > maxOffset {
		offset = maxOffset
	}
	if offset < 0 {
		offset = 0
	}

	vp.Offset = offset
	vp.MaxOffset = maxOffset
	vp.Marker = true
	vp.ScrollAbove = maxOffset - offset
	vp.Anchor = anchorAt(index, offset)
	v.value = vp
	v.valid = true
	v.following = false
	v.lastIntent = scrollIntent
	v.lastSeq = seq
	return vp
}

// Value returns the last resolved viewport.
func (v *ViewportTracker) Value() Viewport { return v.value }

// anchorAt resolves a document row to a stable anchor.
func anchorAt(index *TranscriptLayout, offset int) Anchor {
	if index == nil || index.TotalRows() == 0 {
		return Anchor{}
	}
	if offset < 0 {
		offset = 0
	}
	if offset >= index.TotalRows() {
		offset = index.TotalRows() - 1
	}
	entry, row, ok := index.BlockForRow(offset)
	if !ok {
		return Anchor{}
	}
	return Anchor{BlockID: entry.BlockID, Row: row}
}

// offsetForAnchor resolves an anchor to a document row.
func offsetForAnchor(index *TranscriptLayout, anchor Anchor) (int, bool) {
	if index == nil || anchor.BlockID == "" {
		return 0, false
	}
	block, ok := index.BlockIndexForID(anchor.BlockID)
	if !ok {
		return 0, false
	}
	height := index.BlockHeight(block)
	if height <= 0 {
		return 0, false
	}
	row := anchor.Row
	if row < 0 {
		row = 0
	}
	if row >= height {
		row = height - 1
	}
	return index.BlockStart(block) + row, true
}
