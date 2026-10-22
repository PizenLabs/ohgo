package tools

import (
	"path"
	"path/filepath"
	"strings"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/contract"
)

// IsVerificationCommand reports whether a successful shell command is a
// workspace check the runtime can treat as completion evidence.
//
// This is not an authority boundary. It decides only whether a result counts
// as evidence that a change still works, so a false negative is safe: the task
// is marked NEEDS_REVIEW instead of COMPLETED. The last executable segment must
// be the check, which keeps shell operators from masking a failure
// ("go test ./... || true" is not verification).
func IsVerificationCommand(command string) bool {
	return VerificationFor(command) != nil
}

// VerificationFor returns the structured verification evidence for a command,
// or nil when the command is not a recognised check.
//
// The scope comes from the command's own package arguments, not from the
// user's request: evidence must describe what ran, never what would have been
// convenient to have run. Without a workspace root, a command that changes
// directory is still recognised as a check but carries no scopes; callers that
// need completion evidence must use VerificationForIn.
func VerificationFor(command string) *agent.VerificationEvidence {
	return verificationFor("", command)
}

// VerificationForIn returns the structured verification evidence for a command
// that starts in root.
//
// A shell command can change directory before running its check. Relative
// package arguments are then relative to that directory, not to the workspace,
// and a check in another module says nothing about this one. Scopes are
// rebased onto the workspace; a check outside it, or one whose directory
// cannot be resolved, is not verification for this workspace.
func VerificationForIn(root, command string) *agent.VerificationEvidence {
	return verificationFor(root, command)
}

// verificationFor walks the command in execution order so a directory change
// is applied to the check that follows it. The last executable segment must
// still be the check; a later command would mask whether it is the evidence.
func verificationFor(root, command string) *agent.VerificationEvidence {
	rootDir, rootKnown := verificationRoot(root)
	absDir := rootDir
	dir := "."
	outside := false
	var evidence *agent.VerificationEvidence
	lastIsCheck := false

	for _, segment := range shellSegments(command) {
		words := shellWords(segment)
		program, args := programAndArgs(words)
		if program == "" {
			continue
		}
		base := path.Base(program)

		switch base {
		case "cd", "pushd":
			lastIsCheck = false
			target, ok := cdDirectory(args)
			if !ok || !rootKnown {
				outside = true
				absDir = ""
				dir = ""
				continue
			}
			if filepath.IsAbs(target) {
				absDir = filepath.Clean(target)
			} else if absDir != "" {
				absDir = filepath.Clean(filepath.Join(absDir, target))
			} else {
				outside = true
				dir = ""
				continue
			}
			rel, inside := verificationRelative(rootDir, absDir)
			if !inside {
				outside = true
				dir = ""
				continue
			}
			dir = rel
			outside = false
			continue
		case "popd":
			// popd restores an earlier directory from a stack the verifier
			// does not model. Treating the result as unknown is the safe
			// direction: it becomes review, never a guessed coverage.
			lastIsCheck = false
			outside = true
			absDir = ""
			dir = ""
			continue
		}

		if !verificationProgram(base, args) {
			lastIsCheck = false
			continue
		}
		if outside {
			if rootKnown {
				lastIsCheck = false
				continue
			}
			// Without a root the command is still a recognised check, but its
			// directory cannot be located. Omit the scopes rather than claim a
			// coverage the caller cannot prove.
			evidence = &agent.VerificationEvidence{
				Command: command,
				Check:   verificationCheck(base, args),
			}
			lastIsCheck = true
			continue
		}

		candidate := &agent.VerificationEvidence{
			Command: command,
			Check:   verificationCheck(base, args),
		}
		if base == "go" {
			scopes, ok := goVerificationScopes(args)
			if !ok {
				lastIsCheck = false
				continue
			}
			scopes, ok = rebaseVerificationScopes(dir, scopes)
			if !ok {
				lastIsCheck = false
				continue
			}
			candidate.Scopes = scopes
		}
		evidence = candidate
		lastIsCheck = true
	}
	if !lastIsCheck {
		return nil
	}
	return evidence
}

// verificationRoot resolves the workspace root the command starts in.
func verificationRoot(root string) (string, bool) {
	if strings.TrimSpace(root) == "" {
		return "", false
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", false
	}
	return filepath.Clean(abs), true
}

