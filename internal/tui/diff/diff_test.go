package diff

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// --- parser -----------------------------------------------------------------

const modifiedDiff = `diff --git a/keep.txt b/keep.txt
index 422c2b7..c1b5a1f 100644
--- a/keep.txt
+++ b/keep.txt
@@ -1,3 +1,4 @@ func thing()
 a
-b
+B
 c
+d
`

func TestParseUnifiedMapsLineNumbersAndKinds(t *testing.T) {
	chunks, err := ParseUnifiedString(modifiedDiff)
	if err != nil {
		t.Fatalf("ParseUnified: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("chunks = %d, want 1", len(chunks))
	}
	chunk := chunks[0]
	if !strings.Contains(chunk.Header, "func thing()") {
		t.Errorf("header = %q, want it to keep the section text", chunk.Header)
	}
	if chunk.OldStart != 1 || chunk.OldLines != 3 || chunk.NewStart != 1 || chunk.NewLines != 4 {
		t.Errorf("range = -%d,%d +%d,%d, want -1,3 +1,4", chunk.OldStart, chunk.OldLines, chunk.NewStart, chunk.NewLines)
	}

	type want struct {
		kind     DiffLineType
		old, new int
		content  string
	}
	expected := []want{
		{LineContext, 1, 1, "a"},
		{LineDeletion, 2, 0, "b"},
		{LineAddition, 0, 2, "B"},
		{LineContext, 3, 3, "c"},
		{LineAddition, 0, 4, "d"},
	}
	if len(chunk.Lines) != len(expected) {
		t.Fatalf("lines = %d, want %d", len(chunk.Lines), len(expected))
	}
	for i, exp := range expected {
		got := chunk.Lines[i]
		if got.Type != exp.kind || got.OldLineNum != exp.old || got.NewLineNum != exp.new || got.Content != exp.content {
			t.Errorf("line %d = {%v %d %d %q}, want {%v %d %d %q}",
				i, got.Type, got.OldLineNum, got.NewLineNum, got.Content, exp.kind, exp.old, exp.new, exp.content)
		}
	}
}

func TestRenderRowsReportElidedGaps(t *testing.T) {
	// Between two hunks the viewport must show how many unchanged lines were
	// folded away, and the first hunk must report the lines before it.
	chunks := []DiffChunk{
		{Header: "@@ -10,2 +10,2 @@", OldStart: 10, NewStart: 10, OldLines: 2, NewLines: 2,
			Lines: []DiffLine{{Type: LineContext, OldLineNum: 10, NewLineNum: 10}}},
		{Header: "@@ -20,2 +20,2 @@", OldStart: 20, NewStart: 20, OldLines: 2, NewLines: 2,
			Lines: []DiffLine{{Type: LineContext, OldLineNum: 20, NewLineNum: 20}}},
	}
	rows := buildRenderRows(chunks)
	var gaps []int
	for _, row := range rows {
		if row.kind == rowGap {
			gaps = append(gaps, row.gap)
		}
	}
	// Leading gap: 10 - 1 = 9. Middle gap: 20 - (10+2) = 8.
	if len(gaps) != 2 || gaps[0] != 9 || gaps[1] != 8 {
		t.Fatalf("gaps = %v, want [9 8]", gaps)
	}

	// A new file starts at old line 0: there is no leading gap to report.
	newFile := buildRenderRows([]DiffChunk{{Header: "@@ -0,0 +1,1 @@", OldStart: 0, NewStart: 1, NewLines: 1}})
	for _, row := range newFile {
		if row.kind == rowGap {
			t.Errorf("new file reported a gap of %d, want none", row.gap)
		}
	}
}

func TestParseUnifiedMarksNoNewlineWithoutShiftingNumbers(t *testing.T) {
	// A trailing "\ No newline at end of file" annotates the row above and must
	// not consume a line number, or every later row would be off by one.
	text := "diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1,2 +1,2 @@\n a\n-b" + "\n" + `\ No newline at end of file` + "\n+b\n"
	chunks, err := ParseUnifiedString(text)
	if err != nil {
		t.Fatalf("ParseUnified: %v", err)
	}
	lines := chunks[0].Lines
	if len(lines) != 4 {
		t.Fatalf("lines = %d, want 4 (context, delete, meta, add)", len(lines))
	}
	if lines[2].Type != LineMeta {
		t.Errorf("line 2 type = %v, want LineMeta", lines[2].Type)
	}
	if lines[3].Type != LineAddition || lines[3].NewLineNum != 2 {
		t.Errorf("line 3 = {%v new=%d}, want addition on new line 2", lines[3].Type, lines[3].NewLineNum)
	}
}

func TestParseUnifiedHighlightsTheChangedWordOnly(t *testing.T) {
	oldLine := "let value = computeTotal(items)"
	newLine := "let value = computeSum(items)"
	text := "diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n-" + oldLine + "\n+" + newLine + "\n"

	chunks, err := ParseUnifiedString(text)
	if err != nil {
		t.Fatalf("ParseUnified: %v", err)
	}
	lines := chunks[0].Lines
	del, add := lines[0], lines[1]
	if del.Type != LineDeletion || add.Type != LineAddition {
		t.Fatalf("line types = %v/%v, want deletion/addition", del.Type, add.Type)
	}

	if got := spanText(del, del.Highlights); got != "computeTotal" {
		t.Errorf("deleted highlight = %q, want the changed token only", got)
	}
	if got := spanText(add, add.Highlights); got != "computeSum" {
		t.Errorf("added highlight = %q, want the changed token only", got)
	}
}

func TestWordDiffLeavesIdenticalLinesUnhighlighted(t *testing.T) {
	// A pair that differs only in whitespace still highlights the difference,
	// but an identical pair must not mark the whole line as changed.
	oldSpans, newSpans := wordDiff("same line", "same line")
	if len(oldSpans) != 0 || len(newSpans) != 0 {
		t.Errorf("identical lines = %v/%v, want no highlights", oldSpans, newSpans)
	}
}

func TestWordDiffFallsBackOnOversizedLines(t *testing.T) {
	// A minified line far past the token cap must terminate with a bounded,
	// whole-line span rather than run a quadratic diff.
	oldLine := strings.Repeat("a ", maxIntraLineTokens)
	newLine := strings.Repeat("b ", maxIntraLineTokens)
	oldSpans, newSpans := wordDiff(oldLine, newLine)
	if len(oldSpans) != 1 || oldSpans[0].Start != 0 || oldSpans[0].End != len(oldLine) {
		t.Errorf("oversized old spans = %v, want one full-line span", oldSpans)
	}
	if len(newSpans) != 1 || newSpans[0].End != len(newLine) {
		t.Errorf("oversized new spans = %v, want one full-line span", newSpans)
	}
}

func TestParseStripsTerminalEscapes(t *testing.T) {
	// A crafted file must not be able to move the cursor or recolour the
	// screen through the inspector: control characters are removed when the
	// line is stored, before any span offsets are computed.
	text := "diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n-\x1b[31mred\x1b[0m\n+\x1b[2Jcleared\n"
	chunks, err := ParseUnifiedString(text)
	if err != nil {
		t.Fatalf("ParseUnified: %v", err)
	}
	for _, line := range chunks[0].Lines {
		if strings.ContainsRune(line.Content, '\x1b') {
			t.Errorf("escape survived sanitization: %q", line.Content)
		}
	}
	if got := chunks[0].Lines[1].Content; got != "[2Jcleared" {
		t.Errorf("added content = %q, want the ESC dropped and its bytes left inert", got)
	}
}

func TestParseUnifiedHandlesEmptyTreeHeader(t *testing.T) {
	// The shape a diff against the empty tree produces: no base side, every
	// line an addition. The parser must read the zero start and count without
	// treating the missing side as a malformed header.
	text := "diff --git a/x b/x\nnew file mode 100644\nindex 0000000..abc\n--- /dev/null\n+++ b/x\n@@ -0,0 +1,2 @@\n+one\n+two\n"
	chunks, err := ParseUnifiedString(text)
	if err != nil {
		t.Fatalf("ParseUnified: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("chunks = %d, want 1", len(chunks))
	}
	c := chunks[0]
	if c.OldStart != 0 || c.OldLines != 0 || c.NewStart != 1 || c.NewLines != 2 {
		t.Errorf("range = -%d,%d +%d,%d, want -0,0 +1,2", c.OldStart, c.OldLines, c.NewStart, c.NewLines)
	}
	if len(c.Lines) != 2 || c.Lines[0].Type != LineAddition || c.Lines[1].NewLineNum != 2 {
		t.Errorf("lines = %+v, want two additions numbered 1 and 2", c.Lines)
	}
}

func TestParseUnifiedDropsDegenerateHunkHeader(t *testing.T) {
	// A hunk with no lines on either side carries nothing to show; it must not
	// become a bare header row in the viewport.
	text := "diff --git a/x b/x\n@@ -0,0 +0,0 @@\n"
	chunks, err := ParseUnifiedString(text)
	if err != nil {
		t.Fatalf("ParseUnified: %v", err)
	}
	if len(chunks) != 0 {
		t.Errorf("chunks = %d, want the degenerate hunk dropped", len(chunks))
	}
}

// spanText returns the concatenation of a line's highlighted spans, for tests.
func spanText(line DiffLine, spans []HighlightSpan) string {
	var sb strings.Builder
	for _, span := range spans {
		sb.WriteString(line.Content[span.Start:span.End])
	}
	return sb.String()
}

// --- tree -------------------------------------------------------------------

func TestBuildTreeCollapsesSingleChildChainsAndAggregates(t *testing.T) {
	files := []FileChange{
		{Path: "cmd/ohgo/run.go", Status: StatusModified, Added: 3, Removed: 1},
		{Path: "cmd/ohgo/main.go", Status: StatusModified, Added: 2, Removed: 0},
		{Path: "tui/layout.go", Status: StatusDeleted, Added: 0, Removed: 5},
		{Path: "README.md", Status: StatusAdded, Added: 10},
	}
	roots := BuildTree(files)

	// Directories sort before files.
	if len(roots) != 3 {
		t.Fatalf("roots = %d, want 3", len(roots))
	}
	if roots[0].Name != "cmd/ohgo" || !roots[0].IsDir {
		t.Errorf("root 0 = %q dir=%v, want collapsed dir cmd/ohgo", roots[0].Name, roots[0].IsDir)
	}
	if roots[0].StatAdd != 5 || roots[0].StatDel != 1 {
		t.Errorf("cmd/ohgo stats = +%d -%d, want +5 -1 aggregated", roots[0].StatAdd, roots[0].StatDel)
	}
	if roots[1].Name != "tui" || !roots[1].IsDir {
		t.Errorf("root 1 = %q dir=%v, want dir tui", roots[1].Name, roots[1].IsDir)
	}
	if roots[2].Name != "README.md" || roots[2].IsDir {
		t.Errorf("root 2 = %q dir=%v, want file README.md", roots[2].Name, roots[2].IsDir)
	}
	if roots[2].Status != StatusAdded {
		t.Errorf("README status = %q, want A", roots[2].Status)
	}
}

func TestVisibleRowsRespectsCollapse(t *testing.T) {
	files := []FileChange{{Path: "tui/layout.go"}, {Path: "tui/view.go"}}
	roots := BuildTree(files)
	if got := len(VisibleRows(roots)); got != 3 {
		t.Fatalf("expanded rows = %d, want 3 (dir + 2 files)", got)
	}
	roots[0].Expanded = false
	if got := len(VisibleRows(roots)); got != 1 {
		t.Errorf("collapsed rows = %d, want 1", got)
	}
}

// --- git driver (real repository) -------------------------------------------

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "--quiet"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
	} {
		gitRun(t, dir, args...)
	}
	return dir
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestGitChangedFilesReportsStatusAndStats(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, dir, "keep.txt", "a\nb\nc\n")
	writeFile(t, dir, "del.txt", "gone\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-qm", "init")

	// One modification, one deletion, one addition, plus a nested change.
	writeFile(t, dir, "keep.txt", "a\nB\nc\nd\n")
	writeFile(t, dir, "sub/nested.txt", "new\nlines\n")
	gitRun(t, dir, "add", "-A")
	if err := os.Remove(filepath.Join(dir, "del.txt")); err != nil {
		t.Fatal(err)
	}

	g := NewGit(dir)
	changes, err := g.ChangedFiles(context.Background(), DefaultSpec())
	if err != nil {
		t.Fatalf("ChangedFiles: %v", err)
	}
	byPath := map[string]FileChange{}
	for _, change := range changes {
		byPath[change.Path] = change
	}

	if got := byPath["keep.txt"]; got.Status != StatusModified || got.Added != 2 || got.Removed != 1 {
		t.Errorf("keep.txt = %+v, want M +2 -1", got)
	}
	if got := byPath["del.txt"]; got.Status != StatusDeleted || got.Removed != 1 {
		t.Errorf("del.txt = %+v, want D -1", got)
	}
	if got := byPath["sub/nested.txt"]; got.Status != StatusAdded || got.Added != 2 {
		t.Errorf("sub/nested.txt = %+v, want A +2", got)
	}
}

