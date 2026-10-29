// Package creds resolves provider credentials from ordered sources.
//
// Precedence, lowest number wins:
//
//  1. Explicit — handed to the request (registry map, pasted key, flags).
//  2. File store — ~/.ohgo/credentials.json, written only when the user
//     chooses to save.
//  3. Process environment.
//  4. Shell startup files — parsed as text, never executed.
//
// Discovery is read-only: nothing here writes, deletes, or modifies a shell
// file, and nothing executes one. Finding a credential never prints it;
// Scrub redacts known secrets from diagnostics.
package creds

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/PizenLabs/ohgo/internal/state"
)

// ErrMissingCredential means no source held a credential for the provider.
var ErrMissingCredential = errors.New("creds: missing credential for provider")

// ErrUnknownProvider means the provider has no recognized credential names.
var errUnknownProvider = errors.New("creds: unknown provider")

// ProviderEnvVars maps a provider key to the environment variables that may
// carry its credential, in preference order.
//
// This table is the single source of truth for credential discovery: nothing
// in this codebase may read a provider credential from the environment or a
// shell file directly.
func ProviderEnvVars(provider string) ([]string, bool) {
	vars, ok := providerEnvVars[provider]
	return vars, ok
}

// providerEnvVars is that table. OPENCODE_API_KEY serves both OpenCode Zen
// and OpenCode Go: one operator, one credential, two endpoints. The tiers
// (Go vs Go Plus) differ server-side, not in the key.
var providerEnvVars = map[string][]string{
	"anthropic":    {"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"},
	"openai":       {"OPENAI_API_KEY"},
	"azure":        {"AZURE_OPENAI_API_KEY"},
	"google":       {"GEMINI_API_KEY", "GOOGLE_API_KEY"},
	"groq":         {"GROQ_API_KEY"},
	"mistral":      {"MISTRAL_API_KEY"},
	"deepseek":     {"DEEPSEEK_API_KEY"},
	"openrouter":   {"OPENROUTER_API_KEY"},
	"together":     {"TOGETHER_API_KEY"},
	"fireworks":    {"FIREWORKS_API_KEY"},
	"xai":          {"XAI_API_KEY"},
	"cerebras":     {"CEREBRAS_API_KEY"},
	"ollama":       {},
	"ollama-cloud": {"OLLAMA_API_KEY"},
	"opencode-zen": {"OPENCODE_API_KEY"},
	"opencode-go":  {"OPENCODE_API_KEY"},
}

// ProviderNames lists every provider with recognized credential names,
// sorted. "ollama" (local, keyless) is included with an empty list so
// callers can distinguish "needs no key" from "unknown provider".
func ProviderNames() []string {
	names := make([]string, 0, len(providerEnvVars))
	for name := range providerEnvVars {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ShellFiles lists the startup files consulted for credentials, in order.
// Only these fixed paths are ever read: nothing scans the home directory,
// and symlinks resolve to wherever they point, which the file permissions
// below still gate.
func ShellFiles(home string) []string {
	return []string{
		filepath.Join(home, ".zshrc"),
		filepath.Join(home, ".zprofile"),
		filepath.Join(home, ".bashrc"),
		filepath.Join(home, ".bash_profile"),
		filepath.Join(home, ".profile"),
		filepath.Join(home, ".config", "fish", "config.fish"),
	}
}

// ParseResult is one parsed shell file.
type ParseResult struct {
	// Values holds the assignments understood.
	Values map[string]string
	// Skipped describes lines that could not be parsed safely, for the
	// caller to report. A dynamic expression is skipped, never executed.
	Skipped []string
}

// ParseShellAssignments extracts KEY=value assignments from shell startup
// text without executing anything.
//
// Supported, in order: `export NAME=value`, `NAME=value`, and fish
// `set -gx NAME value`. Values may be bare, single-quoted, or
// double-quoted; `#` starts a comment outside quotes. Anything containing a
// command substitution (`$(...)`, backticks), a `${...}` expansion, or any
// other line shape is skipped and reported. Later assignments win, matching
// shell semantics for repeated exports.
func ParseShellAssignments(text string) ParseResult {
	result := ParseResult{Values: map[string]string{}}
	for i, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, ok := parseAssignmentLine(line)
		if !ok {
			result.Skipped = append(result.Skipped, fmt.Sprintf("line %d: not a plain assignment", i+1))
			continue
		}
		result.Values[name] = value
	}
	return result
}

// parseAssignmentLine parses one line into name and value.
func parseAssignmentLine(line string) (string, string, bool) {
	rest := line
	if name, value, ok := parseFishSet(line); ok {
		if !validName(name) {
			return "", "", false
		}
		return name, value, true
	}
	if stripped, ok := strings.CutPrefix(rest, "export "); ok {
		rest = strings.TrimSpace(stripped)
	}
	name, value, ok := strings.Cut(rest, "=")
	name = strings.TrimSpace(name)
	if !ok || !validName(name) {
		return "", "", false
	}
	parsed, ok := parseShellValue(strings.TrimSpace(value))
	if !ok {
		return "", "", false
	}
	return name, parsed, true
}

// parseFishSet parses `set -gx NAME value` (flags in any order before NAME).
// Fish lists are space-joined; a credential is one token, so exactly one
// value token is accepted and anything else is skipped as unparsable.
func parseFishSet(line string) (string, string, bool) {
	fields, ok := splitBareWords(line)
	if !ok || len(fields) == 0 || fields[0] != "set" {
		return "", "", false
	}
	args := fields[1:]
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		args = args[1:]
	}
	if len(args) != 2 {
		return "", "", false
	}
	value, ok := parseShellValue(args[1])
	if !ok {
		return "", "", false
	}
	return args[0], value, true
}

