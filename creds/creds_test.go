package creds

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The parser is a security boundary: it reads files the shell executes, so
// every test below pins either a value it must extract or a shape it must
// refuse.

func TestParserReadsPlainAndExported(t *testing.T) {
	result := ParseShellAssignments("OPENROUTER_API_KEY=sk-live\n export X=y\n")
	if result.Values["OPENROUTER_API_KEY"] != "sk-live" || result.Values["X"] != "y" {
		t.Errorf("values = %v, want both assignments", result.Values)
	}
	if len(result.Skipped) != 0 {
		t.Errorf("skipped = %v, want none", result.Skipped)
	}
}

func TestParserHandlesQuotesCommentsAndWhitespace(t *testing.T) {
	result := ParseShellAssignments("# comment\n" +
		"  A='single quoted'  \n" +
		"B=\"double quoted\"\n" +
		"C=bare # trailing comment\n" +
		"D=\"escaped \\\" quote\"\n" +
		"E='it''s'\n")
	// E holds an embedded quote the single-quote rule cannot parse safely.
	if result.Values["A"] != "single quoted" {
		t.Errorf("A = %q", result.Values["A"])
	}
	if result.Values["B"] != "double quoted" {
		t.Errorf("B = %q", result.Values["B"])
	}
	if result.Values["C"] != "bare" {
		t.Errorf("C = %q, want the trailing comment stripped", result.Values["C"])
	}
	if result.Values["D"] != `escaped " quote` {
		t.Errorf("D = %q, want the escape honoured", result.Values["D"])
	}
	if _, ok := result.Values["E"]; ok {
		t.Errorf("E = %q, want embedded quotes refused", result.Values["E"])
	}
	if len(result.Skipped) == 0 {
		t.Error("no skips reported for the refused line")
	}
}

func TestParserRefusesDynamicExpressions(t *testing.T) {
	// Each of these would execute something if handed to a shell. The parser
	// must skip every one — and say so.
	text := "A=$(curl evil)\n" +
		"B=`whoami`\n" +
		"C=${HOME}/x\n" +
		"D=\"$HOME/x\"\n" +
		"E='$HOME/x'\n" +
		"F=pass$word\n" +
		"alias ll='ls'\n" +
		"echo hi\n" +
		"if [ x ]; then\n"
	result := ParseShellAssignments(text)
	if len(result.Values) != 0 {
		t.Errorf("values = %v, want nothing dynamic parsed", result.Values)
	}
	if len(result.Skipped) < 8 {
		t.Errorf("skipped = %d lines, want every dynamic line reported", len(result.Skipped))
	}
}

func TestParserSupportsFishSet(t *testing.T) {
	result := ParseShellAssignments("set -gx OLLAMA_API_KEY secret-value\n")
	if result.Values["OLLAMA_API_KEY"] != "secret-value" {
		t.Errorf("values = %v, want the fish export", result.Values)
	}
}

func TestParserLaterAssignmentsWin(t *testing.T) {
	result := ParseShellAssignments("A=first\nA=second\n")
	if result.Values["A"] != "second" {
		t.Errorf("A = %q, want shell last-wins semantics", result.Values["A"])
	}
}

// Precedence: explicit > file > environment > shell files.

func testResolver() *Resolver {
	return &Resolver{
		Explicit: map[string]string{},
		Getenv:   func(string) string { return "" },
		Shell:    map[string]string{},
	}
}