func TestGitFileDiffParsesHunks(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, dir, "keep.txt", "a\nb\nc\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-qm", "init")
	writeFile(t, dir, "keep.txt", "a\nB\nc\nd\n")

	g := NewGit(dir)
	chunks, err := g.FileDiff(context.Background(), DefaultSpec(), "keep.txt")
	if err != nil {
		t.Fatalf("FileDiff: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("chunks = %d, want 1", len(chunks))
	}
	var additions, deletions int
	for _, line := range chunks[0].Lines {
		switch line.Type {
		case LineAddition:
			additions++
		case LineDeletion:
			deletions++
		}
	}
	if additions != 2 || deletions != 1 {
		t.Errorf("hunk = +%d -%d, want +2 -1", additions, deletions)
	}
}

func TestGitChangedFilesHonoursRevisionRange(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, dir, "a.txt", "one\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-qm", "one")
	writeFile(t, dir, "a.txt", "two\n")
	gitRun(t, dir, "commit", "-aqm", "two")

	g := NewGit(dir)
	// HEAD~1..HEAD names one changed file between the two commits.
	changes, err := g.ChangedFiles(context.Background(), Spec{Rev: "HEAD~1..HEAD"})
	if err != nil {
		t.Fatalf("ChangedFiles: %v", err)
	}
	if len(changes) != 1 || changes[0].Path != "a.txt" {
		t.Fatalf("range changes = %+v, want a.txt", changes)
	}
}

