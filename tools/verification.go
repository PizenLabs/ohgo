package tools

import (
	"path"
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
	segments := shellSegments(command)
	for i := len(segments) - 1; i >= 0; i-- {
		words := shellWords(segments[i])
		program, args := programAndArgs(words)
		if program == "" {
			continue
		}
		return verificationProgram(path.Base(program), args)
	}
	return false
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
