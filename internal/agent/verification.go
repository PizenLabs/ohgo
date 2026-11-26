package agent

import (
	"strings"

	"github.com/PizenLabs/ohgo/internal/control/contract"
)

// VerificationScope is one package directory a successful check covered.
//
// It is produced by the tool that ran the command, from the command's own
// arguments, so the completion evaluator can tell "the tests for the package I
// changed passed" from "some other package's tests passed".
type VerificationScope struct {
	// Path is the repository-relative directory, "." for the module root.
	Path string
	// Recursive reports that the command covered subdirectories too.
	Recursive bool
	// All reports that the command covered every package in the repository.
	All bool
}

// Covers reports whether the scope includes the required directory.
func (s VerificationScope) Covers(required string) bool {
	if required == "" {
		return false
	}
	if s.All {
		return true
	}
	if required == "*" {
		return false
	}
	if s.Path == required {
		return true
	}
	if required == "." {
		return s.Path == "."
	}
	if s.Recursive && s.Path != "" && s.Path != "." {
		return strings.HasPrefix(required, s.Path+"/")
	}
	return false
}

// RuntimeEvidence records that an execution capability started the project's
// runtime, observed it reach its declared ready state, ran at least one check
// against the live process, and saw it stay alive for the observation.
//
// It exists because an exit code is not a running application. A command that
// finished successfully ("node -v", "npm -v") is execution evidence for a
// command objective, never for "run this project". Completion for a runtime
// objective requires this observation instead.
type RuntimeEvidence struct {
	// Command is the exact service command that was launched.
	Command string
	// URL is the discovered or checked address, when one was observed.
	URL string
	// PID is the service process the runtime owned and cleaned up.
	PID int
	// Checks is how many checks passed while the service was alive.
	Checks int
}

// VerificationClass is what a successful check actually observed.
//
// It is deliberately coarser than the check kind: a build, vet run, and a
// shell assertion all observe a static property, while a test executes code
// and a live service or browser check observes runtime behavior. The class is
// a property of the evidence, never a confidence level, and it lets the answer
// audit refuse a behavioral claim that only a static check supports.
type VerificationClass string

// Verification classes, ordered from weakest to strongest observation.
const (
	// VerificationStatic is a build, static analysis, or source assertion. It
	// proves a property of the files, not that the code behaved.
	VerificationStatic VerificationClass = "STATIC"
	// VerificationTest is a test run. It proves the exercised code path
	// completed, which is stronger than a static property but still not a
	// live application observation.
	VerificationTest VerificationClass = "TEST"
	// VerificationBehavioral is a live service or browser observation. It is
	// the only class that can support a claim about runtime behavior.
	VerificationBehavioral VerificationClass = "BEHAVIORAL"
)

// EffectiveClass returns Class, or VerificationStatic when the field is unset.
//
// The zero value predates the distinction. Treating it as static is the
// conservative reading: legacy evidence can support a build or source claim,
// never a behavioral one.
func (c VerificationClass) EffectiveClass() VerificationClass {
	switch c {
	case VerificationBehavioral, VerificationTest, VerificationStatic:
		return c
	default:
		return VerificationStatic
	}
}

// Rank orders classes by observational strength for selecting the strongest
// evidence a verification plan produced.
func (c VerificationClass) Rank() int {
	switch c.EffectiveClass() {
	case VerificationBehavioral:
		return 3
	case VerificationTest:
		return 2
	default:
		return 1
	}
}

// Behavioral reports whether the class observed runtime behavior.
func (c VerificationClass) Behavioral() bool {
	return c.EffectiveClass() == VerificationBehavioral
}

// VerificationEvidence records what a successful check actually checked.
//
// Scopes may be empty for a recognised check whose package arguments the
// runtime cannot interpret. Empty scope is not proof of coverage: the
// completion evaluator treats it as an unscoped check, which can satisfy a
// generic requirement but not a specific target scope.
type VerificationEvidence struct {
	// Command is the exact command that ran.
	Command string
	// Check is the kind of check the command performed. The zero value means
	// CheckAny.
	Check contract.VerificationCheck
	// Class is what the check observed. The zero value means
	// VerificationStatic.
	Class VerificationClass
	// Scopes are the package directories the command named.
	Scopes []VerificationScope
}

// EffectiveClass returns Class, or VerificationStatic when the field is unset.
func (v *VerificationEvidence) EffectiveClass() VerificationClass {
	if v == nil {
		return VerificationStatic
	}
	return v.Class.EffectiveClass()
}

// EffectiveCheck returns Check, or CheckAny when the field is unset.
func (v *VerificationEvidence) EffectiveCheck() contract.VerificationCheck {
	if v == nil || v.Check == "" {
		return contract.CheckAny
	}
	return v.Check
}

// checkCovers reports whether a provided check kind satisfies a requirement.
//
// A passing test is also evidence that the package builds, so it covers a build
// requirement. A build is not evidence that tests pass. Vet and test are
// different checks and do not substitute for each other.
func checkCovers(provided, required contract.VerificationCheck) bool {
	if provided == "" {
		provided = contract.CheckAny
	}
	if required == "" {
		required = contract.CheckAny
	}
	switch required {
	case contract.CheckAny:
		return true
	case contract.CheckTest:
		return provided == contract.CheckTest
	case contract.CheckBuild:
		return provided == contract.CheckBuild || provided == contract.CheckTest
	case contract.CheckVet:
		return provided == contract.CheckVet
	default:
		return false
	}
}

// Covers reports whether this check covered the required directory.
func (v *VerificationEvidence) Covers(required string) bool {
	if v == nil {
		return false
	}
	for _, scope := range v.Scopes {
		if scope.Covers(required) {
			return true
		}
	}
	return false
}
