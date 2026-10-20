package ast

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// indexFixture writes files into a fresh repository root and returns it.
func indexFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func mustIndex(t *testing.T, root string) *Index {
	t.Helper()
	idx, err := NewIndex(root)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	return idx
}

// lookupCount returns the number of exact matches or fails the test.
func lookupCount(t *testing.T, idx *Index, name string) int {
	t.Helper()
	got, err := idx.Lookup(name)
	if err != nil {
		t.Fatalf("Lookup(%q): %v", name, err)
	}
	return len(got)
}

func TestIndexLookupReturnsStableCoordinate(t *testing.T) {
	// The coordinate is what lets a caller open the declaration without
	// searching: it must name the repository-relative path, the package,
	// the symbol, its kind, and the exact line range.
	root := indexFixture(t, map[string]string{
		"tools/grep.go": "package tools\n\n// Fetch loads a remote value.\nfunc Fetch(url string) string {\n\treturn url\n}\n",
	})
	idx := mustIndex(t, root)

	got, err := idx.Lookup("Fetch")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Lookup = %d results, want exactly 1", len(got))
	}
	sym := got[0]
	if sym.Path != "tools/grep.go" {
		t.Errorf("Path = %q, want repository-relative tools/grep.go", sym.Path)
	}
	if sym.Package != "tools" {
		t.Errorf("Package = %q, want tools", sym.Package)
	}
	if sym.Name != "Fetch" || sym.Kind != KindFunc {
		t.Errorf("Name/Kind = %q/%q, want Fetch/func", sym.Name, sym.Kind)
	}
	if sym.Line != 4 || sym.EndLine != 6 {
		t.Errorf("lines = %d..%d, want 4..6", sym.Line, sym.EndLine)
	}
}

func TestIndexLookupIsAmbiguousAcrossFiles(t *testing.T) {
	// One name declared in two files must return both, ordered by path, so
	// no caller silently receives an arbitrary winner.
	root := indexFixture(t, map[string]string{
		"b.go": "package main\n\nfunc alpha() {}\n",
		"a.go": "package main\n\nfunc alpha() {}\n",
	})
	idx := mustIndex(t, root)

	got, err := idx.Lookup("alpha")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Lookup = %d results, want 2", len(got))
	}
	if got[0].Path != "a.go" || got[1].Path != "b.go" {
		t.Errorf("order = %q, %q, want a.go then b.go", got[0].Path, got[1].Path)
	}
}

func TestIndexLookupMissesNothing(t *testing.T) {
	root := indexFixture(t, map[string]string{
		"a.go": "package main\n\nfunc alpha() {}\n",
	})
	got, err := mustIndex(t, root).Lookup("nope")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Lookup = %d results, want 0", len(got))
	}
}

func TestIndexLookupAcceptsQualifiedName(t *testing.T) {
	// The model may know the package but not the file. A qualified name is
	// still an exact coordinate, not a substring search.
	root := indexFixture(t, map[string]string{
		"a.go": "package tools\n\ntype Widget struct{}\n\nfunc (w Widget) Render() {}\n",
	})
	idx := mustIndex(t, root)

	for name, wantKind := range map[string]Kind{
		"tools.Widget":        KindType,
		"tools.Widget.Render": KindMethod,
	} {
		got, err := idx.Lookup(name)
		if err != nil {
			t.Fatalf("Lookup(%q): %v", name, err)
		}
		if len(got) != 1 || got[0].Kind != wantKind || got[0].Path != "a.go" {
			t.Errorf("Lookup(%q) = %+v, want one %s in a.go", name, got, wantKind)
		}
	}
}

