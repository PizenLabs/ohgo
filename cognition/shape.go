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

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/budget"
	"github.com/PizenLabs/ohgo/contract"
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
	// Authority is what the active turn may do. It describes authority; it
	// never grants it.
	Authority Authority `json:"authority"`
	// Mutation is the repository change scope.
	Mutation MutationScope `json:"mutation"`
	// RepositoryScope reports that the request is about the repository itself
	// rather than a general question.
	RepositoryScope bool `json:"repositoryScope,omitempty"`
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
		Version:   ShapeVersion,
		Objective: strings.TrimSpace(intent.Objective),
		Kind:      intent.Kind,
		Intent:    strings.ToLower(strings.TrimSpace(params.Intent)),
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

	shape.RepositoryScope = repositoryScope(shape.Objective, intent)
	shape.NeedsCodeNavigation = needsCodeNavigation(shape.Objective, intent)
	shape.NeedsRelationshipNavigation = needsRelationshipNavigation(shape.Objective)
	shape.NeedsExecution = shape.Kind == contract.ObjectiveExecution ||
		len(shape.Verification) > 0 || needsExecution(shape.Objective)
	shape.External = externalRequirement(shape.Objective)
	shape.Constraints = constraintVocabulary(shape.Objective)

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
		shape.Authority.ExecutionAllowed = false
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
	if len(s.Verification) > 0 {
		parts = append(parts, "verification=required")
	}
	if s.Authority.Workspace != "" {
		parts = append(parts, "authority="+s.Authority.Workspace)
	}
	return strings.Join(parts, "; ")
}

// repositoryScope reports whether the request is about the repository itself.
func repositoryScope(objective string, intent contract.IntentContract) bool {
	normalized := " " + normalizeWords(objective) + " "
	for _, phrase := range []string{
		" this project ", " the project ", " this repository ", " the repository ",
		" this repo ", " the repo ", " this codebase ", " the codebase ",
		" this code ", " the code ", " this tool ", " the tool ",
		" this application ", " the application ",
	} {
		if strings.Contains(normalized, phrase) {
			return true
		}
	}
	// A mutation with no named coordinate is necessarily about the repository:
	// there is no other place the change could go.
	return intent.Kind == contract.ObjectiveMutation && len(intent.Targets) == 0
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
	add("no_mutation", "do not modify anything", "do not change anything", "read only", "without modifying")
	add("unrelated_mutation_forbidden", "do not modify any other file", "do not change any other file", "no other file")
	add("no_creation", "do not create it", "do not create the file", "do not create any file")
	add("no_external", "do not use the web", "do not browse", "no external")
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
	for _, path := range shape.Mutation.Paths {
		add("target_state:"+path, "the target's pre-change state must be known before it is changed")
	}
	for _, target := range intent.Targets {
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
		for _, path := range shape.Mutation.Paths {
			add("target_state:"+path, "the answer names a concrete repository path")
		}
		for _, target := range intent.Targets {
			switch target.Kind {
			case contract.TargetSymbol, contract.TargetPackage:
				add("target_coordinates:"+target.Value, "the answer names a declaration or package")
			}
		}
		if shape.RepositoryScope {
			add("repository_overview", "the answer is about the repository")
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
