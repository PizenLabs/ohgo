package agent

import (
	"strings"

	"github.com/PizenLabs/ohgo/contract"
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
	// Scopes are the package directories the command named.
	Scopes []VerificationScope
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