func TestIndexFileListsDeclarationsInLineOrder(t *testing.T) {
	root := indexFixture(t, map[string]string{
		"a.go": "package tools\n\n// Widget describes a widget.\ntype Widget struct{}\n\n// Render draws the widget.\nfunc (w Widget) Render() string {\n\treturn \"\"\n}\n\nvar debug bool\n\nconst Version = \"1\"\n",
	})
	idx := mustIndex(t, root)

	syms, err := idx.File("a.go")
	if err != nil {
		t.Fatalf("File: %v", err)
	}
	// package + type + method + var + const = 5
	if len(syms) != 5 {
		t.Fatalf("File = %d symbols, want 5", len(syms))
	}
	for i := 1; i < len(syms); i++ {
		if syms[i].Line < syms[i-1].Line {
			t.Fatalf("symbols out of line order at index %d", i)
		}
	}
	seen := map[Kind]int{}
	for _, s := range syms {
		seen[s.Kind]++
		if s.Path != "a.go" || s.Package != "tools" {
			t.Errorf("symbol %q has Path=%q Package=%q", s.Name, s.Path, s.Package)
		}
	}
	for _, kind := range []Kind{KindPackage, KindType, KindMethod, KindVar, KindConst} {
		if seen[kind] != 1 {
			t.Errorf("kind %q appears %d times, want 1", kind, seen[kind])
		}
	}
}

func TestIndexMethodCarriesReceiver(t *testing.T) {
	root := indexFixture(t, map[string]string{
		"a.go": "package tools\n\ntype Widget struct{}\n\nfunc (w Widget) Render() string {\n\treturn \"\"\n}\n\nfunc (w *Widget) Draw() {}\n",
	})
	syms, err := mustIndex(t, root).File("a.go")
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, s := range syms {
		if s.Kind == KindMethod {
			names[s.Name] = true
		}
	}
	for _, want := range []string{"Widget.Render", "Widget.Draw"} {
		if !names[want] {
			t.Errorf("method %q missing from %v", want, names)
		}
	}
}

func TestIndexClassifiesEachPhaseOneKind(t *testing.T) {
	// The taxonomy is the contract the model reads: a wrong kind sends it
	// looking for the wrong construct. One declaration of each supported
	// kind pins the mapping.
	root := indexFixture(t, map[string]string{
		"a.go": "package kinds\n\ntype Widget struct{}\n\nfunc Top() {}\n\nfunc (w Widget) Render() {}\n\nvar Debug bool\n\nconst Version = \"1\"\n",
	})
	idx := mustIndex(t, root)

	cases := []struct {
		name string
		kind Kind
	}{
		{"kinds", KindPackage},
		{"Top", KindFunc},
		{"Widget.Render", KindMethod},
		{"Widget", KindType},
		{"Debug", KindVar},
		{"Version", KindConst},
	}
	for _, tc := range cases {
		got, err := idx.Lookup(tc.name)
		if err != nil {
			t.Fatalf("Lookup(%q): %v", tc.name, err)
		}
		if len(got) != 1 {
			t.Errorf("Lookup(%q) = %d results, want 1", tc.name, len(got))
			continue
		}
		if got[0].Kind != tc.kind {
			t.Errorf("Lookup(%q).Kind = %q, want %q", tc.name, got[0].Kind, tc.kind)
		}
	}
}

func TestIndexRetainsCoordinatesNotSourceText(t *testing.T) {
	// The index is disposable derived state. Keeping doc comments and
	// signatures per symbol would duplicate source into resident memory for
	// fields no Phase 1 query returns.
	root := indexFixture(t, map[string]string{
		"a.go": "package main\n\n// Alpha does a thing.\nfunc Alpha(value string) string {\n\treturn value\n}\n",
	})
	got, err := mustIndex(t, root).Lookup("Alpha")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("Lookup = %d results, want 1", len(got))
	}
	if got[0].Doc != "" || got[0].Signature != "" {
		t.Errorf("index retained source text: Doc=%q Signature=%q", got[0].Doc, got[0].Signature)
	}
}