// TestGitChangedFilesInUnbornRepo is the regression for the fatal
// "ambiguous argument 'HEAD'" a fresh `git init` produced: with no commit there
// is no HEAD, so /diff must compare against the empty tree and list the
// untracked files as additions.
func TestGitChangedFilesInUnbornRepo(t *testing.T) {
	dir := initRepo(t) // no commit: HEAD does not exist
	writeFile(t, dir, "hello.txt", "one\ntwo\nthree\n")
	writeFile(t, dir, "sub/new.txt", "x\n")

	g := NewGit(dir)
	changes, err := g.ChangedFiles(context.Background(), DefaultSpec())
	if err != nil {
		t.Fatalf("ChangedFiles on an unborn repo: %v", err)
	}
	byPath := changesByPath(changes)
	if got := byPath["hello.txt"]; got.Status != StatusAdded || got.Added != 3 {
		t.Errorf("hello.txt = %+v, want A +3", got)
	}
	if got := byPath["sub/new.txt"]; got.Status != StatusAdded || got.Added != 1 {
		t.Errorf("sub/new.txt = %+v, want A +1 (untracked dirs must expand)", got)
	}

	// The hunk view must render the whole file as additions; git cannot do
	// this itself, so the driver streams the file from disk.
	chunks, err := g.FileDiff(context.Background(), DefaultSpec(), "hello.txt")
	if err != nil {
		t.Fatalf("FileDiff on an unborn repo: %v", err)
	}
	if additions := countKind(chunks, LineAddition); additions != 3 {
		t.Errorf("hello.txt additions = %d, want 3", additions)
	}
}

