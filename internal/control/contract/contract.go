// Package contract defines the bounded completion contract a user request
// implies, together with the small explicit vocabulary that names its limits.
//
// The package is data and vocabulary only. It never executes, authorizes, or
// decides completion; workspace builds the contract from user text, and agent
// consumes it as evidence about what would make a completion claim truthful.
// A constraint recognizer preserves the user's own words; it never decides
// whether an action is permitted.
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

// ExecutionMode says which observation proves an execution objective.
//
// ObjectiveExecution alone only says "something must run". That is too weak
// when the request asks to run the project itself: a successful environment
// probe ("node -v && npm -v") is an execution, but it is not the requested
// execution. The mode keeps the distinction deterministic, so completion can
// require the observation the request actually named.
type ExecutionMode string

// Execution modes.
const (
	// ExecutionCommand means the objective is a process or check that runs to a
	// successful terminal state. A named check, a build, or a one-shot command
	// proves it. It is the zero value so older checkpoints keep their meaning.
	ExecutionCommand ExecutionMode = ""
	// ExecutionRuntime means the objective is a long-running project or
	// service. A finished unrelated command is not proof: the runtime must be
	// launched and observed reaching a ready state and answering at least one
	// check while it stays alive.
	ExecutionRuntime ExecutionMode = "runtime"
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

// TargetKind names the kind of object a target addresses.
//
// A target is not always a repository coordinate. A migration names a
// technology, a request can name a command, and a report can address a
// resource. Keeping the kind explicit is what stops a semantic name such as
// "Next.js" from being mistaken for the file "Next.js".
type TargetKind string

// Target kinds.
const (
	// TargetPath is a repository-relative file or directory path.
	TargetPath TargetKind = "path"
	// TargetSymbol is a Go symbol name, optionally package-qualified.
	TargetSymbol TargetKind = "symbol"
	// TargetPackage is a Go package name.
	TargetPackage TargetKind = "package"
	// TargetTechnology is a technology, framework, or language the request
	// names as an objective ("convert this project to Next.js"). It is a
	// semantic target: it says what the result should be, never where the
	// files are.
	TargetTechnology TargetKind = "technology"
	// TargetRepository is the workspace itself ("this project", "the
	// repository"). It is a mutation scope, never a filesystem coordinate.
	TargetRepository TargetKind = "repository"
	// TargetCommand is a command the request names as an operation.
	TargetCommand TargetKind = "command"
	// TargetResource is a named non-code artifact or class ("documentation",
	// "configuration", "tests", "JSON"). It is a semantic target.
	TargetResource TargetKind = "resource"
)

// Filesystem reports whether the target addresses a concrete filesystem
// coordinate that structural evidence can check.
func (k TargetKind) Filesystem() bool {
	return k == TargetPath || k == TargetSymbol || k == TargetPackage
}

// Target is one object named by the request.
type Target struct {
	// Kind is how Value is interpreted.
	Kind TargetKind
	// Value is the user's name for the target, after trimming path
	// punctuation. For a technology it is the technology name. For a
	// repository target it is a canonical label, not a path.
	Value string
	// Directory reports that a path target names a directory rather than one
	// file. It is meaningless for other kinds.
	Directory bool `json:"directory,omitempty"`
}

// MutationScopeKind says what a mutation objective may change.
type MutationScopeKind string

// Mutation scope kinds.
const (
	// MutationScopeUnspecified means the contract predates the explicit
	// scope. The evaluator falls back to the captured target snapshot.
	MutationScopeUnspecified MutationScopeKind = ""
	// MutationScopeWorkspace means the objective may change any path inside
	// the authorized workspace. A technology migration or a behavioural
	// objective ("fix the failing tests") normally has this scope.
	MutationScopeWorkspace MutationScopeKind = "workspace"
	// MutationScopePaths means the objective is bounded to the named files
	// and directories.
	MutationScopePaths MutationScopeKind = "paths"
)

// MutationScope is the explicit persistent mutation scope of an objective.
//
// It is deliberately separate from Targets: a target describes the desired
// result (a technology, a symbol, a file), while the scope describes where a
// persistent change is allowed. Deriving a path scope from a technology name
// would turn "convert to Next.js" into "change the file Next.js".
type MutationScope struct {
	// Kind is the scope class.
	Kind MutationScopeKind `json:"kind,omitempty"`
	// Files are the exact repository-relative files the objective may change.
	Files []string `json:"files,omitempty"`
	// Directories are repository-relative directories whose contents the
	// objective may change.
	Directories []string `json:"directories,omitempty"`
}

// Workspace reports whether the objective may change the whole workspace.
func (s MutationScope) Workspace() bool {
	return s.Kind == MutationScopeWorkspace
}

// ExplicitPaths reports whether the objective named a filesystem scope. A
// semantic or behavioural objective with no named path is not an explicit
// path scope even though it has targets.
func (s MutationScope) ExplicitPaths() bool {
	return s.Kind == MutationScopePaths && (len(s.Files) > 0 || len(s.Directories) > 0)
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
	// ExecutionMode refines an ObjectiveExecution: a one-shot command or check
	// versus the project's own long-running runtime. It is meaningless for any
	// other kind and stays empty (ExecutionCommand) for legacy contracts.
	ExecutionMode ExecutionMode
	// Targets are the unique objects the request names.
	Targets []Target
	// ExpectedChanges binds each target to its requested direction.
	ExpectedChanges []ExpectedChange
	// MutationScope is where a persistent change is allowed. It is separate
	// from Targets so a semantic target such as a technology never becomes a
	// filesystem scope.
	MutationScope MutationScope `json:"mutationScope,omitempty"`
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
	// ConditionalMutation reports that the request stages an investigation or
	// understanding phase before a conditional future change ("investigate
	// first; if the issue is confirmed, fix it"). Kind remains Mutation so the
	// change remains authorized and grounded when the branch is reached, but
	// completion must not require the change until it is observed. The field is
	// additive and false for an unconditional mutation request.
	ConditionalMutation bool
	// Grounding is the structured semantic reading of the request: action,
	// entities, scope, assumptions, and ambiguities. It is the reference the
	// runtime validates a model proposal and final answer against. It is
	// extracted from user text, never from model output, and grants nothing.
	Grounding *IntentGrounding
	// Issues are deterministic extraction notes, used in the review reason.
	Issues []string
}

// InformationalPhase returns the contract evaluated as the informational or
// investigation phase of a deferred objective.
//
// The objective, targets, and grounding stay attached so task identity and the
// eventual mutation branch survive. Mutation-only completion requirements are
// removed: the current authority or conditional branch cannot satisfy them
// yet, and judging the phase against them would report a false evidence gap.
func (c IntentContract) InformationalPhase() IntentContract {
	phase := c
	phase.Kind = ObjectiveInformational
	phase.ExecutionMode = ExecutionCommand
	phase.Verification = nil
	phase.BehavioralObjective = false
	phase.Ambiguous = false
	phase.Issues = nil
	return phase
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
