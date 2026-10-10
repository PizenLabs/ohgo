// Package diff computes Myers line diffs and unified-diff hunks.
//
// It replaces the original `pi-diff` crate, which the TypeScript side reached
// through a NAPI binding. The run structure is a faithful port of the original's
// O(ND) implementation, including its tie-breaking, so a diff produced here
// matches one produced there token for token.
package diff

import "strings"

// Run is one contiguous span of the diff.
//
// Exactly one of Added or Removed is set; a run with neither is a common run
// present in both inputs.
type Run struct {
	// Count is the number of tokens in this run.
	Count int
	// Added is true when the run exists only in the new input.
	Added bool
	// Removed is true when the run exists only in the old input.
	Removed bool
}

// component is one node of the edit path.
//
// It is a linked list rather than a slice because the algorithm branches: each
// candidate path extends its parent's chain without copying it. Go's garbage
// collector makes the shared tails free, where the original used Rc.
type component struct {
	count   int
	added   bool
	removed bool
	prev    *component
}

// pathState is the furthest old-text position reached on one diagonal, plus the
// chain of edits that got there.
type pathState struct {
	oldPos int
	last   *component
}

// extractCommon extends a path along its diagonal while tokens match,
// recording the common run. It returns the new-token position.
func extractCommon(path *pathState, newTokens, oldTokens []uint32, diagonal int) int {
	newLen := len(newTokens)
	oldLen := len(oldTokens)

	oldPos := path.oldPos
	newPos := oldPos - diagonal
	common := 0

	for newPos+1 < newLen && oldPos+1 < oldLen && oldTokens[oldPos+1] == newTokens[newPos+1] {
		newPos++
		oldPos++
		common++
	}
	if common > 0 {
		path.last = &component{
			count:   common,
			added:   false,
			removed: false,
			prev:    path.last,
		}
	}
	path.oldPos = oldPos
	return newPos
}

// addToPath branches from a path with one added or removed token.
//
// It merges into the previous component when the edit kind repeats, which is
// what keeps the run structure minimal: a run of three insertions is one Run of
// count 3, not three Runs of count 1.
func addToPath(path *pathState, added, removed bool, oldPosInc int) pathState {
	oldPos := path.oldPos + oldPosInc
	if path.last != nil && path.last.added == added && path.last.removed == removed {
		return pathState{
			oldPos: oldPos,
			last: &component{
				count:   path.last.count + 1,
				added:   added,
				removed: removed,
				prev:    path.last.prev,
			},
		}
	}
	return pathState{
		oldPos: oldPos,
		last:   &component{count: 1, added: added, removed: removed, prev: path.last},
	}
}

// buildRuns converts a component chain into forward-ordered runs.
func buildRuns(last *component) []Run {
	var reversed []Run
	for cursor := last; cursor != nil; cursor = cursor.prev {
		reversed = append(reversed, Run{
			Count:   cursor.count,
			Added:   cursor.added,
			Removed: cursor.removed,
		})
	}
	// The chain is built newest-first, so the runs come out reversed.
	for i, j := 0, len(reversed)-1; i < j; i, j = i+1, j-1 {
		reversed[i], reversed[j] = reversed[j], reversed[i]
	}
	return reversed
}

// Myers computes the O(ND) diff of two interned token sequences.
//
// The tie-breaking is deliberate and load-bearing: when a removal and an
// insertion reach equally far, the insertion path is preferred, matching the
// original. Changing the preference produces a different — still valid — edit
// script, which is why the run structure is asserted rather than just the
// resulting text.
func Myers(oldTokens, newTokens []uint32) []Run {
	oldLen := len(oldTokens)
	newLen := len(newTokens)

	maxEdit := oldLen + newLen
	offset := maxEdit + 1

	// best[diagonal+offset] holds the furthest path reaching that diagonal.
	best := make([]*pathState, 2*maxEdit+3)

	// Edit length 0: the texts may share a leading common run.
	seed := pathState{oldPos: -1}
	seedNewPos := extractCommon(&seed, newTokens, oldTokens, 0)
	if seed.oldPos+1 >= oldLen && seedNewPos+1 >= newLen {
		return buildRuns(seed.last)
	}
	best[offset] = &seed

	// Once a diagonal reaches the end of one text, no diagonal beyond it can
	// ever be on a shortest path, so the search range narrows.
	minDiagonal := -(maxEdit + 2)
	maxDiagonal := maxEdit + 2

	for editLength := 1; editLength <= maxEdit; editLength++ {
		for diagonal := maxInt(minDiagonal, -editLength); diagonal <= minInt(maxDiagonal, editLength); diagonal += 2 {
			idx := diagonal + offset

			removePath := best[idx-1]
			best[idx-1] = nil

			var addOldPos int
			hasAdd := false
			if best[idx+1] != nil {
				addOldPos = best[idx+1].oldPos
				hasAdd = true
			}

			addNewPos := 0
			canAdd := false
			if hasAdd {
				addNewPos = addOldPos - diagonal
				canAdd = addNewPos >= 0 && addNewPos < newLen
			}
			canRemove := removePath != nil && removePath.oldPos+1 < oldLen

			if !canAdd && !canRemove {
				best[idx] = nil
				continue
			}

			// Prefer the path that is furthest along, breaking ties toward the
			// insertion.
			var base pathState
			preferAdd := !canRemove || (canAdd && removePath != nil && removePath.oldPos < addOldPos)
			if preferAdd {
				base = addToPath(best[idx+1], true, false, 0)
			} else {
				base = addToPath(removePath, false, true, 1)
			}

			newPos := extractCommon(&base, newTokens, oldTokens, diagonal)
			if base.oldPos+1 >= oldLen && newPos+1 >= newLen {
				return buildRuns(base.last)
			}
			if base.oldPos+1 >= oldLen && diagonal-1 < maxDiagonal {
				maxDiagonal = diagonal - 1
			}
			if newPos+1 >= newLen && diagonal+1 > minDiagonal {
				minDiagonal = diagonal + 1
			}
			best[idx] = &base
		}
	}

	// Myers terminates within oldLen+newLen edits, so this is unreachable for
	// any pair of sequences. Returning the seed keeps the function total rather
	// than panicking on a future change to the bounds above.
	return buildRuns(best[offset].last)
}