// A staged file in an unborn repo is a tracked path, so it diffs through git
// against the empty tree rather than through the disk fallback.
func TestGitStagedFileInUnbornRepoDiffsAgainstEmptyTree(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, dir, "staged.txt", "a\nb\n")
	gitRun(t, dir, "add", "staged.txt")

	g := NewGit(dir)
	chunks, err := g.FileDiff(context.Background(), DefaultSpec(), "staged.txt")
	if err != nil {
		t.Fatalf("FileDiff: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("chunks = %d, want 1", len(chunks))
	}
	if chunks[0].Header != "@@ -0,0 +1,2 @@" {
		t.Errorf("header = %q, want the empty-tree shape", chunks[0].Header)
	}
	if got := countKind(chunks, LineAddition); got != 2 {
		t.Errorf("additions = %d, want 2", got)
	}
}

// Untracked files belong in the worktree view even once HEAD exists: git diff
// never reports them, so a newly created file would otherwise be invisible.
func TestGitChangedFilesIncludesUntrackedWithHead(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, dir, "tracked.txt", "a\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-qm", "init")
	writeFile(t, dir, "fresh.txt", "new\nline\n")

	g := NewGit(dir)
	changes, err := g.ChangedFiles(context.Background(), DefaultSpec())
	if err != nil {
		t.Fatalf("ChangedFiles: %v", err)
	}
	got := changesByPath(changes)["fresh.txt"]
	if got.Status != StatusAdded || got.Added != 2 {
		t.Errorf("fresh.txt = %+v, want A +2", got)
	}
}

