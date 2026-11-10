// Package cognition owns Ohgo's task-shape, evidence, projection, and
// sufficiency model.
//
// It is runtime-side state. Nothing here is produced by a model or grants
// authority: a shape describes what a task structurally needs, evidence
// records what the runtime has observed, a projection builds a smaller view of
// that state for the next model request, and sufficiency says whether the
// current evidence can support the next step. The security boundary remains
// the only component that authorizes execution, and completion remains owned
// by the agent evidence evaluator.
package cognition

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/budget"
	"github.com/PizenLabs/ohgo/internal/control/contract"
)

// ShapeVersion is the schema version of a TaskShape.
const ShapeVersion = 1

// TaskShape is the declarative structural requirement of one task.
//
// It is derived deterministically from the user's completion contract and the
// active authority. It never encodes a workflow: it records what evidence the
// task needs, what may change, what verification would make the change
// trustworthy, and what completion would mean. Candidate action selection and
// context projection read this shape; neither is allowed to widen authority.
type TaskShape struct {
	Version int `json:"version"`
	// Objective is the user's request, trimmed.
	Objective string `json:"objective"`
	// Kind is the completion objective from the contract.
	Kind contract.ObjectiveKind `json:"kind"`
	// Intent is the routed intent (read, execute, investigate), when known.
	Intent string `json:"intent,omitempty"`
	// Phase reports that this shape describes the informational phase of an
	// objective that needs a stronger authority later. A deferred phase may
	// still need the execution tools its authority permits; it is not the same
	// as an ordinary question that only needs an answer.
	Phase bool `json:"phase,omitempty"`
	// Investigation reports that the request asks to probe, reproduce, or
	// diagnose rather than merely to read. The completion is still an answer,
	// but an investigation remains open while its execution surface is
	// available: observing one repository fact is not the same as finishing
	// the probe the user asked for.
	Investigation bool `json:"investigation,omitempty"`
	// Authority is what the active turn may do. It describes authority; it
	// never grants it.
	Authority Authority `json:"authority"`
	// Mutation is the repository change scope.
	Mutation MutationScope `json:"mutation"`
	// RepositoryScope reports that the request is about the repository itself
	// rather than a general question.
	RepositoryScope bool `json:"repositoryScope,omitempty"`
	// BroadRepositoryAnalysis reports that the objective is a repository-wide
	// account (purpose, structure, stack) rather than one targeted fact. It is
	// the boundary where documentation claims must stay distinguishable from
	// observed implementation facts.
	BroadRepositoryAnalysis bool `json:"broadRepositoryAnalysis,omitempty"`
	// RepositoryFacts is the bounded workspace probe captured when the shape
	// was built. Sufficiency uses it to decide whether a broad account needs an
	// implementation observation or documentation alone is all the project
	// has.
	RepositoryFacts RepositoryFacts `json:"repositoryFacts,omitempty"`
	// NeedsCodeNavigation reports that structural code navigation is relevant
	// (a symbol/package target, debugging, refactoring, or test work).
	NeedsCodeNavigation bool `json:"needsCodeNavigation,omitempty"`
	// NeedsExecution reports that the task asks for a process or check.
	NeedsExecution bool `json:"needsExecution,omitempty"`
	// NeedsRelationshipNavigation reports that the task asks about imports,
	// references, callers, callees, or dependency impact.
	NeedsRelationshipNavigation bool `json:"needsRelationshipNavigation,omitempty"`
	// RequiredKnowledge is the evidence the task needs to make progress.
	RequiredKnowledge []KnowledgeNeed `json:"requiredKnowledge,omitempty"`
	// Constraints are explicit limits the request states.
	Constraints []string `json:"constraints,omitempty"`
	// NamedEntities are the project/entity names the request preserves.
	NamedEntities []string `json:"namedEntities,omitempty"`
	// RequestedAction is the grounded action, or "unknown".
	RequestedAction string `json:"requestedAction,omitempty"`
	// Ambiguities are the request's unresolved dimensions.
	Ambiguities []string `json:"ambiguities,omitempty"`
	// Verification is what the contract requires after execution or mutation.
	Verification []contract.VerificationRequirement `json:"verification,omitempty"`
	// External is whether the task requires knowledge outside the workspace.
	External ExternalRequirement `json:"external"`
	// Completion lists the evidence conditions a truthful completion claim
	// would need. The agent completion evaluator remains authoritative.
	Completion []CompletionCondition `json:"completion,omitempty"`
	// Budget is an optional snapshot of the enclosing grant. Zero fields mean
	// unknown, never unlimited.
	Budget BudgetShape `json:"budget,omitempty"`
}

// Authority describes the turn's effective authority.
type Authority struct {
	// Workspace is the effective workspace label, e.g. "build".
	Workspace string `json:"workspace,omitempty"`
	// ReadOnly reports that repository mutation is not available.
	ReadOnly bool `json:"readOnly"`
	// MutationAllowed reports that the contract asks for a change and the
	// authority permits it. This is a description, not a grant.
	MutationAllowed bool `json:"mutationAllowed"`
	// ExecutionAllowed reports that the authority permits running commands.
	ExecutionAllowed bool `json:"executionAllowed"`
}

