package tools

import (
	"path"
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
// convenient to have run.
func VerificationFor(command string) *agent.VerificationEvidence {
	segments := shellSegments(command)
	for i := len(segments) - 1; i >= 0; i-- {
		words := shellWords(segments[i])
		program, args := programAndArgs(words)
		if program == "" {
			continue
		}
		base := path.Base(program)
		if !verificationProgram(base, args) {
			return nil
		}
		evidence := &agent.VerificationEvidence{
			Command: command,
			Check:   verificationCheck(base, args),
		}
		if base == "go" {
			evidence.Scopes = goVerificationScopes(args)
		}
		return evidence
	}
	return nil
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
// without duplicating the go command-line grammar; an unrecognised scope falls
// back to the module root, which is conservative for a target in a subpackage.
func goVerificationScopes(args []string) []agent.VerificationScope {
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
		if arg == "..." || arg == "./..." || strings.HasSuffix(arg, "/...") {
			if arg == "..." || arg == "./..." {
				return []agent.VerificationScope{{All: true}}
			}
			scope := strings.TrimPrefix(strings.TrimSuffix(arg, "/..."), "./")
			if scope != "" && !strings.Contains(scope, "..") {
				scopes = append(scopes, agent.VerificationScope{Path: scope, Recursive: true})
			}
			continue
		}
		if !strings.HasPrefix(arg, ".") && !strings.Contains(arg, "/") {
			continue
		}
		scope := strings.TrimPrefix(arg, "./")
		scope = strings.Trim(scope, "/")
		if scope == "" || scope == "." {
			scopes = append(scopes, agent.VerificationScope{Path: "."})
			continue
		}
		if strings.Contains(scope, "..") {
			continue
		}
		scopes = append(scopes, agent.VerificationScope{Path: scope})
	}
	if len(scopes) == 0 {
		// "go test" with no package argument checks the current package.
		scopes = append(scopes, agent.VerificationScope{Path: "."})
	}
	return scopes
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