// An empty repository is a clean state, not an error: no files, no failure.
func TestGitChangedFilesEmptyRepoIsClean(t *testing.T) {
	dir := initRepo(t)
	g := NewGit(dir)
	changes, err := g.ChangedFiles(context.Background(), DefaultSpec())
	if err != nil {
		t.Fatalf("ChangedFiles on an empty repo: %v", err)
	}
	if len(changes) != 0 {
		t.Errorf("changes = %+v, want none", changes)
	}
}

// A file with no trailing newline gets the same marker git would emit, so the
// disk fallback matches a committed diff byte for byte.
func TestGitUntrackedFileWithoutTrailingNewline(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, dir, "tail.txt", "no newline at end")

	g := NewGit(dir)
	chunks, err := g.FileDiff(context.Background(), DefaultSpec(), "tail.txt")
	if err != nil {
		t.Fatalf("FileDiff: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("chunks = %d, want 1", len(chunks))
	}
	lines := chunks[0].Lines
	if len(lines) != 2 || lines[0].Type != LineAddition || lines[1].Type != LineMeta {
		t.Errorf("lines = %+v, want one addition then the no-newline marker", lines)
	}
}

// A binary untracked file is flagged rather than scanned as text.
func TestGitUntrackedBinaryIsFlagged(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, dir, "blob.bin", "bin\x00ary\ndata\n")

	g := NewGit(dir)
	changes, err := g.ChangedFiles(context.Background(), DefaultSpec())
	if err != nil {
		t.Fatalf("ChangedFiles: %v", err)
	}
	got := changesByPath(changes)["blob.bin"]
	if got.Status != StatusAdded || !got.Binary {
		t.Errorf("blob.bin = %+v, want A with binary=true", got)
	}
}

// changesByPath indexes a change list for lookup.
func changesByPath(changes []FileChange) map[string]FileChange {
	byPath := make(map[string]FileChange, len(changes))
	for _, change := range changes {
		byPath[change.Path] = change
	}
	return byPath
}

// countKind counts the lines of one kind across every chunk.
func countKind(chunks []DiffChunk, kind DiffLineType) int {
	n := 0
	for _, chunk := range chunks {
		for _, line := range chunk.Lines {
			if line.Type == kind {
				n++
			}
		}
	}
	return n
}

// --- spec parsing -----------------------------------------------------------

func TestParseArgsForms(t *testing.T) {
	if spec, err := ParseArgs(nil); err != nil || spec.Rev != "HEAD" {
		t.Errorf("no args = %+v err=%v, want HEAD", spec, err)
	}
	if spec, err := ParseArgs([]string{"main"}); err != nil || spec.Rev != "main" {
		t.Errorf("one arg = %+v err=%v, want main", spec, err)
	}
	if spec, err := ParseArgs([]string{"main..dev"}); err != nil || spec.Rev != "main..dev" {
		t.Errorf("range = %+v err=%v, want main..dev", spec, err)
	}
	if _, err := ParseArgs([]string{"main", "dev"}); err == nil {
		t.Error("two args accepted, want a usage error")
	}
}