// splitBareWords splits on whitespace, failing on any quote character: fish
// set lines with quotes need full shell parsing, which this parser does not
// do, so they are skipped rather than misread.
func splitBareWords(line string) ([]string, bool) {
	if strings.ContainsAny(line, `'"`+"`") {
		return nil, false
	}
	return strings.Fields(line), true
}

// validName reports shell identifier shape.
func validName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		if r == '_' || ('a' <= r && r <= 'z') || ('A' <= r && r <= 'Z') || (i > 0 && '0' <= r && r <= '9') {
			continue
		}
		return false
	}
	return true
}

// parseShellValue unquotes one value. It fails on anything dynamic — command
// substitutions, expansions, unclosed quotes — because guessing at dynamic
// text is how a parser ends up executing it by accident in a later refactor.
func parseShellValue(raw string) (string, bool) {
	if raw == "" {
		return "", true
	}
	if strings.Contains(raw, "`") || strings.Contains(raw, "$(") || strings.Contains(raw, "${") {
		return "", false
	}
	if len(raw) >= 2 && raw[0] == '\'' && raw[len(raw)-1] == '\'' {
		inner := raw[1 : len(raw)-1]
		if strings.Contains(inner, "'") {
			return "", false
		}
		// A lone $ is literal inside single quotes in shell; but a $ next to
		// text invites misreading, so only a bare $ or $-free text passes.
		if strings.Contains(inner, "$") {
			return "", false
		}
		return inner, true
	}
	if len(raw) >= 2 && raw[0] == '"' && raw[len(raw)-1] == '"' {
		var sb strings.Builder
		inner := raw[1 : len(raw)-1]
		for i := 0; i < len(inner); i++ {
			if inner[i] == '\\' && i+1 < len(inner) {
				next := inner[i+1]
				// Inside double quotes only $ ` " \ and newline stay escapable;
				// anything else keeps its backslash, matching shell.
				if next == '$' || next == '`' || next == '"' || next == '\\' {
					sb.WriteByte(next)
					i++
					continue
				}
			}
			if inner[i] == '$' {
				return "", false
			}
			sb.WriteByte(inner[i])
		}
		return sb.String(), true
	}
	if strings.HasPrefix(raw, "'") || strings.HasPrefix(raw, "\"") {
		return "", false
	}
	// Bare words end at whitespace or an outside-quotes comment.
	if i := strings.Index(raw, "#"); i >= 0 {
		// A # mid-word without preceding space is literal (e.g. pass#1);
		// with space before it, shell treats it as a comment.
		if i > 0 && (raw[i-1] == ' ' || raw[i-1] == '\t') {
			raw = strings.TrimRight(raw[:i], " \t")
		}
	}
	if strings.ContainsAny(raw, " \t") {
		return "", false
	}
	if strings.Contains(raw, "$") {
		return "", false
	}
	return raw, true
}

// Store is a file-backed credential map with owner-only permissions.
//
// Secrets live here, never in settings.json: settings are shown, diffed, and
// occasionally pasted into issues, and a secret beside them would eventually
// travel along.
type Store struct {
	mu   sync.Mutex
	path string
	data map[string]string
}

// DefaultPath locates the credential file.
func DefaultPath() (string, error) {
	return state.Path("credentials.json")
}

// Open reads the store, treating a missing file as empty. A malformed file
// is an error, not silent emptiness: silently dropping saved credentials
// would send the user back to setup with no explanation.
func Open(path string) (*Store, error) {
	store := &Store{path: path, data: map[string]string{}}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return store, nil
		}
		return nil, fmt.Errorf("creds: read %s: %w", path, err)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return store, nil
	}
	var data map[string]string
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("creds: parse %s: %w", path, err)
	}
	store.data = data
	return store, nil
}