func TestIndexLineRangesForEachKind(t *testing.T) {
	// A coordinate is only useful if its range is exact: the model reads
	// start_line..end_line. Pin each kind's range against a known file.
	root := indexFixture(t, map[string]string{
		"a.go": "package kinds\n" +
			"\n" +
			"type Widget struct{}\n" +
			"\n" +
			"func Top() {}\n" +
			"\n" +
			"func (w Widget) Render() {}\n" +
			"\n" +
			"var Debug bool\n" +
			"\n" +
			"const Version = \"1\"\n",
	})
	got, err := mustIndex(t, root).File("a.go")
	if err != nil {
		t.Fatal(err)
	}

	want := map[string][2]int{
		"kinds":         {1, 1},
		"Widget":        {3, 3},
		"Top":           {5, 5},
		"Widget.Render": {7, 7},
		"Debug":         {9, 9},
		"Version":       {11, 11},
	}
	if len(got) != len(want) {
		t.Fatalf("File = %d symbols, want %d", len(got), len(want))
	}
	for _, s := range got {
		r, ok := want[s.Name]
		if !ok {
			t.Errorf("unexpected symbol %q", s.Name)
			continue
		}
		if s.Line != r[0] || s.EndLine != r[1] {
			t.Errorf("%s lines = %d..%d, want %d..%d", s.Name, s.Line, s.EndLine, r[0], r[1])
		}
	}
}

func TestIndexPackageGroupsFiles(t *testing.T) {
	root := indexFixture(t, map[string]string{
		"a.go": "package tools\n\nfunc Alpha() {}\n",
		"b.go": "package tools\n\nfunc Beta() {}\n",
		"c.go": "package other\n\nfunc Gamma() {}\n",
	})
	got, err := mustIndex(t, root).Package("tools")
	if err != nil {
		t.Fatalf("Package: %v", err)
	}
	names := map[string]bool{}
	for _, s := range got {
		names[s.Name] = true
	}
	for _, want := range []string{"Alpha", "Beta", "tools"} {
		if !names[want] {
			t.Errorf("package tools is missing %q (got %v)", want, names)
		}
	}
	if names["Gamma"] {
		t.Error("package tools leaked a symbol from package other")
	}
}

func TestIndexDetectsModifiedFile(t *testing.T) {
	root := indexFixture(t, map[string]string{
		"a.go": "package main\n\nfunc alpha() {}\n",
	})
	idx := mustIndex(t, root)
	if lookupCount(t, idx, "alpha") != 1 {
		t.Fatal("precondition: alpha is indexed")
	}

	// Rename the declaration on disk. The next lookup must see the new
	// coordinate without any manual rebuild: the index refreshes itself.
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package main\n\nfunc alphaRenamed() {}\nfunc beta() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !idx.Stale() {
		t.Error("Stale = false after modifying an indexed file")
	}
	if lookupCount(t, idx, "alpha") != 0 {
		t.Error("stale coordinate for alpha survived a modification")
	}
	if lookupCount(t, idx, "beta") != 1 {
		t.Error("new declaration beta was not discovered after modification")
	}
}