// --- model ------------------------------------------------------------------

// fakeFetcher scripts the two git reads and records the calls it receives, so a
// test can assert the inspector's laziness directly.
type fakeFetcher struct {
	changes []FileChange
	diffs   map[string][]DiffChunk
	calls   []string
}

func (f *fakeFetcher) ChangedFiles(context.Context, Spec) ([]FileChange, error) {
	f.calls = append(f.calls, "files")
	return f.changes, nil
}

func (f *fakeFetcher) FileDiff(_ context.Context, _ Spec, path string) ([]DiffChunk, error) {
	f.calls = append(f.calls, "diff:"+path)
	return f.diffs[path], nil
}

func countingCalls(f *fakeFetcher, prefix string) int {
	n := 0
	for _, call := range f.calls {
		if strings.HasPrefix(call, prefix) {
			n++
		}
	}
	return n
}

// loadedModel builds a model whose initial file list has arrived.
func loadedModel(t *testing.T, fetcher *fakeFetcher, width, height int) *Model {
	t.Helper()
	m := New(fetcher, DefaultSpec(), "test diff")
	m.SetSize(width, height)
	msg := m.Init()()
	if _, ok := msg.(filesLoadedMsg); !ok {
		t.Fatalf("Init msg = %T, want filesLoadedMsg", msg)
	}
	m.Update(msg)
	return m
}

// applyCmd runs a command's message back through the model, so a test exercises
// the same async path the program does.
func applyCmd(t *testing.T, m *Model, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		return
	}
	if msg := cmd(); msg != nil {
		m.Update(msg)
	}
}

func TestInitialLoadFetchesNamesOnly(t *testing.T) {
	// The whole point of the laziness contract: opening /diff must never read
	// a file's hunks.
	fetcher := &fakeFetcher{changes: []FileChange{{Path: "a.go", Status: StatusModified}}}
	loadedModel(t, fetcher, 100, 24)
	if got := countingCalls(fetcher, "diff:"); got != 0 {
		t.Fatalf("diff fetches after open = %d, want 0", got)
	}
	if got := countingCalls(fetcher, "files"); got != 1 {
		t.Fatalf("file fetches = %d, want 1", got)
	}
}

func TestEmptyRepoRendersCleanState(t *testing.T) {
	// An unborn or empty repository reaches the model as an empty change list.
	// The pane must say so plainly and never let a git error string through.
	fetcher := &fakeFetcher{}
	m := loadedModel(t, fetcher, 100, 24)
	view := m.View()
	if !strings.Contains(view, "no changes") {
		t.Errorf("empty change list does not read as a clean state:\n%s", view)
	}
	if strings.Contains(view, "fatal:") || strings.Contains(view, "ambiguous") {
		t.Errorf("git error text leaked into the view:\n%s", view)
	}
}

func TestSelectingAFileFetchesOnceAndCaches(t *testing.T) {
	fetcher := &fakeFetcher{
		changes: []FileChange{{Path: "a.go", Status: StatusModified}},
		diffs:   map[string][]DiffChunk{"a.go": {{Header: "@@ -1 +1 @@", Lines: []DiffLine{{Type: LineAddition, NewLineNum: 1, Content: "hello"}}}}},
	}
	m := loadedModel(t, fetcher, 100, 24)

	// Enter on the highlighted file loads and displays it.
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	applyCmd(t, m, cmd)
	if got := countingCalls(fetcher, "diff:a.go"); got != 1 {
		t.Fatalf("diff fetches = %d, want 1", got)
	}
	if !strings.Contains(m.View(), "hello") {
		t.Fatalf("view does not show the selected file's content:\n%s", m.View())
	}

	// Re-selecting the same file must come from the cache, not git.
	m.startLoad("a.go")
	if got := countingCalls(fetcher, "diff:a.go"); got != 1 {
		t.Fatalf("diff fetches after revisit = %d, want the cache to answer", got)
	}
}