// MutationScope is the repository change surface.
type MutationScope struct {
	// Allowed reports that a repository change is both requested and within
	// the active authority.
	Allowed bool `json:"allowed"`
	// Explicit reports that the request named at least one target.
	Explicit bool `json:"explicit"`
	// Targets are the coordinates the request named.
	Targets []contract.Target `json:"targets,omitempty"`
	// Paths are the named file paths, repository-relative.
	Paths []string `json:"paths,omitempty"`
	// PresentPaths are the named paths observed before the run.
	PresentPaths []string `json:"presentPaths,omitempty"`
	// AbsentPaths are the named paths observed absent before the run.
	AbsentPaths []string `json:"absentPaths,omitempty"`
	// DeleteRequested reports that the request explicitly asks to remove a
	// path. A general change request must not silently offer deletion.
	DeleteRequested bool `json:"deleteRequested,omitempty"`
	// Operations are the requested change directions.
	Operations []contract.ChangeOp `json:"operations,omitempty"`
	// UnrelatedForbidden reports that changes outside Paths would contradict
	// the request.
	UnrelatedForbidden bool `json:"unrelatedForbidden"`
}

// KnowledgeNeed is one evidence requirement.
type KnowledgeNeed struct {
	// Key is a stable identity, e.g. "target_state:README.md".
	Key string `json:"key"`
	// Reason explains why the evidence matters.
	Reason string `json:"reason"`
}

// ExternalRequirement describes external knowledge use.
type ExternalRequirement struct {
	// Required reports that the task explicitly needs external information.
	Required bool `json:"required"`
	// Reason explains the classification.
	Reason string `json:"reason,omitempty"`
}

// CompletionCondition is one evidence condition for completion.
type CompletionCondition struct {
	// Key is a stable identity.
	Key string `json:"key"`
	// Description is the model-facing condition.
	Description string `json:"description"`
}

// BudgetShape is an optional resource snapshot.
type BudgetShape struct {
	MaxTurns      int `json:"maxTurns,omitempty"`
	TurnsUsed     int `json:"turnsUsed,omitempty"`
	MaxToolCalls  int `json:"maxToolCalls,omitempty"`
	ToolCallsUsed int `json:"toolCallsUsed,omitempty"`
}

// BudgetShapeFrom projects a root grant snapshot into the shape vocabulary.
// A zero snapshot means "no grant information", never unlimited resources.
func BudgetShapeFrom(snapshot budget.Snapshot) BudgetShape {
	return BudgetShape{
		MaxTurns:      snapshot.Limits.Turns,
		TurnsUsed:     snapshot.Used.Turns,
		MaxToolCalls:  snapshot.Limits.ToolCalls,
		ToolCallsUsed: snapshot.Used.ToolCalls,
	}
}

// Exhausted reports whether the snapshot's own bounds are already reached.
func (b BudgetShape) Exhausted() bool {
	return (b.MaxTurns > 0 && b.TurnsUsed >= b.MaxTurns) ||
		(b.MaxToolCalls > 0 && b.ToolCallsUsed >= b.MaxToolCalls)
}

// RepositoryFacts is the result of a cheap, model-free workspace probe.
//
// It is not a repository index and never substitutes for evidence. It exists so
// the runtime does not offer Go structural tools in a project with no Go
// sources, or hide them in a Go project before the model has read anything.
type RepositoryFacts struct {
	Go         bool     `json:"go,omitempty"`
	JavaScript bool     `json:"javascript,omitempty"`
	Python     bool     `json:"python,omitempty"`
	Rust       bool     `json:"rust,omitempty"`
	Make       bool     `json:"make,omitempty"`
	Markers    []string `json:"markers,omitempty"`
	RootFiles  int      `json:"rootFiles,omitempty"`
}

// HasLanguage reports whether the probe found the named language.
func (f RepositoryFacts) HasLanguage(language string) bool {
	switch strings.ToLower(strings.TrimSpace(language)) {
	case "go":
		return f.Go
	case "javascript", "js", "typescript", "ts":
		return f.JavaScript
	case "python":
		return f.Python
	case "rust":
		return f.Rust
	default:
		// An unknown language requirement is not evidence of absence.
		return true
	}
}

// HasSourceCode reports whether the probe found a language whose
// implementation can be observed directly. A project with only documentation
// may still have a complete survey; a code project needs at least one source
// or structural observation before a broad account can be called sufficient.
func (f RepositoryFacts) HasSourceCode() bool {
	return f.Go || f.JavaScript || f.Python || f.Rust
}

// ProbeFacts reads a few marker files at the workspace root.
//
// The probe is deliberately bounded and silent: a missing or unreadable marker
// is simply absent. It is a projection hint, not evidence, and no completion
// decision may depend on it.
func ProbeFacts(root string) RepositoryFacts {
	var facts RepositoryFacts
	if strings.TrimSpace(root) == "" {
		return facts
	}
	markers := []struct {
		name     string
		language string
	}{
		{"go.mod", "go"},
		{"go.work", "go"},
		{"package.json", "javascript"},
		{"tsconfig.json", "javascript"},
		{"pyproject.toml", "python"},
		{"requirements.txt", "python"},
		{"Cargo.toml", "rust"},
		{"Makefile", "make"},
	}
	for _, marker := range markers {
		if info, err := os.Stat(filepath.Join(root, marker.name)); err == nil && !info.IsDir() {
			facts.Markers = append(facts.Markers, marker.name)
			switch marker.language {
			case "go":
				facts.Go = true
			case "javascript":
				facts.JavaScript = true
			case "python":
				facts.Python = true
			case "rust":
				facts.Rust = true
			case "make":
				facts.Make = true
			}
		}
	}
	// A Go repository often has .go files without go.mod (a fixture or a
	// GOPATH-style tree). One directory read is cheap and keeps the structural
	// tool surface honest.
	entries, err := os.ReadDir(root)
	if err == nil {
		facts.RootFiles = len(entries)
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			if strings.HasSuffix(entry.Name(), ".go") {
				facts.Go = true
			}
		}
	}
	sort.Strings(facts.Markers)
	return facts
}

