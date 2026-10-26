package main

import (
	"github.com/PizenLabs/ohgo/catalog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// providerKeyVars mirrors ai's credential table so the keyless tests hold on
// any machine. If ai gains a provider, add its variables here too.
var providerKeyVars = []string{
	"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "OPENAI_API_KEY",
	"AZURE_OPENAI_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY", "GROQ_API_KEY",
	"MISTRAL_API_KEY", "DEEPSEEK_API_KEY", "OPENROUTER_API_KEY",
	"TOGETHER_API_KEY", "FIREWORKS_API_KEY", "XAI_API_KEY", "CEREBRAS_API_KEY",
}

// clearProviderKeys removes every credential from the environment.
func clearProviderKeys(t *testing.T) {
	t.Helper()
	for _, name := range providerKeyVars {
		_ = os.Unsetenv(name)
	}
}

// TestPlanInitPrefersTheCatalogDefault proves the default survives setup when
// its credential is present: a wizard that switched models on a configured
// machine would be untrustworthy.
func TestPlanInitPrefersTheCatalogDefault(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-key")

	plan, err := planInit("", "")
	if err != nil {
		t.Fatalf("planInit: %v", err)
	}
	if plan.model != catalog.DefaultRef {
		t.Errorf("model = %q, want the catalog default %q", plan.model, catalog.DefaultRef)
	}
	if plan.keyHint != "" {
		t.Errorf("keyHint = %q, want none when the key is present", plan.keyHint)
	}
}

// TestPlanInitFallsBackToACredentialedProvider proves a machine without the
// default's key still gets a working setup rather than a model it cannot call.
func TestPlanInitFallsBackToACredentialedProvider(t *testing.T) {
	clearProviderKeys(t)
	t.Setenv("GROQ_API_KEY", "test-key")

	plan, err := planInit("", "")
	if err != nil {
		t.Fatalf("planInit: %v", err)
	}
	if !strings.HasPrefix(plan.model, "groq/") {
		t.Errorf("model = %q, want a groq model: the only credentialed provider", plan.model)
	}
	found := false
	for _, provider := range plan.providers {
		if provider == "groq" {
			found = true
		}
	}
	if !found {
		t.Errorf("providers = %v, want groq listed", plan.providers)
	}
}

// TestPlanInitRefusesAnUnknownModel keeps a typo from becoming the default:
// every later run would fail with the catalog error instead of this one.
func TestPlanInitRefusesAnUnknownModel(t *testing.T) {
	if _, err := planInit("anthropic/no-such-model", ""); err == nil {
		t.Error("an unknown --model was accepted into the setup")
	}
}

// TestPlanInitNormalizesTheTheme proves a typo degrades to dark rather than
// failing the setup over a cosmetic choice.
func TestPlanInitNormalizesTheTheme(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-key")

	plan, err := planInit("", "LIGHT")
	if err != nil {
		t.Fatalf("planInit: %v", err)
	}
	if plan.theme != "light" {
		t.Errorf("theme = %q, want light", plan.theme)
	}
	plan, err = planInit("", "solarized")
	if err != nil {
		t.Fatalf("planInit: %v", err)
	}
	if plan.theme != "dark" {
		t.Errorf("theme = %q, want a typo to degrade to dark", plan.theme)
	}
}

// TestRunInitWritesSettings is the end-to-end contract: a fresh HOME gets a
// working settings file, and the output says what is left.
func TestRunInitWritesSettings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "test-key")

	cmd := rootCmd()
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"init"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("init: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(home, ".ohgo", "settings.json"))
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	if !strings.Contains(string(data), catalog.DefaultRef) {
		t.Errorf("settings = %s, want the resolved model written", data)
	}
	if !strings.Contains(out.String(), "ohgo doctor") {
		t.Errorf("output = %q, want the next step named", out.String())
	}
}

