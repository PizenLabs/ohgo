// Package contract defines the bounded completion contract a user request
// implies.
//
// The package is data only. It never parses, executes, authorizes, or decides
// completion; workspace builds the contract from user text, and agent consumes
// it as evidence about what would make a completion claim truthful.
package contract

// ObjectiveKind is the completion contract a request implies.
//
// It is deliberately coarser than routing intent: routing asks which authority
// the request needs, while completion asks what evidence would prove the
// request was satisfied. A read can complete with an answer; an execution can
// complete by running the requested check; a mutation needs an observed target
// change.
type ObjectiveKind string

// Completion objective kinds.
const (
	// ObjectiveInformational asks for an answer, not an action.
	ObjectiveInformational ObjectiveKind = "informational"
	// ObjectiveExecution asks for a process or check, not a repository change.
	ObjectiveExecution ObjectiveKind = "execution"
	// ObjectiveMutation asks for a repository change.
	ObjectiveMutation ObjectiveKind = "mutation"
)

// ChangeOp is the expected direction of one target change.
type ChangeOp string

// Target change operations.
const (
	// ChangeAny accepts any observed change to the target.
	ChangeAny ChangeOp = "change"
	// ChangeCreate requires the target to be absent before and present after.
	ChangeCreate ChangeOp = "create"
	// ChangeModify requires the target to exist before and differ after.
	ChangeModify ChangeOp = "modify"
	// ChangeDelete requires the target to exist before and be absent after.
	ChangeDelete ChangeOp = "delete"
)

// TargetKind names the repository coordinate a target uses.
type TargetKind string

// Target kinds.
const (
	// TargetPath is a repository-relative file path.
	TargetPath TargetKind = "path"
	// TargetSymbol is a Go symbol name, optionally package-qualified.
	TargetSymbol TargetKind = "symbol"
	// TargetPackage is a Go package name.
	TargetPackage TargetKind = "package"
)

// Target is one repository coordinate named by the request.
type Target struct {
	// Kind is how Value is interpreted.
	Kind TargetKind
	// Value is a path, symbol, or package name exactly as the user named it
	// (after trimming path punctuation).
	Value string
}

// ExpectedChange binds a target to the operation the request asks for.
//
// It is separate from Target so the scope list stays unique while a compound
// request can still carry per-target operations. OperationFor is the only
// lookup, so the two lists cannot drift at a call site.
type ExpectedChange struct {
	// Target is the coordinate the change applies to.
	Target Target
	// Operation is the direction the request asks for, or ChangeAny when the
	// request names a location without naming a direction.
	Operation ChangeOp
}

// VerificationKind says how a behavioural requirement is expressed.
type VerificationKind string

// Verification requirement kinds.
const (
	// VerifyAny requires a successful recognised check, with no target scope.
	VerifyAny VerificationKind = "any"
	// VerifyScope requires a successful check covering Scope.
	VerifyScope VerificationKind = "scope"
)

// VerificationCheck says what kind of check a requirement or command is.
//
// A passing build is not a passing test, and a passing test in another package
// is not a passing test here. The check kind is part of the evidence, not a
// decoration: completion matches it against the requirement.
type VerificationCheck string

// Verification checks.
const (
	// CheckAny accepts any recognised successful check.
	CheckAny VerificationCheck = "any"
	// CheckTest is a test run.
	CheckTest VerificationCheck = "test"
	// CheckBuild is a build or compile check.
	CheckBuild VerificationCheck = "build"
	// CheckVet is a static analysis check.
	CheckVet VerificationCheck = "vet"
)

// VerificationRequirement is one behavioural check the request asks for.
type VerificationRequirement struct {
	// Kind is any-check or scope-check.
	Kind VerificationKind
	// Scope is a repository-relative directory, "." for the module root, or
	// "*" for every package (or, for a behavioural objective with no named
	// target, every package the run changed).
	Scope string
	// Check is the kind of check that satisfies the requirement. The zero
	// value means CheckAny.
	Check VerificationCheck
}

// EffectiveCheck returns Check, or CheckAny when the field is unset.
func (r VerificationRequirement) EffectiveCheck() VerificationCheck {
	if r.Check == "" {
		return CheckAny
	}
	return r.Check
}

// IntentContract is the bounded, inspectable completion contract for one
// user request.
//
// It is extracted deterministically from the user's text. It is never built
// from model output, and it never grants authority: it only tells the runtime
// what evidence would make the request's completion claim truthful.
type IntentContract struct {
	// Objective is the user's request, trimmed. It is carried for the task
	// record and for the assessment rationale, never parsed as a command.
	Objective string
	// Kind is the completion objective.
	Kind ObjectiveKind
	// Targets are the unique repository coordinates the request names.
	Targets []Target
	// ExpectedChanges binds each target to its requested direction.
	ExpectedChanges []ExpectedChange
	// Verification lists the behavioural checks the request asks for. A
	// mutation objective always has at least one requirement, because Phase 3
	// already requires a check after a change.
	Verification []VerificationRequirement
	// BehavioralObjective reports that the request's own success condition is
	// a named check ("fix the failing tests"), so no explicit target is needed
	// to assess alignment.
	BehavioralObjective bool
	// Ambiguous reports that a mutation objective has no verifiable target and
	// no behavioural success condition. It is the only field that downgrades a
	// run before any execution happens; it never grants anything.
	Ambiguous bool
	// Grounding is the structured semantic reading of the request: action,
	// entities, scope, assumptions, and ambiguities. It is the reference the
	// runtime validates a model proposal and final answer against. It is
	// extracted from user text, never from model output, and grants nothing.
	Grounding *IntentGrounding
	// Issues are deterministic extraction notes, used in the review reason.
	Issues []string
}

// OperationFor returns the requested operation for a target.
func (c IntentContract) OperationFor(target Target) ChangeOp {
	for _, change := range c.ExpectedChanges {
		if change.Target == target {
			return change.Operation
		}
	}
	return ChangeAny
}