// ShapeParams carries the non-contract facts BuildShape needs.
type ShapeParams struct {
	// Authority is the effective workspace label, e.g. "build", "ask", "fuzz".
	Authority string
	// Intent is the routed intent, e.g. "read", "execute", "investigate".
	Intent string
	// Phase marks the informational phase of a deferred higher-authority
	// objective. The host sets it from workspace.PhaseContract.
	Phase bool
	// Facts is the bounded workspace probe.
	Facts RepositoryFacts
	// Snapshot is the pre-run target evidence, when the caller captured it.
	// It lets the shape know whether a named path already exists; it never
	// authorizes anything.
	Snapshot *agent.TargetSnapshot
	// Budget is an optional resource snapshot.
	Budget BudgetShape
}

// BuildShape derives a TaskShape from a completion contract.
//
// The derivation is finite and declarative: it names the evidence, mutation
// scope, verification, and completion conditions the contract implies. It
// never turns a request into a sequence of steps. An empty authority defaults
// to BUILD semantics, preserving the pre-Phase-9 behaviour for embedders that
// never resolved a workspace.
func BuildShape(intent contract.IntentContract, params ShapeParams) TaskShape {
	authority := strings.ToLower(strings.TrimSpace(params.Authority))
	if authority == "" {
		authority = "build"
	}
	shape := TaskShape{
		Version:         ShapeVersion,
		Objective:       strings.TrimSpace(intent.Objective),
		Kind:            intent.Kind,
		Intent:          strings.ToLower(strings.TrimSpace(params.Intent)),
		Phase:           params.Phase,
		RepositoryFacts: params.Facts,
		Authority: Authority{
			Workspace:        authority,
			ExecutionAllowed: authority == "build" || authority == "fuzz",
		},
		Verification: append([]contract.VerificationRequirement(nil), intent.Verification...),
		Budget:       params.Budget,
	}
	if shape.Kind == "" {
		shape.Kind = contract.ObjectiveInformational
	}
	if shape.Intent == "" {
		switch shape.Kind {
		case contract.ObjectiveMutation, contract.ObjectiveExecution:
			shape.Intent = "execute"
		default:
			shape.Intent = "read"
		}
	}
	shape.Investigation = shape.Intent == "investigate" ||
		(intent.Grounding != nil && intent.Grounding.HasAction(contract.GroundingInvestigate))

	shape.RepositoryScope = repositoryScope(shape.Objective, intent)
	shape.BroadRepositoryAnalysis = shape.RepositoryScope && broadRepositoryAnalysis(shape.Objective, intent)
	shape.NeedsCodeNavigation = needsCodeNavigation(shape.Objective, intent)
	shape.NeedsRelationshipNavigation = needsRelationshipNavigation(shape.Objective)
	// An explicit Gry/MCP request is structural by definition.
	if strings.Contains(strings.ToLower(shape.Objective), "gry") ||
		strings.Contains(strings.ToLower(shape.Objective), "mcp") {
		shape.NeedsCodeNavigation = true
		shape.NeedsRelationshipNavigation = true
	}
	shape.NeedsExecution = shape.Kind == contract.ObjectiveExecution ||
		len(shape.Verification) > 0 || needsExecution(shape.Objective)
	shape.External = externalRequirement(shape.Objective)
	shape.Constraints = constraintVocabulary(shape.Objective)
	if intent.ConditionalMutation {
		// A staged request scopes its no-change wording to the investigation
		// phase. That phase runs under ASK/FUZZ authority, whose gate already
		// prevents mutation; keeping the limit global would veto the later
		// BUILD branch the request explicitly allows.
		shape.Constraints = withoutConstraint(shape.Constraints, "no_mutation", "no_creation")
	}
	if intent.Grounding != nil {
		shape.NamedEntities = intent.Grounding.EntityValues()
		shape.RequestedAction = string(intent.Grounding.Action)
		for _, ambiguity := range intent.Grounding.Ambiguities {
			if ambiguity.Kind != "" {
				shape.Ambiguities = appendUnique(shape.Ambiguities, ambiguity.Kind)
			}
		}
	}

	shape.Mutation = MutationScope{
		Allowed:  authority == "build" && shape.Kind == contract.ObjectiveMutation,
		Explicit: len(intent.Targets) > 0,
		Targets:  append([]contract.Target(nil), intent.Targets...),
	}
	shape.Authority.MutationAllowed = shape.Mutation.Allowed
	shape.Authority.ReadOnly = !shape.Mutation.Allowed
	for _, target := range intent.Targets {
		if target.Kind == contract.TargetPath {
			if path := cleanPath(target.Value); path != "" {
				shape.Mutation.Paths = appendUnique(shape.Mutation.Paths, path)
			}
		}
		shape.Mutation.Operations = appendOperation(shape.Mutation.Operations, intent.OperationFor(target))
	}
	sort.Strings(shape.Mutation.Paths)
	shape.Mutation.DeleteRequested = requestsDelete(shape.Objective)
	shape.Mutation.UnrelatedForbidden = shape.Mutation.Allowed && shape.Mutation.Explicit
	// An explicit "do not modify anything" is an authority constraint, not a
	// suggestion to the model. It narrows the shape even when the verb would
	// otherwise request a change; the result may be NEEDS_REVIEW, which is
	// truthful for a contradictory request.
	if hasConstraint(shape.Constraints, "no_mutation") {
		shape.Mutation.Allowed = false
		shape.Authority.MutationAllowed = false
		shape.Authority.ReadOnly = true
		shape.Mutation.UnrelatedForbidden = false
	}
	// External knowledge is optional unless the task needs it. A request that
	// forbids it keeps the tools withheld even if it also names a URL.
	if hasConstraint(shape.Constraints, "no_external") {
		shape.External.Required = false
		shape.External.Reason = "the request forbids external knowledge"
	}
	if params.Snapshot != nil {
		for _, entry := range params.Snapshot.Entries {
			if entry.Target.Kind != contract.TargetPath {
				continue
			}
			path := cleanPath(entry.Target.Value)
			if path == "" || len(entry.Files) == 0 {
				continue
			}
			if entry.Files[0].Exists {
				shape.Mutation.PresentPaths = appendUnique(shape.Mutation.PresentPaths, path)
			} else {
				shape.Mutation.AbsentPaths = appendUnique(shape.Mutation.AbsentPaths, path)
			}
		}
		sort.Strings(shape.Mutation.PresentPaths)
		sort.Strings(shape.Mutation.AbsentPaths)
	}

	shape.RequiredKnowledge = requiredKnowledge(shape, intent)
	shape.Completion = completionConditions(shape, intent)
	return shape
}