func TestIndexDetectsSameSizeRewrite(t *testing.T) {
	// Size alone is not a change signal. A same-length rename still must
	// invalidate the coordinate, so the test keeps the byte count equal and
	// moves only the mtime.
	root := indexFixture(t, map[string]string{
		"a.go": "package main\n\nfunc alpha() {}\n",
	})
	path := filepath.Join(root, "a.go")
	old := time.Now().Add(-time.Minute)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	idx := mustIndex(t, root)
	if lookupCount(t, idx, "alpha") != 1 {
		t.Fatal("precondition: alpha is indexed")
	}

	if err := os.WriteFile(path, []byte("package main\n\nfunc gamma() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if lookupCount(t, idx, "gamma") != 1 {
		t.Error("same-size rewrite was not discovered")
	}
	if lookupCount(t, idx, "alpha") != 0 {
		t.Error("same-size rewrite kept a stale coordinate")
	}
}

func TestIndexDetectsDeletedFile(t *testing.T) {
	root := indexFixture(t, map[string]string{
		"a.go": "package main\n\nfunc alpha() {}\n",
		"b.go": "package main\n\nfunc beta() {}\n",
	})
	idx := mustIndex(t, root)

	if err := os.Remove(filepath.Join(root, "a.go")); err != nil {
		t.Fatal(err)
	}
	if !idx.Stale() {
		t.Error("Stale = false after deleting an indexed file")
	}
	if lookupCount(t, idx, "alpha") != 0 {
		t.Error("deleted file still contributes symbols")
	}
	if lookupCount(t, idx, "beta") != 1 {
		t.Error("surviving file lost its symbols after a sibling was deleted")
	}
}

func TestIndexDetectsCreatedFile(t *testing.T) {
	root := indexFixture(t, map[string]string{
		"a.go": "package main\n\nfunc alpha() {}\n",
	})
	idx := mustIndex(t, root)

	if err := os.WriteFile(filepath.Join(root, "b.go"), []byte("package main\n\nfunc beta() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !idx.Stale() {
		t.Error("Stale = false after creating an indexed file")
	}
	if lookupCount(t, idx, "beta") != 1 {
		t.Error("new file was not discovered")
	}
}

func TestIndexIgnoresNonGoFiles(t *testing.T) {
	// Irrelevant files must neither poison the snapshot nor appear as symbols.
	root := indexFixture(t, map[string]string{
		"a.go": "package main\n\nfunc alpha() {}\n",
	})
	idx := mustIndex(t, root)

	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("func Fake() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if idx.Stale() {
		t.Error("a non-Go file marked the Go index stale")
	}
	if lookupCount(t, idx, "Fake") != 0 {
		t.Error("text content leaked into the symbol index")
	}
}

func TestIndexSurvivesMalformedSource(t *testing.T) {
	// One broken file must not take down its neighbours, and asking for it
	// must report the parse failure rather than an empty listing.
	root := indexFixture(t, map[string]string{
		"good.go": "package main\n\nfunc alpha() {}\n",
		"bad.go":  "package broken\n\nfunc ( {\n",
	})
	idx := mustIndex(t, root)

	if lookupCount(t, idx, "alpha") != 1 {
		t.Error("a malformed sibling hid a valid symbol")
	}
	if _, err := idx.File("bad.go"); err == nil {
		t.Error("expected a parse error for the malformed file")
	}
	if got := idx.ParseFailures(); got != 1 {
		t.Errorf("ParseFailures = %d, want 1", got)
	}
}

func TestIndexFileEnforcesContainment(t *testing.T) {
	root := indexFixture(t, map[string]string{
		"a.go": "package main\n\nfunc alpha() {}\n",
	})

	if _, err := mustIndex(t, root).File("../outside.go"); err == nil {
		t.Error("a path escaping the root resolved to an indexed file")
	}
	if _, err := mustIndex(t, root).File("nope.go"); err == nil {
		t.Error("a nonexistent file resolved to an indexed file")
	}
	abs := filepath.Join(root, "a.go")
	if _, err := mustIndex(t, root).File(abs); err != nil {
		t.Errorf("an absolute in-root path should resolve: %v", err)
	}
}

func TestIndexResultsAreDeterministic(t *testing.T) {
	root := indexFixture(t, map[string]string{
		"b.go": "package main\n\nfunc beta() {}\nfunc alpha() {}\n",
		"a.go": "package main\n\nfunc gamma() {}\n",
	})
	idx := mustIndex(t, root)

	first, err := idx.Symbols()
	if err != nil {
		t.Fatalf("Symbols: %v", err)
	}
	second, err := idx.Symbols()
	if err != nil {
		t.Fatalf("Symbols: %v", err)
	}
	if len(first) != len(second) {
		t.Fatalf("repeated Symbols() returned %d then %d", len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("result %d differs between calls", i)
		}
	}
}

func TestIndexPrunesNonSourceDirectories(t *testing.T) {
	// vendor and testdata are not package source the compiler sees. Hidden
	// directories are excluded for the same reason: a derived index should
	// not surface tool state as repository symbols.
	root := indexFixture(t, map[string]string{
		"main.go":               "package main\n\nfunc Visible() {}\n",
		"vendor/dep/dep.go":     "package dep\n\nfunc Vendored() {}\n",
		"testdata/fixture.go":   "package fixture\n\nfunc Fixture() {}\n",
		".cache/generated/x.go": "package generated\n\nfunc Hidden() {}\n",
	})
	idx := mustIndex(t, root)

	for _, name := range []string{"Vendored", "Fixture", "Hidden"} {
		if got := lookupCount(t, idx, name); got != 0 {
			t.Errorf("Lookup(%q) = %d, want 0 (pruned directory leaked)", name, got)
		}
	}
	if got := lookupCount(t, idx, "Visible"); got != 1 {
		t.Errorf("Lookup(Visible) = %d, want 1", got)
	}
}

func TestIndexFollowsRootSymlink(t *testing.T) {
	// A working directory is often reached through a symlink (macOS /var is
	// the common case). The root itself may resolve, but files inside it
	// must still be indexed and their coordinates relative to that root.
	realRoot := indexFixture(t, map[string]string{
		"a.go": "package main\n\nfunc alpha() {}\n",
	})
	link := filepath.Join(t.TempDir(), "work")
	if err := os.Symlink(realRoot, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	idx := mustIndex(t, link)
	if got := lookupCount(t, idx, "alpha"); got != 1 {
		t.Fatalf("Lookup(alpha) = %d, want 1 through a symlinked root", got)
	}
	if _, err := idx.File("a.go"); err != nil {
		t.Errorf("File(a.go) through a symlinked root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(realRoot, "b.go"), []byte("package main\n\nfunc beta() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := lookupCount(t, idx, "beta"); got != 1 {
		t.Error("freshness did not observe a file created under a symlinked root")
	}
}

func TestIndexDoesNotFollowSymlinkOutOfRoot(t *testing.T) {
	// WalkDir does not follow symlinks, so a link cannot smuggle an outside
	// declaration into the repository coordinate space.
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "secret.go")
	if err := os.WriteFile(outsideFile, []byte("package secret\n\nfunc Leaked() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := indexFixture(t, map[string]string{
		"a.go": "package main\n\nfunc alpha() {}\n",
	})
	if err := os.Symlink(outsideFile, filepath.Join(root, "link.go")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "outside")); err != nil {
		t.Skipf("directory symlinks unavailable: %v", err)
	}

	idx := mustIndex(t, root)
	if got := lookupCount(t, idx, "Leaked"); got != 0 {
		t.Errorf("Lookup(Leaked) = %d, want 0: a symlink escaped the root", got)
	}
	if _, err := idx.File("link.go"); err == nil {
		t.Error("a symlinked file resolved to an indexed coordinate")
	}
}

func TestIndexConcurrentQueriesStayConsistent(t *testing.T) {
	// Tool calls run concurrently in the agent loop. The index must not
	// lose symbols, duplicate them, or race while refreshing.
	root := indexFixture(t, map[string]string{
		"a.go": "package main\n\nfunc alpha() {}\n",
		"b.go": "package main\n\nfunc beta() {}\n",
	})
	idx := mustIndex(t, root)

	const workers = 8
	const iterations = 25
	errCh := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				got, err := idx.Lookup("alpha")
				if err != nil {
					errCh <- err
					return
				}
				if len(got) != 1 || got[0].Path != "a.go" {
					errCh <- fmt.Errorf("concurrent Lookup = %+v, want one a.go coordinate", got)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
}
