// Package tools implements the built-in tool set.
//
// Every tool is a plain struct implementing agent.Tool. Tools never spawn
// shells to accomplish file work; the standard library covers it, and a shell
// would reintroduce the quoting and escaping hazards the API already avoids.
package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/contract"
	"github.com/PizenLabs/ohgo/jsonschema"
)

// Base carries the fields every tool needs.
//
// Tools embed it and override the methods that differ. This is preferred over
// an interface-plus-adapter pair because it keeps the concrete tool type
// assignable to agent.Tool with no wrapper.
type Base struct {
	// name is the wire name.
	name string
	// description is shown to the model.
	description string
	// parameters is the argument schema.
	parameters *jsonschema.Schema
	// approval declares the capability tier.
	approval agent.Approval
	// exclusive forces the tool to run alone.
	exclusive bool
	// classes is the declared action surface. Empty means derive it from the
	// approval tier.
	classes []agent.ActionClass
	// language is the source language the tool needs, or "".
	language string
	// mutations is the declared repository change surface, or nil when the
	// tool does not declare one.
	mutations []contract.ChangeOp
}

// NewBase builds a Base. A nil schema yields a schema that accepts any value,
// which is the correct default for a tool taking no parameters.
func NewBase(name, description string, schema map[string]any, approval agent.Approval) Base {
	compiled, err := jsonschema.CompileMap(schema)
	if err != nil {
		// A malformed schema is a programming error at the call site, so it
		// fails loudly at construction rather than silently per call.
		panic(fmt.Sprintf("tools: compile schema for %q: %v", name, err))
	}
	return Base{name: name, description: description, parameters: compiled, approval: approval}
}

// Name implements agent.Tool.
func (b Base) Name() string { return b.name }

// Description implements agent.Tool.
func (b Base) Description() string { return b.description }

// Parameters implements agent.Tool.
func (b Base) Parameters() *jsonschema.Schema { return b.parameters }

// Approval implements agent.Tool.
func (b Base) Approval() agent.Approval { return b.approval }

// Exclusive implements agent.Tool. Tools opt in with SetExclusive.
func (b Base) Exclusive(map[string]any) bool { return b.exclusive }

// SetExclusive marks the tool as requiring sole use of the batch.
func (b *Base) SetExclusive(value bool) { b.exclusive = value }

// ActionClasses implements agent.ActionClassifier.
func (b Base) ActionClasses() []agent.ActionClass {
	return append([]agent.ActionClass(nil), b.classes...)
}

// SetActionClasses declares the action surface a tool exposes.
func (b *Base) SetActionClasses(classes ...agent.ActionClass) {
	b.classes = append([]agent.ActionClass(nil), classes...)
}

// RequiredLanguage implements agent.LanguageReporter.
func (b Base) RequiredLanguage() string { return b.language }

// SetRequiredLanguage declares the source language a tool needs.
func (b *Base) SetRequiredLanguage(language string) { b.language = language }

// MutationOperations implements agent.MutationReporter.
func (b Base) MutationOperations() []contract.ChangeOp {
	return append([]contract.ChangeOp(nil), b.mutations...)
}

// SetMutationOperations declares the repository change operations a tool
// performs.
func (b *Base) SetMutationOperations(operations ...contract.ChangeOp) {
	b.mutations = append([]contract.ChangeOp(nil), operations...)
}

// pathArg extracts the common "path" argument.
func pathArg(args map[string]any) (string, error) {
	raw, ok := args["path"]
	if !ok {
		return "", fmt.Errorf("missing required argument %q", "path")
	}
	path, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("argument %q must be a string", "path")
	}
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("argument %q must not be empty", "path")
	}
	return path, nil
}

// strArg extracts a string argument with a default.
func strArg(args map[string]any, key, fallback string) (string, error) {
	raw, ok := args[key]
	if !ok {
		return fallback, nil
	}
	value, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("argument %q must be a string", key)
	}
	return value, nil
}

// intArg extracts an integer argument with a default.
//
// JSON numbers decode as float64, so the value is range-checked and truncated
// only after confirming it is integral.
func intArg(args map[string]any, key string, fallback int) (int, error) {
	raw, ok := args[key]
	if !ok {
		return fallback, nil
	}
	value, ok := raw.(float64)
	if !ok {
		return 0, fmt.Errorf("argument %q must be a number", key)
	}
	if value != float64(int(value)) {
		return 0, fmt.Errorf("argument %q must be an integer", key)
	}
	return int(value), nil
}

// boolArg extracts a boolean argument with a default.
func boolArg(args map[string]any, key string, fallback bool) (bool, error) {
	raw, ok := args[key]
	if !ok {
		return fallback, nil
	}
	value, ok := raw.(bool)
	if !ok {
		return false, fmt.Errorf("argument %q must be a boolean", key)
	}
	return value, nil
}

// resolvePath turns a model-supplied path into an absolute path inside root.
//
// A relative path is interpreted against root. Symlinks are resolved because a
// symlink inside the workspace must not be usable to reach a file the model did
// not name. A path that does not exist yet is legitimate for writes, so the
// deepest existing ancestor is resolved and the missing segments are rejoined.
//
// The containment check lives here rather than at every tool call site: a
// caller that forgets it must fail loudly, not silently read outside Root.
func resolvePath(root, path string) (string, error) {
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve working directory %q: %w", root, err)
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	cleaned := filepath.Clean(path)

	// pending holds the trailing segments that do not exist yet, nearest-last.
	var pending string
	current := cleaned
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			final := resolved
			if pending != "" {
				final = filepath.Join(resolved, pending)
			}
			// Checking containment after resolution is what makes a symlink
			// escape impossible: the resolved prefix is authoritative.
			if !withinRoot(resolvedRoot, final) {
				return "", fmt.Errorf("path %q escapes the working directory", path)
			}
			// Keep the workspace's original spelling for callers. On macOS,
			// /var is commonly a symlink to /private/var; returning the
			// canonical spelling would make an in-root result look outside
			// the root to callers that compare lexical paths.
			rel, err := filepath.Rel(resolvedRoot, final)
			if err != nil {
				return "", fmt.Errorf("relativize resolved path %q: %w", final, err)
			}
			return filepath.Join(root, rel), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}

		parent := filepath.Dir(current)
		if parent == current {
			// Reached the filesystem root without resolving anything.
			return "", fmt.Errorf("resolve path: no existing ancestor of %q", cleaned)
		}
		if pending == "" {
			pending = filepath.Base(current)
		} else {
			pending = filepath.Join(filepath.Base(current), pending)
		}
		current = parent
	}
}

// withinRoot reports whether an absolute path is inside root.
func withinRoot(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// checkCancelled reports a uniform error when the context is done, so every
// tool aborts the same way instead of returning a partial result.
func checkCancelled(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("cancelled: %w", err)
	}
	return nil
}