// Summary renders one bounded line for audit and projection.
func (s TaskShape) Summary() string {
	var parts []string
	parts = append(parts, string(s.Kind))
	if s.Mutation.Allowed {
		scope := "unbounded"
		if len(s.Mutation.Paths) > 0 {
			scope = strings.Join(s.Mutation.Paths, ",")
		}
		parts = append(parts, "mutation="+scope)
		if s.Mutation.UnrelatedForbidden {
			parts = append(parts, "unrelated=forbidden")
		}
	} else {
		parts = append(parts, "mutation=forbidden")
	}
	if s.RepositoryScope {
		parts = append(parts, "repository=yes")
	}
	if s.NeedsCodeNavigation {
		parts = append(parts, "code-nav=yes")
	}
	if s.NeedsExecution {
		parts = append(parts, "execution=yes")
	}
	if s.NeedsRelationshipNavigation {
		parts = append(parts, "relationships=yes")
	}
	if s.External.Required {
		parts = append(parts, "external=yes")
	}
	if len(s.NamedEntities) > 0 {
		parts = append(parts, "entities="+strings.Join(s.NamedEntities, ","))
	}
	if s.RequestedAction != "" {
		parts = append(parts, "action="+s.RequestedAction)
	}
	if len(s.Ambiguities) > 0 {
		parts = append(parts, "ambiguity="+strings.Join(s.Ambiguities, ","))
	}
	if len(s.Verification) > 0 {
		parts = append(parts, "verification=required")
	}
	if s.Authority.Workspace != "" {
		parts = append(parts, "authority="+s.Authority.Workspace)
	}
	return strings.Join(parts, "; ")
}

// repositoryScope reports whether the request is about the repository itself.
//
// The rules are conservative. A determiner-bound repository noun ("this
// workflow", "the tests") points at this repository's artifact. A deictic
// reference ("this", "our") combined with a hosting or CI noun ("GitHub", "the
// pipeline") also does: the question is about what this repository does, and
// the answer lives in its configuration. A short compound such as "project
// overview" carries the subject itself, and a breadth word with a structural
// concept ("overall structure") refers to the object in front of the agent. A
// general knowledge question ("what is a cron expression?", "what is the best
// CI?") matches none of them.
func repositoryScope(objective string, intent contract.IntentContract) bool {
	normalized := " " + normalizeWords(objective) + " "
	for _, phrase := range repositoryPhrases {
		if strings.Contains(normalized, phrase) {
			return true
		}
	}
	if hasConceptTopic(intent) {
		return true
	}
	if hasRepositorySubject(normalized) {
		return true
	}
	if containsAnyPhrase(normalized, wholeRepositoryPhrases) ||
		containsAnyPhrase(normalized, shortRepositorySubjectPhrases) {
		return true
	}
	// "Investigate the overall structure" refers to the object in front of the
	// agent even though it does not repeat "repository". The breadth word and
	// a structural concept together are the repository-scope signal.
	if containsAnyPhrase(normalized, broadScopePhrases) &&
		containsAnyPhrase(normalized, structuralConceptPhrases) {
		return true
	}
	if hasDeicticReference(normalized) && containsAnyWord(normalized, repositoryArtifactWords) {
		return true
	}
	// A mutation with no named coordinate is necessarily about the repository:
	// there is no other place the change could go.
	return intent.Kind == contract.ObjectiveMutation && len(intent.Targets) == 0
}

// hasConceptTopic reports whether the contract names a repository concept.
// The topic is neither a coordinate nor a project entity: it is the semantic
// subject whose implementation lives somewhere in the repository.
func hasConceptTopic(intent contract.IntentContract) bool {
	return intent.Grounding.HasConcept()
}

// hasRepositorySubject reports whether a request points at this repository as
// its subject.
//
// The word-aware form exists because natural modifiers sit between the
// determiner and the noun ("this Go e-commerce project", "our legacy
// codebase"). It stops at an auxiliary or verb so "this is a checkout project"
// is not mistaken for a reference to the repository in front of the agent.
func hasRepositorySubject(normalized string) bool {
	if containsAnyPhrase(normalized, repositorySubjectPhrases) {
		return true
	}
	words := strings.Fields(normalized)
	for i, word := range words {
		if !repositoryReferenceWords[word] {
			continue
		}
		for j := i + 1; j <= i+8 && j < len(words); j++ {
			if repositorySubjectBarrierWords[words[j]] {
				break
			}
			if repositorySubjectNouns[words[j]] {
				return true
			}
		}
	}
	return false
}