func TestPrecedenceExplicitFirst(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(filepath.Join(dir, "creds.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Set("groq", "file-key"); err != nil {
		t.Fatal(err)
	}
	r := testResolver()
	r.File = store
	r.Explicit["groq"] = "explicit-key"
	r.Getenv = func(name string) string { return "env-key" }
	r.Shell["GROQ_API_KEY"] = "shell-key"

	key, source, err := r.Resolve("groq")
	if err != nil || key != "explicit-key" || source != "explicit" {
		t.Errorf("got (%q, %q, %v), want the explicit key to win", key, source, err)
	}
}

func TestPrecedenceFileBeforeEnv(t *testing.T) {
	dir := t.TempDir()
	store, _ := Open(filepath.Join(dir, "creds.json"))
	_ = store.Set("groq", "file-key")
	r := testResolver()
	r.File = store
	r.Getenv = func(string) string { return "env-key" }

	key, source, err := r.Resolve("groq")
	if err != nil || key != "file-key" || source != "credential file" {
		t.Errorf("got (%q, %q, %v), want the file before the environment", key, source, err)
	}
}

func TestPrecedenceEnvBeforeShell(t *testing.T) {
	r := testResolver()
	r.Getenv = func(name string) string {
		if name == "GROQ_API_KEY" {
			return "env-key"
		}
		return ""
	}
	r.Shell["GROQ_API_KEY"] = "shell-key"
	r.ShellSource = "~/.zshrc"

	key, source, err := r.Resolve("groq")
	if err != nil || key != "env-key" || !strings.Contains(source, "GROQ_API_KEY") {
		t.Errorf("got (%q, %q, %v), want env with its variable named", key, source, err)
	}
}

func TestPrecedenceShellLastWithSource(t *testing.T) {
	r := testResolver()
	r.Shell["GROQ_API_KEY"] = "shell-key"
	r.ShellSource = "~/.zshrc"

	key, source, err := r.Resolve("groq")
	if err != nil || key != "shell-key" || !strings.Contains(source, "~/.zshrc") {
		t.Errorf("got (%q, %q, %v), want the shell value with its file named", key, source, err)
	}
}

func TestMissingCredentialNamesTheVariables(t *testing.T) {
	r := testResolver()
	_, _, err := r.Resolve("groq")
	if err == nil || !strings.Contains(err.Error(), "GROQ_API_KEY") {
		t.Errorf("err = %v, want it to name the variable to set", err)
	}
}

func TestUnknownProviderIsAnError(t *testing.T) {
	r := testResolver()
	if _, _, err := r.Resolve("fictional"); err == nil {
		t.Error("an unknown provider resolved")
	}
}

func TestLocalOllamaNeedsNoKey(t *testing.T) {
	// Resolving a requirement that does not exist would invent one: local
	// Ollama serves without authentication.
	r := testResolver()
	key, source, err := r.Resolve("ollama")
	if err != nil || key != "" || source != "none required" {
		t.Errorf("got (%q, %q, %v), want no key required", key, source, err)
	}
}

func TestSharedOpenCodeCredential(t *testing.T) {
	// One operator, one credential, two endpoints: zen and go must resolve
	// the same variable, never different ones.
	r := testResolver()
	r.Getenv = func(name string) string {
		if name == "OPENCODE_API_KEY" {
			return "shared-key"
		}
		return ""
	}
	for _, provider := range []string{"opencode-zen", "opencode-go"} {
		key, _, err := r.Resolve(provider)
		if err != nil || key != "shared-key" {
			t.Errorf("%s: got (%q, %v), want the shared key", provider, key, err)
		}
	}
}

// Store: permissions, atomicity, delete.

func TestStoreWritesOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "creds.json")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Set("groq", "secret"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("perm = %o, want 0600: group and other must not read secrets", perm)
	}
}

func TestStoreRoundTripsAndDeletes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "creds.json")
	store, _ := Open(path)
	_ = store.Set("a", "1")
	_ = store.Set("b", "2")
	if err := store.Delete("a"); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Get("a"); ok {
		t.Error("deleted credential still resolves")
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if key, ok := reopened.Get("b"); !ok || key != "2" {
		t.Error("the store did not survive a reopen")
	}
}

func TestStoreRejectsMalformedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "creds.json")
	if err := os.WriteFile(path, []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Error("a malformed credential file read as empty: saved keys would silently vanish")
	}
}

// Redaction.

func TestScrubRedactsLongestFirst(t *testing.T) {
	// A key containing a shorter secret must not partially survive the
	// replacement order.
	text := "key=abc123xyz and fragment abc123"
	got := Scrub(text, []string{"abc123", "abc123xyz"})
	if strings.Contains(got, "abc123") {
		t.Errorf("scrubbed = %q, want no secret bytes surviving", got)
	}
	if !strings.Contains(got, "[redacted]") {
		t.Errorf("scrubbed = %q, want the marker", got)
	}
}

func TestScrubSkipsBlanks(t *testing.T) {
	if got := Scrub("nothing here", []string{"", "  "}); got != "nothing here" {
		t.Errorf("scrubbed = %q, want blanks ignored, not matched everywhere", got)
	}
}