// Intern maps tokens to dense ids under exact equality.
//
// Interning is what makes the comparison O(1) per token: the algorithm works on
// integers, and a generic token would be compared with a hash per step.
func Intern(oldTokens, newTokens []string) ([]uint32, []uint32) {
	ids := make(map[string]uint32, len(oldTokens)+len(newTokens))

	assign := func(token string) uint32 {
		if id, ok := ids[token]; ok {
			return id
		}
		id := uint32(len(ids))
		ids[token] = id
		return id
	}

	oldIDs := make([]uint32, len(oldTokens))
	for i, token := range oldTokens {
		oldIDs[i] = assign(token)
	}
	newIDs := make([]uint32, len(newTokens))
	for i, token := range newTokens {
		newIDs[i] = assign(token)
	}
	return oldIDs, newIDs
}

// Change is one run mapped back to its text.
type Change struct {
	// Value is the joined token text for this run.
	Value string
	// Count is the number of tokens in this run.
	Count int
	// Added is true when the run exists only in the new input.
	Added bool
	// Removed is true when the run exists only in the old input.
	Removed bool
}

// BuildChanges maps runs back to values.
//
// Common runs take their text from the new tokens, because the two texts agree
// there and only the new one carries the version that was kept.
func BuildChanges(runs []Run, oldTokens, newTokens []string) []Change {
	var oldPos, newPos int

	out := make([]Change, 0, len(runs))
	for _, run := range runs {
		change := Change{Count: run.Count, Added: run.Added, Removed: run.Removed}
		switch {
		case run.Removed:
			change.Value = strings.Join(oldTokens[oldPos:oldPos+run.Count], "")
			oldPos += run.Count
		default:
			change.Value = strings.Join(newTokens[newPos:newPos+run.Count], "")
			newPos += run.Count
			if !run.Added {
				oldPos += run.Count
			}
		}
		out = append(out, change)
	}
	return out
}

// LineTokens splits text into jsdiff-compatible line tokens.
//
// The trailing newline stays with its line, so joining the tokens reproduces
// the input exactly — including a final line that lacks one.
//
// Go's SplitAfter emits a trailing empty element for a text ending in a
// newline, where the original's split_inclusive does not. That empty token
// would become a phantom context line, so it is dropped here: the difference is
// a hunk header claiming one more line than the file has.
func LineTokens(text string) []string {
	if text == "" {
		return nil
	}
	tokens := strings.SplitAfter(text, "\n")
	if last := len(tokens) - 1; tokens[last] == "" {
		tokens = tokens[:last]
	}
	return tokens
}

// LineRuns returns the diff runs for two texts at line granularity.
func LineRuns(oldText, newText string) []Run {
	oldTokens := LineTokens(oldText)
	newTokens := LineTokens(newText)
	oldIDs, newIDs := Intern(oldTokens, newTokens)
	return Myers(oldIDs, newIDs)
}

// DiffLines returns the line-level changes between two texts.
func DiffLines(oldText, newText string) []Change {
	oldTokens := LineTokens(oldText)
	newTokens := LineTokens(newText)
	oldIDs, newIDs := Intern(oldTokens, newTokens)
	runs := Myers(oldIDs, newIDs)
	return BuildChanges(runs, oldTokens, newTokens)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