// repositoryReferenceWords bind the subject to the speaker's context.
var repositoryReferenceWords = map[string]bool{
	"this": true, "these": true, "those": true, "our": true, "its": true,
	"your": true, "their": true, "the": true,
}

// repositorySubjectNouns are the nouns that name the repository itself.
var repositorySubjectNouns = map[string]bool{
	"project": true, "repository": true, "repo": true, "codebase": true,
	"workspace": true, "system": true, "application": true, "app": true,
	"program": true, "product": true, "tool": true, "code": true,
}

// repositorySubjectBarrierWords end a subject noun phrase. A verb after the
// determiner means the sentence is about something else ("this is a project").
var repositorySubjectBarrierWords = map[string]bool{
	"is": true, "are": true, "was": true, "were": true, "be": true,
	"been": true, "being": true, "do": true, "does": true, "did": true,
	"can": true, "could": true, "will": true, "would": true, "should": true,
	"may": true, "might": true, "must": true, "has": true, "have": true,
	"had": true, "and": true, "or": true, "but": true,
	"for": true, "with": true, "from": true, "in": true, "on": true,
	"at": true, "by": true, "to": true, "of": true, "about": true,
	"into": true, "under": true, "over": true, "between": true,
	"after": true, "before": true, "through": true, "during": true,
	"without": true, "within": true, "than": true, "as": true, "like": true,
	"some": true, "any": true, "all": true, "every": true, "each": true,
	"no": true, "more": true, "most": true, "less": true, "least": true,
	"best": true, "a": true, "an": true,
}

// repositoryPhrases are determiner-bound references to this repository or one of
// its artifacts.
var repositoryPhrases = []string{
	" this project ", " the project ", " this repository ", " the repository ",
	" this repo ", " the repo ", " this codebase ", " the codebase ",
	" this code ", " the code ", " this tool ", " the tool ",
	" this application ", " the application ", " this app ", " the app ",
	" this workspace ", " the workspace ",
	" this system ", " the system ", " this program ", " the program ",
	" this product ", " the product ",
	" this workflow ", " the workflow ", " this ci ", " the ci ",
	" this pipeline ", " the pipeline ", " this release ", " the release ",
	" this deployment ", " the deployment ", " this deploy ", " the deploy ",
	" this script ", " the script ", " this command ", " the command ",
	" this commit ", " the commit ", " this branch ", " the branch ",
	" this build ", " the build ", " this test ", " the test ",
	" these tests ", " the tests ", " this suite ", " the suite ",
	" this config ", " the config ", " this configuration ", " the configuration ",
	" on github ", " to github ", " via github ",
	" on gitlab ", " to gitlab ", " via gitlab ",
}

// repositoryArtifactWords are nouns that name a repository artifact or the
// surface it is published to. They are only evidence of repository scope when
// the request also points at one with a deictic reference.
var repositoryArtifactWords = []string{
	"github", "gitlab", "bitbucket", "workflow", "workflows", "actions",
	"ci", "pipeline", "pipelines", "release", "releases", "deploy",
	"deployment", "deployments", "build", "builds", "test", "tests",
	"script", "config", "configuration", "command", "commit", "branch",
}

// hasDeicticReference reports whether the request points at something in the
// speaker's context rather than at the world in general.
func hasDeicticReference(normalized string) bool {
	for _, word := range []string{"this", "these", "those", "our", "its"} {
		if strings.Contains(normalized, " "+word+" ") {
			return true
		}
	}
	return false
}

// containsAnyWord reports whether normalized text contains any whole word.
func containsAnyWord(normalized string, words []string) bool {
	for _, word := range words {
		if strings.Contains(normalized, " "+word+" ") {
			return true
		}
	}
	return false
}

// needsCodeNavigation reports whether structural code navigation is relevant.
func needsCodeNavigation(objective string, intent contract.IntentContract) bool {
	for _, target := range intent.Targets {
		if target.Kind == contract.TargetSymbol || target.Kind == contract.TargetPackage {
			return true
		}
	}
	normalized := " " + normalizeWords(objective) + " "
	for _, word := range []string{
		"debug", "bug", "defect", "failing", "failure", "test", "tests",
		"refactor", "rename", "function", "symbol", "implementation",
		"caller", "callee", "package", "module", "interface", "struct",
		"stack trace", "compile", "build error",
		"explore", "exploring", "architecture", "structure", "overview",
		"graph", "dependency", "dependencies", "index", "indexed", "gry", "mcp",
	} {
		if strings.Contains(normalized, " "+word+" ") {
			return true
		}
	}
	return false
}

// requestsDelete reports whether the request explicitly asks to remove a path.
func requestsDelete(objective string) bool {
	normalized := " " + normalizeWords(objective) + " "
	for _, word := range []string{"delete", "deletes", "deleting", "remove", "removes", "removing", "drop", "drops", "unlink"} {
		if strings.Contains(normalized, " "+word+" ") {
			return true
		}
	}
	return false
}

// needsRelationshipNavigation reports whether the request asks a relationship
// question rather than merely touching code.
func needsRelationshipNavigation(objective string) bool {
	normalized := " " + normalizeWords(objective) + " "
	for _, phrase := range []string{
		"import", "imports", "imported", "dependency", "dependencies",
		"caller", "callers", "callee", "callees", "reference", "references",
		"call graph", "graph", "who calls", "impact", "affected",
		"architecture", "structure", "overview", "gry", "mcp",
		"model context protocol",
	} {
		if strings.Contains(normalized, " "+phrase+" ") {
			return true
		}
	}
	return false
}