// cdDirectory reads the single directory operand of cd or pushd. Flags and
// shell expansions are rejected: a path the verifier cannot resolve is not a
// scope it can claim.
func cdDirectory(args []string) (string, bool) {
	target := ""
	for _, arg := range args {
		if arg == "--" {
			continue
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		if target != "" {
			return "", false
		}
		target = arg
	}
	if target == "" || target == "-" || strings.HasPrefix(target, "~") ||
		strings.ContainsAny(target, "$`") {
		return "", false
	}
	return target, true
}

// verificationRelative maps an absolute command directory to a
// workspace-relative directory. An absolute path outside the workspace has no
// relative scope that can stand for it.
func verificationRelative(rootDir, absDir string) (string, bool) {
	if rootDir == "" || absDir == "" {
		return "", false
	}
	rel, err := filepath.Rel(rootDir, absDir)
	if err != nil {
		return "", false
	}
	rel = filepath.ToSlash(rel)
	if rel == "." {
		return ".", true
	}
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return "", false
	}
	return rel, true
}

// rebaseVerificationScopes maps a command's package arguments onto the
// workspace after its directory changes. A scope that escapes the workspace is
// not evidence about it.
func rebaseVerificationScopes(dir string, scopes []agent.VerificationScope) ([]agent.VerificationScope, bool) {
	if dir == "" {
		return nil, false
	}
	out := make([]agent.VerificationScope, 0, len(scopes))
	for _, scope := range scopes {
		switch {
		case scope.All:
			if dir == "." {
				out = append(out, agent.VerificationScope{All: true})
			} else {
				out = append(out, agent.VerificationScope{Path: dir, Recursive: true})
			}
		case scope.Path == "" || scope.Path == ".":
			out = append(out, agent.VerificationScope{Path: dir, Recursive: scope.Recursive})
		default:
			joined := path.Clean(path.Join(dir, scope.Path))
			if joined == ".." || strings.HasPrefix(joined, "../") {
				return nil, false
			}
			out = append(out, agent.VerificationScope{Path: joined, Recursive: scope.Recursive})
		}
	}
	return out, true
}

// verificationCheck reports what kind of check a recognised command performs.
func verificationCheck(program string, args []string) contract.VerificationCheck {
	sub := firstNonFlag(args)
	switch program {
	case "go":
		switch sub {
		case "test":
			return contract.CheckTest
		case "build":
			return contract.CheckBuild
		case "vet":
			return contract.CheckVet
		}
	case "cargo":
		switch sub {
		case "test":
			return contract.CheckTest
		case "build", "check":
			return contract.CheckBuild
		}
	case "npm", "pnpm", "yarn", "bun":
		if sub == "test" {
			return contract.CheckTest
		}
		if sub == "build" {
			return contract.CheckBuild
		}
		if sub == "run" {
			next := firstNonFlagAfter(args, "run")
			if next == "test" {
				return contract.CheckTest
			}
			if next == "build" {
				return contract.CheckBuild
			}
		}
	case "pytest", "jest", "vitest", "rspec", "phpunit":
		return contract.CheckTest
	case "python", "python3":
		if pythonCheck(args) {
			return contract.CheckTest
		}
	case "make":
		switch sub {
		case "test":
			return contract.CheckTest
		case "build":
			return contract.CheckBuild
		case "check":
			return contract.CheckVet
		}
	case "mvn", "gradle", "gradlew":
		switch sub {
		case "test", "verify":
			return contract.CheckTest
		case "build":
			return contract.CheckBuild
		}
	case "dotnet", "swift", "deno":
		if sub == "test" {
			return contract.CheckTest
		}
		if sub == "build" || sub == "check" {
			return contract.CheckBuild
		}
	case "mix":
		if sub == "test" {
			return contract.CheckTest
		}
	case "tsc":
		return contract.CheckBuild
	case "staticcheck", "golangci-lint":
		return contract.CheckVet
	}
	return contract.CheckAny
}

