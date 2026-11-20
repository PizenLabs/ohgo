package ast

import (
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// jsSourceExtensions is the resolution order for a TypeScript/JavaScript
// specifier. It is shared by relative and path-alias resolution so the two
// cannot disagree about which file a specifier names.
var jsSourceExtensions = []string{".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".mts", ".cts"}

// jsPathAlias is one compilerOptions.paths entry.
//
// TypeScript allows at most one wildcard per pattern. The parser rejects
// additional wildcards rather than guessing which one captures the specifier.
type jsPathAlias struct {
	pattern string
	targets []string
}

// jsPathConfig is the repository-relative TypeScript/JavaScript resolution
// configuration.
//
// It is loaded from the nearest tsconfig.json/jsconfig.json at the index root.
// A project that keeps aliases in a nested package config is outside this
// model; the empty config deliberately resolves nothing rather than applying
// another package's aliases to this one.
type jsPathConfig struct {
	dir     string
	baseURL string
	aliases []jsPathAlias
}

// loadJSPaths reads the root TypeScript configuration. It never fails the
// index: a missing or malformed config means no aliases are known, which is
// reported downstream as unresolved imports rather than a fabricated edge.
func loadJSPaths(root string) jsPathConfig {
	for _, name := range []string{"tsconfig.json", "jsconfig.json"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			continue
		}
		if len(data) > 1<<20 {
			return jsPathConfig{dir: "."}
		}
		var parsed struct {
			CompilerOptions struct {
				BaseURL string              `json:"baseUrl"`
				Paths   map[string][]string `json:"paths"`
			} `json:"compilerOptions"`
		}
		if err := json.Unmarshal(stripJSONC(data), &parsed); err != nil {
			return jsPathConfig{dir: "."}
		}
		config := jsPathConfig{dir: "."}
		base := strings.TrimSpace(parsed.CompilerOptions.BaseURL)
		if base == "" {
			base = "."
		}
		config.baseURL = cleanRepoPath(base)
		patterns := make([]string, 0, len(parsed.CompilerOptions.Paths))
		for pattern := range parsed.CompilerOptions.Paths {
			patterns = append(patterns, pattern)
		}
		sort.Strings(patterns)
		for _, pattern := range patterns {
			if strings.Count(pattern, "*") > 1 {
				continue
			}
			targets := append([]string(nil), parsed.CompilerOptions.Paths[pattern]...)
			if len(targets) == 0 {
				continue
			}
			config.aliases = append(config.aliases, jsPathAlias{pattern: pattern, targets: targets})
		}
		return config
	}
	return jsPathConfig{}
}

// localSpecifier reports whether an unresolved import names a repository
// target rather than an external package. It is the alias-aware half of
// locallyShapedImport.
func (c jsPathConfig) localSpecifier(spec string) bool {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return false
	}
	if strings.HasPrefix(spec, ".") || strings.HasPrefix(spec, "/") {
		return true
	}
	for _, alias := range c.aliases {
		if _, _, ok := matchJSPattern(alias.pattern, spec); ok {
			return true
		}
	}
	return false
}

// resolve maps a non-relative specifier through compilerOptions.paths or
// baseUrl and then resolves it against the indexed file set.
func (c jsPathConfig) resolve(spec string, files map[string]fileRecord) (string, bool) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return "", false
	}
	for _, alias := range c.aliases {
		capture, _, ok := matchJSPattern(alias.pattern, spec)
		if !ok {
			continue
		}
		for _, target := range alias.targets {
			candidate := strings.ReplaceAll(target, "*", capture)
			candidate = cleanRepoPath(path.Join(c.baseURL, candidate))
			if resolved, ok := resolveJSCandidate(candidate, files); ok {
				return resolved, true
			}
		}
	}
	if c.baseURL != "" {
		candidate := cleanRepoPath(path.Join(c.baseURL, spec))
		if resolved, ok := resolveJSCandidate(candidate, files); ok {
			return resolved, true
		}
	}
	return "", false
}