// needsExecution reports whether the request names a process or check.
func needsExecution(objective string) bool {
	normalized := " " + normalizeWords(objective) + " "
	for _, word := range []string{
		"run", "runs", "running", "test", "tests", "build", "builds",
		"compile", "compiles", "verify", "verifies", "verification",
		"reproduce", "reproduces", "execute", "executes", "benchmark",
		"fuzz", "probe", "investigate",
	} {
		if strings.Contains(normalized, " "+word+" ") {
			return true
		}
	}
	return false
}

// externalRequirement reports whether the request explicitly needs external
// knowledge. The classification is conservative: an implicit "maybe docs would
// help" is not a requirement.
func externalRequirement(objective string) ExternalRequirement {
	normalized := " " + normalizeWords(objective) + " "
	if hasURL(objective) {
		return ExternalRequirement{Required: true, Reason: "the request names a URL"}
	}
	for _, phrase := range []string{
		"official documentation", "official docs", "latest release",
		"release notes", "search the web", "search online", "look up online",
		"web search", "from the web", "website", "published documentation",
		"external documentation", "up-to-date documentation", "current documentation",
		"gry", "mcp", "model context protocol",
	} {
		if strings.Contains(normalized, " "+phrase+" ") {
			return ExternalRequirement{Required: true, Reason: "the request asks for external knowledge"}
		}
	}
	return ExternalRequirement{Required: false, Reason: "no external knowledge was requested"}
}

// constraintVocabulary extracts explicit limits stated by the request.
func constraintVocabulary(objective string) []string {
	normalized := " " + normalizeWords(objective) + " "
	var out []string
	add := func(key string, phrases ...string) {
		for _, phrase := range phrases {
			if strings.Contains(normalized, " "+phrase+" ") {
				out = append(out, key)
				return
			}
		}
	}
	add("no_mutation", "do not modify anything", "do not change anything",
		"read only", "without modifying", "without changing")
	add("unrelated_mutation_forbidden", "do not modify any other file", "do not change any other file", "no other file")
	add("no_creation", "do not create it", "do not create the file", "do not create any file",
		"do not create files", "do not create new files")
	add("no_external", "do not use the web", "do not browse", "no external")
	return out
}

// withoutConstraint removes named constraints, preserving order.
func withoutConstraint(values []string, drop ...string) []string {
	if len(values) == 0 {
		return values
	}
	removed := make(map[string]bool, len(drop))
	for _, key := range drop {
		removed[key] = true
	}
	out := values[:0]
	for _, value := range values {
		if !removed[value] {
			out = append(out, value)
		}
	}
	return out
}

// hasConstraint reports whether the request stated a named limit.
func hasConstraint(constraints []string, key string) bool {
	for _, constraint := range constraints {
		if constraint == key {
			return true
		}
	}
	return false
}

// requestsContent reports whether the request asks about what a file says,
// rather than merely whether it exists or where it lives. The distinction is
// load-bearing: a complete directory listing proves target_state but not
// target_content.
func requestsContent(objective string) bool {
	normalized := " " + normalizeWords(objective) + " "
	for _, phrase := range []string{
		" read ", " reading ", " explain ", " explains ", " describe ",
		" what does ", " what is ", " what's ", " summarize ", " summarise ",
		" summary ", " contents ", " content ", " contains ", " declared ",
		" inspect ", " inspects ", " inspecting ", " examine ", " examines ",
		" examining ", " look at ", " open ", " opens ", " opening ",
		" review ", " reviews ", " reviewing ", " analyze ", " analyse ",
		" analyzes ", " analyses ", " analyzing ", " analysing ",
	} {
		if strings.Contains(normalized, phrase) {
			return true
		}
	}
	return false
}

// broadRepositoryAnalysis reports whether the request's objective is a
// repository-wide account rather than one targeted fact.
//
// The decision is semantic and deterministic: an explicit repository subject
// ("this project", "the codebase", "the system") combined with an
// understanding/overview/architecture concept is broad, even when the wording
// is not one of the canonical phrases. A named coordinate or a specific
// artifact ("this project's license", "the test suite") stays narrow unless
// the request explicitly widens to the whole repository. This is deliberately
// concept-based rather than a phrase whitelist: unusual formulations of the
// same objective must not fall back to the single-observation policy.
func broadRepositoryAnalysis(objective string, intent contract.IntentContract) bool {
	normalized := " " + normalizeWords(objective) + " "
	subject := hasRepositorySubject(normalized) ||
		containsAnyPhrase(normalized, shortRepositorySubjectPhrases)
	wholeRepository := containsAnyPhrase(normalized, wholeRepositoryPhrases)
	wholeScope := wholeRepository || containsAnyPhrase(normalized, broadScopePhrases)
	structural := containsAnyPhrase(normalized, structuralConceptPhrases)
	understanding := containsAnyPhrase(normalized, understandingConceptPhrases)
	narrowArtifact := containsAnyPhrase(normalized, narrowArtifactPhrases)

	// A named coordinate is a targeted objective. Only an explicit "whole
	// repository" request widens it again; "overall structure of the parser
	// package" is still about one package.
	if len(intent.Targets) > 0 && !wholeRepository {
		return false
	}
	// A named concept is the object of the understanding. "Understand how the
	// checkout flow works" is targeted even when the sentence also names this
	// project; only an explicit whole-repository request widens it.
	if hasConceptTopic(intent) && !wholeRepository {
		return false
	}
	// A specific artifact is narrow even when the sentence says "explain" or
	// "summarize". "Explain this project's license" is not an architecture
	// survey.
	if narrowArtifact && !wholeRepository && !structural {
		return false
	}
	if wholeScope && (subject || structural || understanding) {
		return true
	}
	if subject && (understanding || structural) {
		return true
	}
	if subject && strings.Contains(normalized, " about ") {
		return true
	}
	if subject && purposeQuestion(normalized) {
		return true
	}
	if subject && howItWorks(normalized) {
		return true
	}
	return false
}