// TestRunInitRefusesToClobber is the safety contract: an existing setup is
// never overwritten without --force, because silently replacing someone's
// configuration is worse than a failed first run.
func TestRunInitRefusesToClobber(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "test-key")

	if err := os.MkdirAll(filepath.Join(home, ".ohgo"), 0o755); err != nil {
		t.Fatal(err)
	}
	before := `{"model":"custom/keep-me"}`
	if err := os.WriteFile(filepath.Join(home, ".ohgo", "settings.json"), []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := rootCmd()
	cmd.SetArgs([]string{"init"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("init overwrote settings without --force")
	}
	data, err := os.ReadFile(filepath.Join(home, ".ohgo", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != before {
		t.Errorf("settings = %q, want the original untouched", data)
	}

	force := rootCmd()
	force.SetArgs([]string{"init", "--force"})
	if err := force.Execute(); err != nil {
		t.Fatalf("init --force: %v", err)
	}
	after, _ := os.ReadFile(filepath.Join(home, ".ohgo", "settings.json"))
	if string(after) == before {
		t.Error("--force did not overwrite")
	}
}

// TestRunInitNamesTheMissingCredential proves the output tells a keyless
// machine exactly what to do, not just what is wrong.
func TestRunInitNamesTheMissingCredential(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	clearProviderKeys(t)

	cmd := rootCmd()
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"init"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("init: %v", err)
	}
	if !strings.Contains(out.String(), "ANTHROPIC_API_KEY") {
		t.Errorf("output = %q, want the missing variable named", out.String())
	}
}

// TestDocsGeneratesReference proves the harness ships its own documentation:
// man pages plus markdown, derived from the live command tree.
func TestDocsGeneratesReference(t *testing.T) {
	dir := t.TempDir()

	cmd := rootCmd()
	cmd.SetArgs([]string{"docs", "--dir", dir})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("docs: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var man, md int
	for _, entry := range entries {
		switch {
		case strings.HasSuffix(entry.Name(), ".1"):
			man++
		case strings.HasSuffix(entry.Name(), ".md"):
			md++
		}
	}
	if man == 0 {
		t.Error("no man pages generated")
	}
	if md == 0 {
		t.Error("no markdown pages generated")
	}
	// The tree must include the harness commands, or the reference silently
	// documents a subset of the binary.
	for _, want := range []string{"ohgo-init.1", "ohgo-doctor.1", "ohgo.1"} {
		if _, err := os.Stat(filepath.Join(dir, want)); err != nil {
			t.Errorf("missing %s", want)
		}
	}
}

// Generated reference covers runtime slash commands, so /mcp is documented
// alongside the CLI tree from the same registry the session uses.
func TestDocsIncludeRuntimeMCPCommands(t *testing.T) {
	dir := t.TempDir()
	cmd := rootCmd()
	cmd.SetArgs([]string{"docs", "--dir", dir})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("docs: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "runtime-commands.md"))
	if err != nil {
		t.Fatalf("read runtime reference: %v", err)
	}
	for _, want := range []string{"/mcp", "/mcp inspect <server>", "/mcp refresh [server]"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("runtime reference missing %q:\n%s", want, data)
		}
	}
}

// `ohgo -h` points at the runtime MCP surface without inventing a top-level
// `ohgo mcp` command.
func TestRootHelpNamesMCPRuntimeCommand(t *testing.T) {
	cmd := rootCmd()
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("help: %v", err)
	}
	if !strings.Contains(out.String(), "/mcp") {
		t.Fatalf("root help = %q, want the MCP runtime command", out.String())
	}
	for _, sub := range cmd.Commands() {
		if sub.Name() == "mcp" {
			t.Fatal("top-level mcp subcommand was added")
		}
	}
}

// TestDocsCreatesItsDirectory proves a fresh path works: the generators write
// into the directory rather than creating it, so docs must create it first.
func TestDocsCreatesItsDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "fresh", "nested")

	cmd := rootCmd()
	cmd.SetArgs([]string{"docs", "--dir", dir})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("docs: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "ohgo.1")); err != nil {
		t.Errorf("man page missing in created dir: %v", err)
	}
}

// TestDocsRequiresADir keeps a missing flag from writing into the working
// directory: generated files belong where the caller says, nowhere else.
func TestDocsRequiresADir(t *testing.T) {
	cmd := rootCmd()
	cmd.SetArgs([]string{"docs"})
	if err := cmd.Execute(); err == nil {
		t.Error("docs without --dir succeeded")
	}
}
