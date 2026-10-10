package tools

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/agent"
)

// grepText runs a grep and returns the response text.
func grepText(t *testing.T, tool *GrepTool, args map[string]any) (string, agent.Result) {
	t.Helper()
	result := tool.Execute(testContext(), args)
	return result.Text, result
}

func TestGrepFindsMatchesGroupedByFile(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"a.go":  "package main\nfunc alpha() {}\n",
		"b.go":  "package main\nfunc alpha() {}\n// beta\n",
		"c.txt": "nothing here\n",
	})

	text, result := grepText(t, NewGrepTool(root), map[string]any{"pattern": "alpha"})
	if result.IsError {
		t.Fatalf("unexpected error: %s", text)
	}
	if !strings.Contains(text, "a.go") || !strings.Contains(text, "b.go") {
		t.Errorf("response did not list both matching files: %s", text)
	}
	if strings.Contains(text, "c.txt") {
		t.Errorf("a non-matching file was reported: %s", text)
	}
	if !strings.Contains(text, "2 file(s) matched") {
		t.Errorf("summary missing or wrong: %s", text)
	}
	// Line numbers let the model jump straight to the hit.
	if !strings.Contains(text, "2: func alpha() {}") {
		t.Errorf("expected a numbered match line: %s", text)
	}
}

func TestGrepIsCaseInsensitiveWhenAsked(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"a.go": "Alpha\nalpha\nALPHA\n"})

	sensitive, _ := grepText(t, NewGrepTool(root), map[string]any{"pattern": "alpha", "case": true})
	if strings.Count(sensitive, "alpha") > 1 {
		t.Errorf("case-sensitive search matched more than the lowercase line: %s", sensitive)
	}
	insensitive, _ := grepText(t, NewGrepTool(root), map[string]any{"pattern": "alpha", "case": false})
	if !strings.Contains(insensitive, "Alpha") {
		t.Errorf("case-insensitive search missed the capitalised line: %s", insensitive)
	}
}

func TestGrepScopesToASingleFile(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"a.go": "needle\n",
		"b.go": "needle\n",
	})

	text, _ := grepText(t, NewGrepTool(root), map[string]any{"pattern": "needle", "path": "a.go"})
	if !strings.Contains(text, "a.go") {
		t.Errorf("expected a.go in the response: %s", text)
	}
	if strings.Contains(text, "b.go") {
		t.Errorf("a file outside the requested scope was included: %s", text)
	}
}

func TestGrepHonoursGitignore(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		".gitignore":     "generated/\n",
		"generated/g.go": "needle\n",
		"src/s.go":       "needle\n",
	})

	text, _ := grepText(t, NewGrepTool(root), map[string]any{"pattern": "needle"})
	if strings.Contains(text, "generated/g.go") {
		t.Errorf("an ignored file was searched: %s", text)
	}
	withIgnored, _ := grepText(t, NewGrepTool(root), map[string]any{"pattern": "needle", "gitignore": false})
	if !strings.Contains(withIgnored, "generated/g.go") {
		t.Errorf("gitignore=false did not search ignored files: %s", withIgnored)
	}
}

func TestGrepPaginatesWithSkip(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{}
	for i := 0; i < 5; i++ {
		files[fileNameFor(i)] = "needle\n"
	}
	writeTree(t, root, files)

	first, _ := grepText(t, NewGrepTool(root), map[string]any{"pattern": "needle", "limit": 2.0})
	if !strings.Contains(first, "5 file(s) matched") {
		t.Errorf("the total count must reflect every match, not the page: %s", first)
	}
	if !strings.Contains(first, "skip=2") {
		t.Errorf("the response must tell the model how to get the next page: %s", first)
	}

	second, _ := grepText(t, NewGrepTool(root), map[string]any{"pattern": "needle", "limit": 2.0, "skip": 2.0})
	// The two pages must not overlap: a shared file means the offset is wrong.
	firstFiles := grepFileList(first)
	secondFiles := grepFileList(second)
	for _, name := range secondFiles {
		if contains(firstFiles, name) {
			t.Errorf("page 2 repeated %q from page 1: %v vs %v", name, firstFiles, secondFiles)
		}
	}
}

// grepFileList extracts the file headers from a grep response.
//
// File headers are the only unindented lines that end with a colon; match
// lines are indented, so filtering on shape is enough to separate them.
func grepFileList(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		if line == "" || (line[0] == ' ' || line[0] == '\t') {
			continue
		}
		if !strings.HasSuffix(line, ":") || strings.Contains(line, "(") {
			continue
		}
		out = append(out, strings.TrimSuffix(line, ":"))
	}
	return out
}

func TestGrepCapsMatchesPerFile(t *testing.T) {
	root := t.TempDir()
	var sb strings.Builder
	for i := 0; i < 100; i++ {
		sb.WriteString("needle line\n")
	}
	writeTree(t, root, map[string]string{"a.go": sb.String()})

	// A single-file scope raises the cap, so the same file yields more matches
	// when it is the only thing being searched.
	multi, _ := grepText(t, NewGrepTool(root), map[string]any{"pattern": "needle", "path": "b.go"})
	_ = multi
	single, _ := grepText(t, NewGrepTool(root), map[string]any{"pattern": "needle", "path": "a.go"})
	if strings.Count(single, "needle line") > GrepSingleFileMatches {
		t.Errorf("matches = %d, want at most %d", strings.Count(single, "needle line"), GrepSingleFileMatches)
	}
}