// repositorySubjectPhrases are determiner-bound references to the repository
// as the subject of the request. They are matched with surrounding spaces.
var repositorySubjectPhrases = []string{
	" this project ", " the project ", " this repository ", " the repository ",
	" this repo ", " the repo ", " this codebase ", " the codebase ",
	" this code ", " the code ", " this workspace ", " the workspace ",
	" this system ", " the system ", " this application ", " the application ",
	" this app ", " the app ", " this program ", " the program ",
	" this product ", " the product ", " this tool ", " the tool ",
	" project is this ", " repository is this ", " repo is this ",
	" codebase is this ", " system is this ", " application is this ",
	" app is this ", " tool is this ", " product is this ",
}

// shortRepositorySubjectPhrases are compact, unambiguous projections of the
// repository subject ("project overview", "repo architecture"). They carry a
// structural or overview concept themselves, so they cannot be mistaken for a
// general question about an arbitrary project.
var shortRepositorySubjectPhrases = []string{
	" project overview ", " repository overview ", " repo overview ",
	" codebase overview ", " system overview ", " project architecture ",
	" repository architecture ", " repo architecture ", " codebase architecture ",
	" project structure ", " repository structure ", " repo structure ",
	" codebase structure ",
}

// wholeRepositoryPhrases explicitly widen the objective to the entire
// repository. They are the only phrases that override a named coordinate.
var wholeRepositoryPhrases = []string{
	" as a whole ", " whole project ", " whole repository ", " whole repo ",
	" whole codebase ", " whole system ", " entire project ", " entire repository ",
	" entire repo ", " entire codebase ", " entire system ", " end to end ",
	" everything in this project ", " everything in the project ",
	" everything in this repository ", " everything in the repository ",
}

// broadScopePhrases widen the objective without necessarily naming the whole
// repository. "Investigate the overall structure" is broad even though it does
// not repeat "repository".
var broadScopePhrases = []string{
	" overall ", " high level ", " high-level ", " big picture ",
	" comprehensive ", " comprehensive overview ", " general overview ",
	" full overview ", " broad overview ", " complete overview ",
}

// structuralConceptPhrases name the architecture/structure family.
var structuralConceptPhrases = []string{
	" architecture ", " architectural ", " structure ", " structural ",
	" component ", " components ", " module ", " modules ", " subsystem ",
	" subsystems ", " organization ", " organisation ", " layout ",
	" design ", " stack ", " technology stack ", " technologies ",
	" directory layout ", " directory structure ",
}

// understandingConceptPhrases name an objective to understand, explain, or
// summarize. The stems cover inflections without listing every phrase.
var understandingConceptPhrases = []string{
	" understand ", " understanding ", " explain ", " explains ", " explaining ",
	" explanation ", " describe ", " describes ", " describing ", " description ",
	" summarize ", " summarise ", " summarizes ", " summarises ", " summary ",
	" analyze ", " analyse ", " analyzes ", " analyses ", " analysis ",
	" inspect ", " inspection ", " investigate ", " investigation ",
	" explore ", " exploration ", " review ", " overview ", " walkthrough ",
	" purpose ", " functionality ", " capabilities ", " works ", " working ",
	" operate ", " operates ", " how it works ", " how this works ",
	" figure out ", " learn ", " tell me ", " help me ",
	" walk me through ", " get oriented ", " break down ",
}

// narrowArtifactPhrases name a specific repository artifact. A question about
// one of these is targeted, not a whole-project account.
var narrowArtifactPhrases = []string{
	" license ", " licence ", " version ", " readme ", " test ", " tests ",
	" suite ", " workflow ", " workflows ", " ci ", " pipeline ", " pipelines ",
	" config ", " configuration ", " script ", " scripts ", " command ",
	" commands ", " commit ", " commits ", " branch ", " branches ",
	" build ", " builds ", " release ", " releases ", " deploy ", " deployment ",
	" dependency ", " dependencies ", " file ", " files ", " line ", " lines ",
	" function ", " functions ", " symbol ", " symbols ", " package ",
	" packages ", " struct ", " interface ", " method ", " methods ",
	" error ", " errors ", " bug ", " bugs ", " issue ", " issues ",
}

// containsAnyPhrase reports whether normalized text contains any phrase. The
// phrase list is expected to carry its own surrounding spaces.
func containsAnyPhrase(normalized string, phrases []string) bool {
	for _, phrase := range phrases {
		if strings.Contains(normalized, phrase) {
			return true
		}
	}
	return false
}

// purposeQuestion reports whether the request asks what the repository is or
// does. A yes/no question ("does this system run automatically?") is targeted
// behaviour, not a whole-project account.
func purposeQuestion(normalized string) bool {
	trimmed := strings.TrimSpace(normalized)
	if !strings.HasPrefix(trimmed, "what ") && !strings.HasPrefix(trimmed, "whats ") {
		return false
	}
	return strings.Contains(trimmed, " is ") || strings.Contains(trimmed, " are ") ||
		strings.Contains(trimmed, " does ") || strings.Contains(trimmed, " do ")
}

// howItWorks reports an objective to understand the mechanism of the whole.
func howItWorks(normalized string) bool {
	if !strings.Contains(normalized, " how ") {
		return false
	}
	return containsAnyPhrase(normalized, []string{
		" works ", " work ", " operates ", " functions ", " fits together ",
		" put together ", " organized ", " organised ", " structured ",
	})
}