// Get returns the stored credential for a provider, if any.
func (s *Store) Get(provider string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key, ok := s.data[provider]
	if !ok || strings.TrimSpace(key) == "" {
		return "", false
	}
	return key, true
}

// Set records a credential. It writes through immediately so a crash after
// "saved" never loses the key the user just confirmed.
func (s *Store) Set(provider, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data == nil {
		s.data = map[string]string{}
	}
	if strings.TrimSpace(key) == "" {
		delete(s.data, provider)
	} else {
		s.data[provider] = key
	}
	return s.saveLocked()
}

// Delete removes a credential. Removing what is not there succeeds: the end
// state is what matters, and an error would force callers to check first.
func (s *Store) Delete(provider string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, provider)
	return s.saveLocked()
}

// Providers lists stored providers, sorted.
func (s *Store) Providers() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.data))
	for provider := range s.data {
		out = append(out, provider)
	}
	sort.Strings(out)
	return out
}

// saveLocked writes the file with owner-only permissions, atomically: write
// a temp file, chmod 0600, rename. A half-written credential file that
// another process reads would leak or corrupt, and 0600 keeps group and
// other users out.
func (s *Store) saveLocked() error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creds: create %s: %w", dir, err)
	}
	raw, err := json.Marshal(s.data)
	if err != nil {
		return fmt.Errorf("creds: encode: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".credentials-*.tmp")
	if err != nil {
		return fmt.Errorf("creds: write temp: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("creds: write temp: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("creds: chmod: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("creds: write temp: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("creds: replace %s: %w", s.path, err)
	}
	return nil
}

// Resolver resolves credentials through the precedence chain.
type Resolver struct {
	// Explicit wins over everything: a key handed to this request.
	Explicit map[string]string
	// File is the credential store, consulted second. Nil skips it.
	File *Store
	// Getenv reads the process environment. Nil means os.Getenv; tests
	// inject a map lookup so the suite never depends on the real shell.
	Getenv func(name string) string
	// Shell holds pre-parsed startup-file assignments. Nil skips them.
	Shell map[string]string
	// ShellSource names where Shell came from, for source labels.
	ShellSource string
}

// Resolve returns the credential and a human-readable source label, e.g.
// "environment (OPENROUTER_API_KEY)" or "shell file (~/.zshrc)".
func (r *Resolver) Resolve(provider string) (key, source string, err error) {
	if value := strings.TrimSpace(r.Explicit[provider]); value != "" {
		return value, "explicit", nil
	}
	if r.File != nil {
		if value, ok := r.File.Get(provider); ok {
			return value, "credential file", nil
		}
	}
	vars, known := ProviderEnvVars(provider)
	if !known {
		return "", "", fmt.Errorf("%w: %q", errUnknownProvider, provider)
	}
	getenv := r.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	for _, name := range vars {
		if value := strings.TrimSpace(getenv(name)); value != "" {
			return value, "environment (" + name + ")", nil
		}
	}
	for _, name := range vars {
		if value, ok := r.Shell[name]; ok && strings.TrimSpace(value) != "" {
			source := "shell file"
			if r.ShellSource != "" {
				source += " (" + r.ShellSource + ")"
			}
			return strings.TrimSpace(value), source, nil
		}
	}
	if len(vars) == 0 {
		// A provider with no credential names (local Ollama) needs no key:
		// resolving one would be inventing a requirement.
		return "", "none required", nil
	}
	return "", "", fmt.Errorf("%w: %s (set %s)", ErrMissingCredential, provider, strings.Join(vars, " or "))
}

// DiscoverShellAssignments parses every readable startup file, merging later
// files over earlier ones. Unreadable files are skipped, never fatal: a
// permission-denied .bashrc must not break resolution for everyone else.
func DiscoverShellAssignments(home string) (values map[string]string, sources []string) {
	values = map[string]string{}
	for _, path := range ShellFiles(home) {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		parsed := ParseShellAssignments(string(raw))
		for name, value := range parsed.Values {
			values[name] = value
		}
		short := "~" + strings.TrimPrefix(path, home)
		sources = append(sources, short)
	}
	return values, sources
}

// Scrub replaces every known secret in text with "[redacted]".
//
// Diagnostics, doctor output, and error paths run user-visible strings
// through this before printing. Longer secrets go first so a key that
// contains a shorter one cannot partially survive.
func Scrub(text string, secrets []string) string {
	ordered := append([]string(nil), secrets...)
	sort.Slice(ordered, func(i, j int) bool { return len(ordered[i]) > len(ordered[j]) })
	for _, secret := range ordered {
		if strings.TrimSpace(secret) == "" {
			continue
		}
		text = strings.ReplaceAll(text, secret, "[redacted]")
	}
	return text
}
