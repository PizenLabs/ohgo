package ast

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// goFixture writes a Go source file and returns its path.
func goFixture(t *testing.T, source string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture.go")
	if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

const fixtureSource = `// Package fixture is a test file.
package fixture

import (
	"fmt"
	"strings"
)

// Version is the exported constant.
const Version = "1.0"

var debug bool

// Widget describes a widget.
type Widget struct {
	Name string
}

// helper is unexported.
func helper(x int) int {
	return x * 2
}

// Render draws the widget.
func (w Widget) Render() string {
	return w.Name
}

// Fetch loads a remote value.
func Fetch(ctx string, url string) (string, error) {
	return fmt.Sprintf("%s:%s", ctx, url), nil
}
`

func TestGoBackendFindsFuncs(t *testing.T) {
	path := goFixture(t, fixtureSource)
	backend := GoBackend{}

	matches, err := backend.Search(path, Options{Kinds: []Kind{KindFunc}})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(matches) != 3 {
		t.Fatalf("matches = %d, want 3 (helper, Render, Fetch)", len(matches))
	}

	var names []string
	for _, m := range matches {
		names = append(names, m.Name)
	}
	for _, want := range []string{"helper", "Widget.Render", "Fetch"} {
		found := false
		for _, name := range names {
			if name == want {
				found = true
			}
		}
		if !found {
			t.Errorf("missing %q in %v", want, names)
		}
	}
}

func TestMatchCarriesDocAndSignature(t *testing.T) {
	// A search result is only useful if it says what the declaration *is*;
	// otherwise the caller must open the file anyway.
	path := goFixture(t, fixtureSource)
	matches, err := GoBackend{}.Search(path, Options{Kinds: []Kind{KindFunc}})
	if err != nil {
		t.Fatal(err)
	}

	var fetch *Match
	for i, m := range matches {
		if m.Name == "Fetch" {
			fetch = &matches[i]
		}
	}
	if fetch == nil {
		t.Fatal("Fetch was not found")
	}
	if !strings.Contains(fetch.Doc, "Fetch loads a remote value") {
		t.Errorf("doc = %q, want the declaration comment", fetch.Doc)
	}
	if !strings.Contains(fetch.Signature, "string, error") {
		t.Errorf("signature = %q, want the result types", fetch.Signature)
	}
	if fetch.Line <= 0 || fetch.EndLine < fetch.Line {
		t.Errorf("line range = %d..%d, want an ascending range", fetch.Line, fetch.EndLine)
	}
}

func TestMethodIsQualifiedByReceiver(t *testing.T) {
	// A bare method name is ambiguous across types, so the receiver is part of
	// the name.
	path := goFixture(t, fixtureSource)
	matches, _ := GoBackend{}.Search(path, Options{})

	var found bool
	for _, m := range matches {
		if m.Name == "Widget.Render" {
			found = true
		}
	}
	if !found {
		t.Error("a method was not qualified by its receiver")
	}
}

func TestFindsTypesVarsAndConsts(t *testing.T) {
	path := goFixture(t, fixtureSource)

	for _, kind := range []Kind{KindType, KindVar, KindConst} {
		matches, err := GoBackend{}.Search(path, Options{Kinds: []Kind{kind}})
		if err != nil {
			t.Fatal(err)
		}
		if len(matches) == 0 {
			t.Errorf("no %s declarations found", kind)
		}
	}

	kinds, err := GoBackend{}.Search(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[Kind]int{}
	for _, m := range kinds {
		seen[m.Kind]++
	}
	if seen[KindConst] != 1 || seen[KindVar] != 1 || seen[KindType] != 1 {
		t.Errorf("declaration counts = %v, want one each", seen)
	}
}

func TestBlankIdentifierIsSkipped(t *testing.T) {
	// The blank identifier declares nothing usable, so listing it wastes a line
	// of the caller's output.
	path := goFixture(t, "package p\n\nvar (\n\t_ = 1\n\treal = 2\n)\n")
	matches, err := GoBackend{}.Search(path, Options{Kinds: []Kind{KindVar}})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range matches {
		if m.Name == "_" {
			t.Error("the blank identifier was reported as a declaration")
		}
	}
}

func TestSearchFiltersByKinds(t *testing.T) {
	path := goFixture(t, fixtureSource)
	only, err := GoBackend{}.Search(path, Options{Kinds: []Kind{KindType}})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range only {
		if m.Kind != KindType {
			t.Errorf("kind = %q, want only type declarations", m.Kind)
		}
	}
	if len(only) == 0 {
		t.Error("filtering to types returned nothing")
	}
}

func TestSupportedOnlyClaimsGoFiles(t *testing.T) {
	backend := GoBackend{}
	for _, path := range []string{"a.go", "deep/nested/File.GO", "x.go.txt"} {
		_ = path
	}
	if !backend.Supported("a.go") {
		t.Error("a .go file was not claimed")
	}
	if !backend.Supported("File.GO") {
		t.Error("extension matching should be case-insensitive")
	}
	for _, path := range []string{"a.rb", "a.py", "a.ts", "Makefile", ""} {
		if backend.Supported(path) {
			t.Errorf("%q was claimed by the Go backend", path)
		}
	}
}

func TestRegistryResolvesTheBackend(t *testing.T) {
	r := New()
	if r.BackendFor("a.go") == nil {
		t.Fatal("no backend claimed a Go file")
	}
	if r.BackendFor("a.rb") != nil {
		t.Error("a backend claimed a file it cannot parse")
	}

	if _, err := r.Search("a.rb", Options{}); err == nil {
		t.Error("an unsupported file should fail with a clear message")
	}
}

func TestRegistryReportsSupportedExtensions(t *testing.T) {
	got := New().SupportedExtensions()
	if !strings.Contains(got, ".go") {
		t.Errorf("extensions = %q, want it to mention .go", got)
	}
}

func TestSearchRejectsBrokenSource(t *testing.T) {
	// A parse failure must surface, not return an empty result: an empty result
	// would read as "no declarations", which is a lie.
	path := goFixture(t, "package broken\n\nfunc ( {\n")
	if _, err := (GoBackend{}).Search(path, Options{}); err == nil {
		t.Fatal("expected a parse error")
	}
}

func TestSearchFindsNothingInAnEmptyFile(t *testing.T) {
	path := goFixture(t, "package empty\n")
	matches, err := GoBackend{}.Search(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Errorf("matches = %+v, want none", matches)
	}
}

func TestGenericFuncsAndMethods(t *testing.T) {
	// A generic parameter list must not crash the signature renderer, and the
	// receiver of a generic method is still named.
	source := "package p\n\nfunc Map[T any](in []T, fn func(T) T) []T { return in }\n\nfunc (s Set[T]) Add(v T) {}\n"
	path := goFixture(t, source)

	matches, err := GoBackend{}.Search(path, Options{Kinds: []Kind{KindFunc}})
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 2 {
		t.Fatalf("matches = %d, want 2", len(matches))
	}
	names := map[string]bool{}
	for _, m := range matches {
		names[m.Name] = true
	}
	if !names["Map"] {
		t.Errorf("generic function %v missing", names)
	}
	if !names["Set.Add"] {
		t.Errorf("generic method %v missing, want the receiver named", names)
	}
}

func TestSearchIsRepeatable(t *testing.T) {
	// The file set is created per call, so a second search must not see stale
	// positions from the first.
	path := goFixture(t, fixtureSource)
	first, _ := GoBackend{}.Search(path, Options{Kinds: []Kind{KindFunc}})
	second, _ := GoBackend{}.Search(path, Options{Kinds: []Kind{KindFunc}})
	if len(first) != len(second) {
		t.Fatalf("repeat search found %d then %d", len(first), len(second))
	}
	for i := range first {
		if first[i].Line != second[i].Line {
			t.Errorf("match %d line = %d then %d", i, first[i].Line, second[i].Line)
		}
	}
}

func TestSearchFindsImports(t *testing.T) {
	path := goFixture(t, fixtureSource)
	matches, err := GoBackend{}.Search(path, Options{Kinds: []Kind{KindImport}})
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) < 2 {
		t.Fatalf("imports = %d, want at least fmt and strings", len(matches))
	}
	var names []string
	for _, m := range matches {
		names = append(names, m.Name)
	}
	for _, want := range []string{"fmt", "strings"} {
		if !contains3(names, want) {
			t.Errorf("import %q missing from %v", want, names)
		}
	}
}

func contains3(values []string, target string) bool {
	for _, v := range values {
		if v == target {
			return true
		}
	}
	return false
}

func TestRegistrySearchMatchesTheBackend(t *testing.T) {
	// The registry is a convenience; it must not change what the backend returns.
	path := goFixture(t, fixtureSource)
	direct, _ := GoBackend{}.Search(path, Options{})
	viaRegistry, err := New().Search(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(direct) != len(viaRegistry) {
		t.Errorf("registry returned %d, backend returned %d", len(viaRegistry), len(direct))
	}
	for i := range direct {
		if direct[i].Name != viaRegistry[i].Name {
			t.Errorf("match %d = %q vs %q", i, direct[i].Name, viaRegistry[i].Name)
		}
	}
}

func BenchmarkGoSearch(b *testing.B) {
	source := strings.Repeat("// doc\nfunc fn(i int) int { return i }\n\n", 2000)
	path := filepath.Join(b.TempDir(), "big.go")
	if err := os.WriteFile(path, []byte("package big\n\n"+source), 0o644); err != nil {
		b.Fatal(err)
	}
	for i := 0; i < b.N; i++ {
		_, _ = GoBackend{}.Search(path, Options{})
	}
}