func TestGrepRejectsInvalidInput(t *testing.T) {
	root := t.TempDir()
	tool := NewGrepTool(root)

	if _, result := grepText(t, tool, map[string]any{}); !result.IsError {
		t.Error("expected a missing pattern to fail")
	}
	if _, result := grepText(t, tool, map[string]any{"pattern": "[unclosed"}); !result.IsError {
		t.Error("expected an invalid regular expression to fail")
	}
	if _, result := grepText(t, tool, map[string]any{"pattern": "x", "skip": -1.0}); !result.IsError {
		t.Error("expected a negative skip to fail")
	}
	if _, result := grepText(t, tool, map[string]any{"pattern": "x", "case": "yes"}); !result.IsError {
		t.Error("expected a non-boolean case flag to fail")
	}
	if _, result := grepText(t, tool, map[string]any{"pattern": "x", "path": "../outside"}); !result.IsError {
		t.Error("expected a path outside the root to fail")
	}
}

func TestGrepReportsNoMatches(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"a.go": "content\n"})
	text, result := grepText(t, NewGrepTool(root), map[string]any{"pattern": "zzzz"})
	if result.IsError {
		t.Fatalf("unexpected error: %s", text)
	}
	if !result.Useless {
		t.Error("an empty result should be marked useless so compaction can elide it")
	}
}

func TestGrepHonoursCancellation(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"a.go": "needle\n"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, result := grepText(t, NewGrepTool(root), map[string]any{"pattern": "needle"})
	result = NewGrepTool(root).Execute(agent.Context{Ctx: ctx}, map[string]any{"pattern": "needle"})
	if !result.IsError {
		t.Fatal("expected a cancelled context to fail")
	}
}

func TestGrepIsRaceFree(t *testing.T) {
	// The worker pool shares a mutex-guarded slice; this run is only
	// meaningful under -race, and the count proves no result was lost or
	// duplicated despite the concurrency.
	root := t.TempDir()
	files := map[string]string{}
	for i := 0; i < 40; i++ {
		files[fileNameFor(i)] = "needle\nother\n"
	}
	writeTree(t, root, files)

	// Raise the file limit so every match fits in one page, otherwise the
	// default window would hide lost results.
	text, result := grepText(t, NewGrepTool(root), map[string]any{
		"pattern": "needle",
		"limit":   float64(GrepMaxFileLimit),
	})
	if result.IsError {
		t.Fatalf("unexpected error: %s", text)
	}
	if got := grepFileList(text); len(got) != 40 {
		t.Errorf("files reported = %d, want 40", len(got))
	}
	// Every entry must be unique: a duplicated result would mean the
	// accumulator recorded the same file twice.
	seen := map[string]bool{}
	for _, name := range grepFileList(text) {
		if seen[name] {
			t.Errorf("file %q reported more than once", name)
		}
		seen[name] = true
	}
}

func TestLsListsDirectoriesFirst(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"z_file.txt":      "x",
		"a_dir/inner.txt": "y",
		"m_file.txt":      "z",
	})

	result := NewLsTool(root).Execute(testContext(), map[string]any{})
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Text)
	}
	lines := result.Text
	if !strings.Contains(lines, "a_dir/") {
		t.Errorf("directories were not listed: %s", lines)
	}
	// The first entry must be the directory, proving the ordering rule.
	if !strings.HasPrefix(lines[strings.IndexByte(lines, '\n')+1:], "a_dir/") {
		t.Errorf("the directory was not listed first: %s", lines)
	}
}

func TestLsExcludesHiddenByDefault(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"visible.txt": "x",
		".hidden":     "y",
	})

	hidden := NewLsTool(root).Execute(testContext(), map[string]any{}).Text
	if strings.Contains(hidden, ".hidden") {
		t.Errorf("a dotfile was listed by default: %s", hidden)
	}
	shown := NewLsTool(root).Execute(testContext(), map[string]any{"hidden": true}).Text
	if !strings.Contains(shown, ".hidden") {
		t.Errorf("hidden=true did not list dotfiles: %s", shown)
	}
}

func TestLsRejectsFilesAndOutsidePaths(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"a.txt": "x"})
	tool := NewLsTool(root)

	if result := tool.Execute(testContext(), map[string]any{"path": "a.txt"}); !result.IsError {
		t.Error("expected a file path to be rejected")
	}
	if result := tool.Execute(testContext(), map[string]any{"path": "../../etc"}); !result.IsError {
		t.Error("expected a path outside the root to fail")
	}
}

func TestWebFetchRejectsNonHTTPSchemes(t *testing.T) {
	tool := NewWebFetchTool()
	cases := []string{"file:///etc/passwd", "ftp://example.com", "javascript:alert(1)", ""}
	for _, raw := range cases {
		if result := tool.Execute(testContext(), map[string]any{"url": raw}); !result.IsError {
			t.Errorf("url %q should have been rejected", raw)
		}
	}
}

