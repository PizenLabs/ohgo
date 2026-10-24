package startup

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/PizenLabs/ohgo/project"
)

// The root ignore file OHGO may extend during explicit project onboarding.
// It is never touched by discovery or by entering an existing project.
const (
	// GitignoreName is the repository-root ignore file.
	GitignoreName = ".gitignore"
	// GitignoreHeader labels the rule OHGO contributes.
	GitignoreHeader = "# ohgo runtime state"
	// GitignoreRule ignores project-local runtime state.
	GitignoreRule = ".ohgo/"
)

// GitignoreState is the observed ignore situation, gathered read-only.
type GitignoreState struct {
	// Exists reports whether the root .gitignore file exists.
	Exists bool
	// IgnoresOhgo reports whether an existing rule ignores .ohgo/.
	IgnoresOhgo bool
}

// InspectGitignore reads <root>/.gitignore without creating or changing it.
//
// An unreadable file is reported as existing but not satisfied. The
// initialization flow then offers the rule and surfaces the write failure
// truthfully instead of quietly treating the directory as covered.
func InspectGitignore(root string) GitignoreState {
	path := filepath.Join(project.Root(root), GitignoreName)
	data, err := os.ReadFile(path)
	if err != nil {
		return GitignoreState{Exists: !errors.Is(err, os.ErrNotExist)}
	}
	return GitignoreState{Exists: true, IgnoresOhgo: IgnoresOhgo(data)}
}

// NeedsIgnore reports whether onboarding must propose the .ohgo/ ignore rule.
//
// It is the read-side gate: a directory whose .gitignore already covers the
// runtime state is never asked again, and nothing is written by this call.
func NeedsIgnore(d Discovery) bool {
	return !d.OhgoIgnored
}

// IgnoresOhgo reports whether gitignore content already ignores the project
// runtime directory.
//
// Recognition is deliberately lexical and covers the common ways one can spell
// the same rule (`.ohgo`, `.ohgo/`, `/.ohgo/`, `**/.ohgo/`, `.ohgo/**`).
// Later matching lines win, so a negation (`!.ohgo/`) re-includes the
// directory and makes the rule unsatisfied. Patterns that also affect other
// paths (`.ohgo*/`) are not treated as equivalent.
func IgnoresOhgo(content []byte) bool {
	ignored := false
	for _, raw := range strings.Split(string(content), "\n") {
		line := strings.TrimSpace(raw)
		line = strings.TrimPrefix(line, "\ufeff")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		negated := strings.HasPrefix(line, "!")
		if negated {
			line = strings.TrimPrefix(line, "!")
		}
		if !patternCoversOhgo(line) {
			continue
		}
		ignored = !negated
	}
	return ignored
}

// patternCoversOhgo reports whether one ignore pattern names .ohgo at the
// repository root.
func patternCoversOhgo(pattern string) bool {
	p := strings.TrimSpace(pattern)
	p = strings.TrimSuffix(p, "/")
	p = strings.TrimSuffix(p, "/**")
	p = strings.TrimPrefix(p, "/")
	p = strings.TrimPrefix(p, "./")
	p = strings.TrimPrefix(p, "**/")
	return p == project.DirName
}

// EnsureGitignore adds the runtime-state rule when it is absent.
//
// Existing content is preserved byte-for-byte: the rule is appended in place,
// never written through a generated template, and an already-ignored directory
// produces no write at all. changed reports whether the file was modified.
func EnsureGitignore(root string) (changed bool, err error) {
	path := filepath.Join(project.Root(root), GitignoreName)
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		if !errors.Is(readErr, os.ErrNotExist) {
			return false, fmt.Errorf("startup: read %s: %w", path, readErr)
		}
		block := GitignoreHeader + "\n" + GitignoreRule + "\n"
		if werr := os.WriteFile(path, []byte(block), 0o644); werr != nil {
			return false, fmt.Errorf("startup: create %s: %w", path, werr)
		}
		return true, nil
	}
	if IgnoresOhgo(data) {
		return false, nil
	}

	prefix := ""
	switch {
	case len(data) == 0:
	case !bytes.HasSuffix(data, []byte("\n")):
		prefix = "\n\n"
	case !bytes.HasSuffix(data, []byte("\n\n")):
		prefix = "\n"
	}
	block := prefix + GitignoreHeader + "\n" + GitignoreRule + "\n"

	file, openErr := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if openErr != nil {
		return false, fmt.Errorf("startup: open %s: %w", path, openErr)
	}
	if _, werr := file.WriteString(block); werr != nil {
		_ = file.Close()
		return false, fmt.Errorf("startup: write %s: %w", path, werr)
	}
	if cerr := file.Close(); cerr != nil {
		return false, fmt.Errorf("startup: close %s: %w", path, cerr)
	}
	return true, nil
}