// goVerificationScopes reads the package directories from a go command.
//
// Only explicit path-like arguments are accepted. Bare package names are not
// interpreted because a flag value is indistinguishable from a package name
// without duplicating the go command-line grammar; with no path argument the
// fallback is the current package. An absolute or otherwise unparseable path
// returns false instead: falling back to the current package would turn a
// check somewhere else into evidence for the workspace.
//
// A relative path may escape the current directory; the caller rebases it
// against the command's actual working directory and rejects an escape from
// the workspace. Keeping the relative form here is what lets
// "cd tools && go test ../ast" be recognised as the ast package.
func goVerificationScopes(args []string) ([]agent.VerificationScope, bool) {
	sub := firstNonFlag(args)
	start := 0
	for i, arg := range args {
		if arg == sub {
			start = i + 1
			break
		}
	}
	var scopes []agent.VerificationScope
	for _, arg := range args[start:] {
		if strings.HasPrefix(arg, "-") {
			continue
		}
		if arg == "and" || arg == "then" {
			break
		}
		if arg == "..." || arg == "./..." {
			return []agent.VerificationScope{{All: true}}, true
		}
		recursive := strings.HasSuffix(arg, "/...")
		raw := arg
		if recursive {
			raw = strings.TrimSuffix(arg, "/...")
		}
		if filepath.IsAbs(raw) {
			return nil, false
		}
		if !recursive && !strings.HasPrefix(arg, ".") && !strings.Contains(arg, "/") {
			continue
		}
		scope := path.Clean(strings.TrimPrefix(raw, "./"))
		if scope == "" || scope == "." {
			scopes = append(scopes, agent.VerificationScope{Path: "."})
			continue
		}
		// Keep a relative escape ("..", "../ast") so the caller can rebase it
		// against the directory the check actually ran in.
		scopes = append(scopes, agent.VerificationScope{Path: scope, Recursive: recursive})
	}
	if len(scopes) == 0 {
		// "go test" with no package argument checks the current package.
		scopes = append(scopes, agent.VerificationScope{Path: "."})
	}
	return scopes, true
}

// verificationProgram reports whether program, invoked with args, is a
// test/build/check command.
func verificationProgram(program string, args []string) bool {
	switch program {
	case "go":
		sub := firstNonFlag(args)
		return sub == "test" || sub == "build" || sub == "vet"
	case "cargo":
		sub := firstNonFlag(args)
		return sub == "test" || sub == "build" || sub == "check"
	case "npm", "pnpm", "yarn", "bun":
		return packageManagerCheck(args)
	case "pytest", "jest", "vitest", "rspec", "phpunit":
		return true
	case "python", "python3":
		return pythonCheck(args)
	case "make":
		sub := firstNonFlag(args)
		return sub == "test" || sub == "check" || sub == "build"
	case "mvn", "gradle", "gradlew":
		sub := firstNonFlag(args)
		return sub == "test" || sub == "verify" || sub == "build"
	case "dotnet":
		return firstNonFlag(args) == "test" || firstNonFlag(args) == "build"
	case "swift":
		return firstNonFlag(args) == "test" || firstNonFlag(args) == "build"
	case "mix":
		return firstNonFlag(args) == "test"
	case "deno":
		return firstNonFlag(args) == "test" || firstNonFlag(args) == "check"
	case "tsc":
		return hasFlag(args, "--noEmit")
	case "staticcheck":
		return true
	case "golangci-lint":
		// Listing linters exits 0 without checking the code. Only "run" is a
		// verification.
		return firstNonFlag(args) == "run"
	}
	return false
}

// packageManagerCheck recognises "test" and "run test"/"run build" forms.
func packageManagerCheck(args []string) bool {
	sub := firstNonFlag(args)
	if sub == "test" || sub == "build" {
		return true
	}
	if sub == "run" {
		return firstNonFlagAfter(args, "run") == "test" || firstNonFlagAfter(args, "run") == "build"
	}
	return false
}

// pythonCheck recognises "python -m pytest" without treating every Python
// invocation as verification.
func pythonCheck(args []string) bool {
	for i, arg := range args {
		if arg == "-m" && i+1 < len(args) {
			return args[i+1] == "pytest" || args[i+1] == "unittest"
		}
	}
	return false
}

// hasFlag reports an exact flag.
func hasFlag(args []string, flag string) bool {
	for _, arg := range args {
		if arg == flag {
			return true
		}
	}
	return false
}