// matchJSPattern applies one paths pattern. It returns the wildcard capture
// and whether the pattern matched.
func matchJSPattern(pattern, spec string) (string, string, bool) {
	star := strings.IndexByte(pattern, '*')
	if star < 0 {
		return "", "", pattern == spec
	}
	prefix, suffix := pattern[:star], pattern[star+1:]
	if !strings.HasPrefix(spec, prefix) || !strings.HasSuffix(spec, suffix) {
		return "", "", false
	}
	if len(spec) < len(prefix)+len(suffix) {
		return "", "", false
	}
	return spec[len(prefix) : len(spec)-len(suffix)], pattern, true
}

// generatedBuildDirs are directories whose contents are build output. An
// import into one is a known exclusion, not a broken local reference; counting
// it as unresolved would make every Next.js project look structurally broken.
var generatedBuildDirs = map[string]bool{
	".next": true, ".nuxt": true, ".svelte-kit": true, ".turbo": true,
	".vercel": true, "dist": true, "build": true, "out": true,
	"coverage": true, "target": true, "bin": true, "obj": true,
}

// generatedBuildPath reports whether a repository-relative path names build
// output rather than authored source.
func generatedBuildPath(rel string) bool {
	rel = cleanRepoPath(rel)
	if rel == "" || rel == "." {
		return false
	}
	first := rel
	if i := strings.IndexByte(first, '/'); i >= 0 {
		first = first[:i]
	}
	return generatedBuildDirs[first]
}

// resolveJSCandidate tries a base path, its source extensions, and an index
// file. A candidate that escapes the repository root is rejected.
func resolveJSCandidate(base string, files map[string]fileRecord) (string, bool) {
	base = cleanRepoPath(base)
	if base == "" || base == ".." || strings.HasPrefix(base, "../") {
		return "", false
	}
	candidates := []string{base}
	for _, ext := range jsSourceExtensions {
		candidates = append(candidates, base+ext, path.Join(base, "index"+ext))
	}
	for _, candidate := range candidates {
		candidate = strings.TrimPrefix(cleanRepoPath(candidate), "./")
		if _, ok := files[candidate]; ok {
			return candidate, true
		}
	}
	return "", false
}

func cleanRepoPath(value string) string {
	value = strings.ReplaceAll(strings.TrimSpace(value), "\\", "/")
	value = strings.TrimPrefix(value, "./")
	cleaned := path.Clean(value)
	if cleaned == "." {
		return "."
	}
	return strings.TrimPrefix(cleaned, "./")
}

// stripJSONC removes comments and trailing commas so encoding/json can read a
// tsconfig. It preserves bytes inside strings, including escaped quotes.
func stripJSONC(data []byte) []byte {
	out := make([]byte, 0, len(data))
	inString := false
	escaped := false
	for i := 0; i < len(data); i++ {
		c := data[i]
		if inString {
			out = append(out, c)
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
			out = append(out, c)
			continue
		}
		if c == '/' && i+1 < len(data) {
			if data[i+1] == '/' {
				for i < len(data) && data[i] != '\n' {
					i++
				}
				if i < len(data) {
					out = append(out, '\n')
				}
				continue
			}
			if data[i+1] == '*' {
				i += 2
				for i+1 < len(data) && (data[i] != '*' || data[i+1] != '/') {
					i++
				}
				i++
				continue
			}
		}
		out = append(out, c)
	}
	// A trailing comma is legal JSONC, not legal JSON.
	stripped := make([]byte, 0, len(out))
	for i := 0; i < len(out); i++ {
		if out[i] == ',' {
			j := i + 1
			for j < len(out) && (out[j] == ' ' || out[j] == '\t' || out[j] == '\n' || out[j] == '\r') {
				j++
			}
			if j < len(out) && (out[j] == '}' || out[j] == ']') {
				continue
			}
		}
		stripped = append(stripped, out[i])
	}
	return stripped
}