// requiredKnowledge derives the evidence needs from the shape.
func requiredKnowledge(shape TaskShape, intent contract.IntentContract) []KnowledgeNeed {
	var needs []KnowledgeNeed
	add := func(key, reason string) {
		for _, existing := range needs {
			if existing.Key == key {
				return
			}
		}
		needs = append(needs, KnowledgeNeed{Key: key, Reason: reason})
	}
	// An explicit external requirement is evidence the runtime must observe,
	// not a preference. Treating it as satisfied by local files let the
	// projector close the action surface and demand an answer that the
	// completion evaluator would then reject for lacking external evidence.
	if shape.External.Required {
		add("external_observation", "the request explicitly requires knowledge from an external capability")
	}
	// A conditional mutation starts with an investigation. Its literal targets
	// describe the later change branch, not evidence the investigation phase
	// must acquire first; requiring them here would report a future change as a
	// missing observation while the authority is still only investigating.
	phaseTargetsDeferred := shape.Phase && intent.ConditionalMutation
	stateReason := "the named repository path must be known"
	if shape.Kind == contract.ObjectiveMutation {
		stateReason = "the target's pre-change state must be known before it is changed"
	}
	if !phaseTargetsDeferred {
		for _, path := range shape.Mutation.Paths {
			add("target_state:"+path, stateReason)
		}
	}
	// A mutation, or the investigation phase of one, that already names a
	// concrete path does not need a separate coordinate lookup for every symbol
	// in the same request: the path is the edit/inspection boundary and
	// completion re-resolves the symbol after the change. A targetless mutation
	// or an ordinary informational question still needs the lookup, because it
	// has no other way to locate the subject.
	pathScopedTarget := (shape.Kind == contract.ObjectiveMutation || shape.Phase) &&
		len(shape.Mutation.Paths) > 0
	for _, target := range intent.Targets {
		if pathScopedTarget {
			continue
		}
		switch target.Kind {
		case contract.TargetSymbol, contract.TargetPackage:
			add("target_coordinates:"+target.Value, "the requested declaration or package must be located")
		}
	}
	switch shape.Kind {
	case contract.ObjectiveMutation:
		if shape.RepositoryScope || len(intent.Targets) == 0 {
			add("repository_overview", "a repository change needs enough structure to place it correctly")
		}
		if len(shape.Verification) > 0 {
			add("verification", "the contract requires a successful check after the change")
		}
	case contract.ObjectiveExecution:
		add("execution_result", "the request asks for a process or check to run")
		if len(shape.Verification) > 0 {
			add("verification", "the request asks for a successful check")
		}
	case contract.ObjectiveInformational:
		if !phaseTargetsDeferred {
			for _, path := range shape.Mutation.Paths {
				add("target_state:"+path, "the answer names a concrete repository path")
				if requestsContent(shape.Objective) {
					add("target_content:"+path, "the answer needs the path's contents, not merely its presence")
				}
			}
		}
		if !pathScopedTarget {
			for _, target := range intent.Targets {
				switch target.Kind {
				case contract.TargetSymbol, contract.TargetPackage:
					add("target_coordinates:"+target.Value, "the answer names a declaration or package")
				}
			}
		}
		if shape.RepositoryScope {
			add("repository_overview", "the answer is about the repository")
			if shape.BroadRepositoryAnalysis {
				add("repository_survey", "a broad overview needs a second content observation or the repository's structure")
			}
		}
	}
	return needs
}

// completionConditions derives the deterministic completion conditions.
func completionConditions(shape TaskShape, intent contract.IntentContract) []CompletionCondition {
	var out []CompletionCondition
	switch shape.Kind {
	case contract.ObjectiveInformational:
		out = append(out, CompletionCondition{Key: "answer_present", Description: "a non-empty answer was produced"})
	case contract.ObjectiveExecution:
		out = append(out, CompletionCondition{Key: "execution_observed", Description: "a requested process reached a successful terminal state"})
		if len(shape.Verification) > 0 {
			out = append(out, CompletionCondition{Key: "verification_after_execution", Description: "a successful check followed the execution"})
		}
	case contract.ObjectiveMutation:
		if len(shape.Mutation.Paths) > 0 {
			for _, path := range shape.Mutation.Paths {
				out = append(out, CompletionCondition{Key: "target_mutated:" + path, Description: path + " changed in the requested direction"})
			}
		} else {
			out = append(out, CompletionCondition{Key: "mutation_observed", Description: "a repository change was observed"})
		}
		if len(shape.Verification) > 0 {
			out = append(out, CompletionCondition{Key: "verification_after_change", Description: "a successful check followed the last change"})
		}
		if shape.Mutation.UnrelatedForbidden {
			out = append(out, CompletionCondition{Key: "no_unrelated_mutation", Description: "no path outside the requested scope changed"})
		}
	}
	return out
}

// normalizeWords lowercases text and collapses every non-alphanumeric run.
func normalizeWords(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	space := false
	for _, r := range strings.ToLower(text) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
			space = false
			continue
		}
		if !space {
			b.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimSpace(b.String())
}

// cleanPath normalizes a repository-relative path.
func cleanPath(value string) string {
	value = strings.TrimSpace(strings.Trim(value, "`'\""))
	value = strings.TrimPrefix(value, "./")
	value = filepath.ToSlash(filepath.Clean(value))
	if value == "." || value == "" || strings.HasPrefix(value, "../") || strings.HasPrefix(value, "/") {
		return ""
	}
	return value
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func appendOperation(values []contract.ChangeOp, value contract.ChangeOp) []contract.ChangeOp {
	if value == "" {
		value = contract.ChangeAny
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

// hasURL reports whether text contains an http or https URL.
func hasURL(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "http://") || strings.Contains(lower, "https://")
}
