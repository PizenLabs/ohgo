// Package shell decodes and minimises the output of subprocesses.
//
// It replaces the original `pi-shell` crate, which the TypeScript side reached
// through a NAPI binding. The Go port keeps the parts that are pure functions
// of their input: incremental UTF-8 decoding, and the output minimiser that
// turns a wall of build log into the lines that matter.
package shell

import (
	"strings"
	"unicode/utf8"
)

// Replacement is substituted for a byte sequence that is not valid UTF-8.
//
// It matches "replacement character" (U+FFFD), which is what
// `string(bytes)` would have produced anyway — but doing it here means a
// truncated multi-byte sequence is held rather than replaced.
const Replacement = "\uFFFD"

// Decoder incrementally decodes one stdout or stderr stream.
//
// A pipe can split a multi-byte UTF-8 sequence across reads. Emitting each read
// independently would turn that character into a replacement character, so the
// incomplete tail is buffered until the next read completes it.
//
// It is not safe for concurrent use: one decoder owns one stream.
type Decoder struct {
	// pending holds bytes that do not yet form a complete sequence.
	pending []byte
}

// New returns an empty decoder.
func New() *Decoder { return &Decoder{} }

// Push decodes the next read and returns the text that is safe to emit.
//
// The fast path returns the input unchanged when nothing is buffered and the
// read is already valid UTF-8, which is the overwhelmingly common case for a
// UTF-8 program.
func (d *Decoder) Push(bytes []byte) string {
	if len(bytes) == 0 {
		return ""
	}
	if len(d.pending) == 0 && utf8.Valid(bytes) {
		return string(bytes)
	}

	d.pending = append(d.pending, bytes...)
	return d.drain(false)
}

// Finish flushes any held bytes at end of stream.
//
// An incomplete sequence can never be completed, so it becomes a replacement
// character rather than being dropped: silently losing a byte would make the
// output shorter than the bytes the process wrote.
func (d *Decoder) Finish() string {
	return d.drain(true)
}

// drain decodes as much of pending as it can, compacting once at the end.
//
// It scans rather than re-slicing per invalid sequence, so a buffer full of
// invalid bytes costs linear time rather than quadratic.
func (d *Decoder) drain(eof bool) string {
	if len(d.pending) == 0 {
		return ""
	}

	var out strings.Builder
	out.Grow(len(d.pending))

	start := 0
	for start < len(d.pending) {
		remaining := d.pending[start:]

		if utf8.Valid(remaining) {
			out.Write(remaining)
			start = len(d.pending)
			break
		}

		// Emit the valid prefix, then classify what is wrong with the rest.
		validLen := validPrefixLen(remaining)
		out.Write(remaining[:validLen])
		start += validLen

		seqLen := utf8SequenceLen(d.pending[start:])
		switch {
		case seqLen == 0:
			// A byte that cannot begin a sequence at all is junk: replace it and
			// move on. Holding it would stall the stream forever on a binary
			// payload, since no future read can complete it.
			out.WriteString(Replacement)
			start++
			continue

		case start+seqLen <= len(d.pending):
			// A complete but invalid sequence: replace it and continue.
			out.WriteString(Replacement)
			start += seqLen
			continue

		case eof:
			// Incomplete because the stream ended, so it never will complete.
			out.WriteString(Replacement)
			start = len(d.pending)
			continue
		}

		// Incomplete because the read split it: hold for the next push.
		break
	}

	// Keep only what was not consumed.
	d.pending = append(d.pending[:0], d.pending[start:]...)
	return out.String()
}

// validPrefixLen returns the length of the valid UTF-8 prefix of b.
//
// It is equivalent to Rust's `Utf8Error::valid_up_to`: the number of bytes
// before the first invalid or incomplete sequence.
func validPrefixLen(b []byte) int {
	for i := 0; i < len(b); {
		if b[i] < utf8.RuneSelf {
			i++
			continue
		}
		seqLen := utf8SequenceLen(b[i:])
		if seqLen == 0 {
			return i
		}
		if i+seqLen > len(b) {
			// Incomplete: the prefix ends here.
			return i
		}
		if !utf8.Valid(b[i : i+seqLen]) {
			return i
		}
		i += seqLen
	}
	return len(b)
}

// utf8SequenceLen returns the length of the UTF-8 sequence starting at b[0],
// or 0 when the lead byte cannot begin a sequence at all.
//
// It reports the *expected* length, so it distinguishes "this byte is junk"
// (0) from "this sequence is truncated by the read" (non-zero but extending
// past the buffer).
func utf8SequenceLen(b []byte) int {
	if len(b) == 0 {
		return 0
	}
	switch first := b[0]; {
	case first < 0x80:
		return 1
	case first>>5 == 0b110:
		return 2
	case first>>4 == 0b1110:
		return 3
	case first>>3 == 0b11110:
		return 4
	default:
		// A continuation byte, or a lead byte for a length UTF-8 does not
		// define: definitely not the start of a valid sequence.
		return 0
	}
}

// Pending reports the bytes currently buffered awaiting completion.
//
// It exists so a test can assert that a split sequence was held rather than
// emitted or dropped.
func (d *Decoder) Pending() int { return len(d.pending) }