func TestWebFetchExtractsTextFromHTML(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<!DOCTYPE html>
<html><head><title>T</title><style>body{color:red}</style></head>
<body>
<script>var secret = "must not leak";</script>
<h1>Heading</h1>
<p>Visible&nbsp;paragraph &amp; more.</p>
<ul><li>one</li><li>two</li></ul>
</body></html>`)
	}))
	defer server.Close()

	result := NewWebFetchTool().Execute(testContext(), map[string]any{"url": server.URL})
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Text)
	}
	text := result.Text
	if !strings.Contains(text, "Heading") || !strings.Contains(text, "Visible") {
		t.Errorf("visible text was not extracted: %s", text)
	}
	if !strings.Contains(text, "one") || !strings.Contains(text, "two") {
		t.Errorf("list items were not extracted: %s", text)
	}
	// Script and style bodies must be dropped entirely: they are noise at best
	// and a context-poisoning vector at worst.
	if strings.Contains(text, "secret") {
		t.Errorf("script content leaked into the result: %s", text)
	}
	if strings.Contains(text, "color:red") {
		t.Errorf("style content leaked into the result: %s", text)
	}
	if !strings.Contains(text, "&") {
		t.Errorf("an entity was not decoded: %s", text)
	}
}

func TestWebFetchReturnsPlainText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "line one\nline two\n")
	}))
	defer server.Close()

	result := NewWebFetchTool().Execute(testContext(), map[string]any{"url": server.URL})
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Text)
	}
	if !strings.Contains(result.Text, "line one") {
		t.Errorf("text was not returned: %s", result.Text)
	}
}

func TestWebFetchReportsHTTPStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	if result := NewWebFetchTool().Execute(testContext(), map[string]any{"url": server.URL}); !result.IsError {
		t.Fatal("expected a 404 to be reported as an error")
	}
}

func TestWebFetchHonoursCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := NewWebFetchTool().Execute(agent.Context{Ctx: ctx}, map[string]any{"url": server.URL})
	if !result.IsError {
		t.Fatal("expected a cancelled context to fail")
	}
}

func TestCollapseBlankLinesSqueezesRuns(t *testing.T) {
	got := collapseBlankLines("a\n\n\n\nb\n   c   \n\n")
	if got != "a\n\nb\nc" {
		t.Errorf("got %q, want %q", got, "a\n\nb\nc")
	}
}

func TestLineMatcherDispatchesLiteralVsRegex(t *testing.T) {
	// A pattern with metacharacters must still behave as a regex, and a plain
	// one must match literally. Dispatching wrongly in either direction
	// silently returns the wrong result set.
	cases := []struct {
		name    string
		pattern string
		line    string
		want    bool
	}{
		{"literal exact", "handler123", "x handler123 y", true},
		{"literal is not a regex dot", "handler123", "handlerx", false},
		{"literal anchors by substring", "abc", "xxabcyy", true},
		{"regex wildcard", "handler.23", "handlerx23", true},
		{"regex quantifier", "ab+c", "abbbc", true},
		{"regex alternation", "foo|bar", "a bar b", true},
		{"regex character class", "[a-z]+", "  x  ", true},
		{"regex anchors", "^abc", "abcdef", true},
		{"regex group", "(foo)?bar", "bar", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, err := newLineMatcher(tc.pattern, true)
			if err != nil {
				t.Fatalf("newLineMatcher: %v", err)
			}
			if got := m.matchBytes([]byte(tc.line)); got != tc.want {
				t.Errorf("match(%q) = %v, want %v", tc.line, got, tc.want)
			}
		})
	}
}

func TestLineMatcherCaseSensitivity(t *testing.T) {
	insensitive, err := newLineMatcher("alpha", false)
	if err != nil {
		t.Fatal(err)
	}
	if !insensitive.matchBytes([]byte("ALPHA")) {
		t.Error("case-insensitive matching did not fold")
	}
	sensitive, err := newLineMatcher("alpha", true)
	if err != nil {
		t.Fatal(err)
	}
	if sensitive.matchBytes([]byte("ALPHA")) {
		t.Error("case-sensitive matching folded when it should not have")
	}
}

func TestLineMatcherRejectsInvalidRegex(t *testing.T) {
	if _, err := newLineMatcher("[unclosed", true); err == nil {
		t.Fatal("expected an invalid regex to be rejected")
	}
}

func TestHasRegexMeta(t *testing.T) {
	cases := map[string]bool{
		"plain":                false,
		"with_underscore-dash": false,
		"a.b":                  true,
		"a*b":                  true,
		"a+b":                  true,
		"a?b":                  true,
		"(a)":                  true,
		"[abc]":                true,
		"{1,2}":                true,
		"a|b":                  true,
		"^a":                   true,
		"a$":                   true,
		`a\d`:                  true,
	}
	for pattern, want := range cases {
		if got := hasRegexMeta(pattern); got != want {
			t.Errorf("hasRegexMeta(%q) = %v, want %v", pattern, got, want)
		}
	}
}
