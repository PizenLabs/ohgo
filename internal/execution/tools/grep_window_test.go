package tools

import (
	"strings"
	"testing"
	"time"
)

// A line longer than the scanner's initial 64 KiB buffer (a minified bundle or
// generated table) must not end the scan of its file: matches after it are
// real repository content and a silent stop turns them into false negatives.
func TestGrepReachesMatchesPastALongLine(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"gen.go": strings.Repeat("x", 70*1024) + "\nneedle after the long line\n",
	})

	text, result := grepText(t, NewGrepTool(root), map[string]any{"pattern": "needle"})
	if result.IsError {
		t.Fatalf("unexpected error: %s", text)
	}
	if !strings.Contains(text, "needle after the long line") {
		t.Errorf("match after a long line was silently dropped: %s", text)
	}
}

// A file larger than the search window is only partly searched. A multi-file
// search must say so, including when the in-window part has no match.
func TestGrepSaysWhenAFileExceedsTheSearchWindow(t *testing.T) {
	root := t.TempDir()
	filler := strings.Repeat("filler line with no match\n", 200)
	var sb strings.Builder
	for sb.Len() <= GrepMaxFileBytes+1024 {
		sb.WriteString(filler)
	}
	sb.WriteString("needle beyond the window\n")
	writeTree(t, root, map[string]string{
		"big.go":   sb.String(),
		"other.go": "package other\n",
	})

	text, result := grepText(t, NewGrepTool(root), map[string]any{"pattern": "needle"})
	if result.IsError {
		t.Fatalf("unexpected error: %s", text)
	}
	if strings.Contains(text, "needle beyond the window") {
		t.Fatalf("match beyond the window was shown: %s", text)
	}
	if !strings.Contains(text, "search window") {
		t.Errorf("a partly searched file was not reported: %s", text)
	}
}

// A search that runs out of time must not answer "No matches found" as if it
// had searched everything.
func TestGrepReportsTimeoutInsteadOfAnEmptyAnswer(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"a.go": "needle\n"})
	tool := NewGrepTool(root)
	tool.Timeout = time.Nanosecond

	text, result := grepText(t, tool, map[string]any{"pattern": "needle"})
	if result.IsError {
		t.Fatalf("unexpected error: %s", text)
	}
	if !strings.Contains(text, "partial") {
		t.Errorf("a timed-out search was not reported as partial: %s", text)
	}
}