func TestViewportRendersOnlyTheVisibleWindow(t *testing.T) {
	// 60 added lines, a short terminal: the last line must not be in the
	// rendered frame until the viewport scrolls to it.
	var lines []DiffLine
	for i := 0; i < 60; i++ {
		lines = append(lines, DiffLine{Type: LineAddition, NewLineNum: i + 1, Content: "line-" + pad(i) + "-marker"})
	}
	fetcher := &fakeFetcher{
		changes: []FileChange{{Path: "big.go", Status: StatusAdded, Added: 60}},
		diffs:   map[string][]DiffChunk{"big.go": {{Header: "@@ -0,0 +1,60 @@", OldStart: 0, NewStart: 1, OldLines: 0, NewLines: 60, Lines: lines}}},
	}
	m := loadedModel(t, fetcher, 100, 12)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	applyCmd(t, m, cmd)

	view := m.View()
	if !strings.Contains(view, "line-00-marker") {
		t.Fatalf("visible window misses the first line:\n%s", view)
	}
	if strings.Contains(view, "line-59-marker") {
		t.Fatalf("off-screen line rendered before scrolling:\n%s", view)
	}
	// Every emitted frame fits the terminal height.
	if got := len(strings.Split(view, "\n")); got != 12 {
		t.Fatalf("frame rows = %d, want 12", got)
	}

	// Scroll to the bottom: the last line is now rendered, the first is gone.
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}})
	view = m.View()
	if !strings.Contains(view, "line-59-marker") {
		t.Fatalf("scrolled window misses the last line:\n%s", view)
	}
	if strings.Contains(view, "line-00-marker") {
		t.Fatalf("first line still rendered after scrolling to the end:\n%s", view)
	}
}

func TestStaleDiffResultIsDropped(t *testing.T) {
	// A slow fetch for a file the reader has left must not replace the pane.
	fetcher := &fakeFetcher{
		changes: []FileChange{{Path: "a.go"}, {Path: "b.go"}},
		diffs: map[string][]DiffChunk{
			"a.go": {{Header: "@@ -1 +1 @@", Lines: []DiffLine{{Type: LineAddition, NewLineNum: 1, Content: "from-a"}}}},
			"b.go": {{Header: "@@ -1 +1 @@", Lines: []DiffLine{{Type: LineAddition, NewLineNum: 1, Content: "from-b"}}}},
		},
	}
	m := loadedModel(t, fetcher, 100, 24)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter}) // selects a.go
	applyCmd(t, m, cmd)
	if !strings.Contains(m.View(), "from-a") {
		t.Fatal("a.go was not installed as current")
	}
	// Move to b.go and load it; it becomes current.
	m.Update(tea.KeyMsg{Type: tea.KeyTab}) // focus back to the tree
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	applyCmd(t, m, cmd)
	if !strings.Contains(m.View(), "from-b") {
		t.Fatal("b.go was not installed as current")
	}

	// A late result for a.go arrives now: it must be ignored.
	m.Update(diffLoadedMsg{path: "a.go", chunks: fetcher.diffs["a.go"]})
	if strings.Contains(m.View(), "from-a") {
		t.Fatalf("stale a.go result was installed over b.go:\n%s", m.View())
	}
}

func TestFilterNarrowsToMatchingFiles(t *testing.T) {
	fetcher := &fakeFetcher{changes: []FileChange{
		{Path: "tui/app.go"}, {Path: "tui/view.go"}, {Path: "cmd/main.go"},
	}}
	m := loadedModel(t, fetcher, 100, 24)
	before := len(m.rows)

	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	for _, r := range "view" {
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if !m.FilterActive() {
		t.Fatal("filter not active after typing")
	}
	if len(m.rows) >= before {
		t.Fatalf("filtered rows = %d, want fewer than %d", len(m.rows), before)
	}
	for _, row := range m.rows {
		if row.Node.IsDir {
			continue
		}
		if !strings.Contains(row.Node.Path, "view") {
			t.Errorf("row %q survived a view filter", row.Node.Path)
		}
	}

	m.ClearFilter()
	if len(m.rows) != before {
		t.Errorf("rows after clearing = %d, want %d", len(m.rows), before)
	}
}

// pad renders a two-digit line index for the viewport test's markers.
func pad(n int) string {
	if n < 10 {
		return "0" + string(rune('0'+n))
	}
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}
