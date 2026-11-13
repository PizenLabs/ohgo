package agent

import "github.com/PizenLabs/ohgo/internal/control/contract"

// ActionClass is the kind of work a tool performs.
//
// It is runtime metadata, not authority. A class lets a task-aware projector
// narrow the action space — hide a mutating tool from a read-only task, for
// example — while the security boundary remains the only thing that can
// authorize a call. Tools declare classes through the optional ActionClassifier
// interface; a tool that declares none falls back to its approval tier.
type ActionClass string

// Action classes.
const (
	// ActionObserve reads repository state without changing it.
	ActionObserve ActionClass = "observe"
	// ActionMutate changes repository state.
	ActionMutate ActionClass = "mutate"
	// ActionExecute runs a process.
	ActionExecute ActionClass = "execute"
	// ActionExternal reaches knowledge outside the workspace.
	ActionExternal ActionClass = "external"
	// ActionConfigured marks a tool a human configured explicitly, such as an
	// MCP server capability. It is still external and still passes the
	// security boundary, but task-shape narrowing must not hide it merely
	// because the request did not ask for web knowledge: configuration is the
	// human's decision to make the capability available.
	ActionConfigured ActionClass = "configured"
	// ActionDelegate hands work to a child agent.
	ActionDelegate ActionClass = "delegate"
	// ActionPlan writes task guidance (a todo list, an artifact).
	ActionPlan ActionClass = "plan"
	// ActionRequiresExisting marks a tool that can only operate on a path that
	// already exists. A create-only target makes such a tool irrelevant.
	ActionRequiresExisting ActionClass = "requires_existing"
	// ActionRelationship marks a tool that answers import, reference, or
	// call-graph questions. Such a tool is relevant only when the task asks a
	// relationship question.
	ActionRelationship ActionClass = "relationship"
	// ActionStructure marks a language-neutral structural capability. It is
	// withheld when the task shape selected NONE activation.
	ActionStructure ActionClass = "structure"
	// ActionBackground observes or waits for detached work.
	ActionBackground ActionClass = "background"
)

// ActionClassifier reports the classes of work a tool performs.
//
// It is optional so existing tools and third-party embedders keep working. A
// zero-length result means "derive from Approval().Tier", never "no
// classification": an unclassified tool must remain available rather than
// disappear from the model's action space.
type ActionClassifier interface {
	ActionClasses() []ActionClass
}

// LanguageReporter reports the source language a tool needs.
//
// A structural Go tool over a repository with no Go sources cannot help, and
// showing its schema costs context on every turn. The empty string means the
// tool has no language requirement.
type LanguageReporter interface {
	RequiredLanguage() string
}

// LanguageSetReporter is the multi-language form of LanguageReporter. A tool
// that can read several adapters declares the set; the runtime offers it when
// the repository has at least one of those sources.
type LanguageSetReporter interface {
	SupportedLanguages() []string
}

// MutationReporter reports the repository change operations a tool performs.
//
// The op set is what lets a create-only task hide an edit/delete tool without
// guessing from its name. An empty result means the tool's mutation surface is
// not declared; such a tool is never hidden for an operation mismatch.
type MutationReporter interface {
	MutationOperations() []contract.ChangeOp
}

// policySensitive reports whether a capability can change the workspace, run a
// process, delegate, reach external knowledge, or was explicitly configured.
// Such a proposal is an attempted action, not observation continuation, so a
// refusal must stay visible to the user.
func policySensitive(tool Tool) bool {
	for _, class := range ToolActionClasses(tool) {
		switch class {
		case ActionMutate, ActionExecute, ActionDelegate, ActionExternal, ActionConfigured:
			return true
		}
	}
	return false
}

// ToolActionClasses returns the declared classes, or the approval-tier default.
func ToolActionClasses(tool Tool) []ActionClass {
	if classifier, ok := tool.(ActionClassifier); ok {
		if classes := classifier.ActionClasses(); len(classes) > 0 {
			return classes
		}
	}
	switch tool.Approval().Tier {
	case TierWrite:
		return []ActionClass{ActionMutate}
	case TierExec:
		return []ActionClass{ActionExecute}
	default:
		return []ActionClass{ActionObserve}
	}
}

// HasActionClass reports whether a tool declares a class.
func HasActionClass(tool Tool, class ActionClass) bool {
	for _, candidate := range ToolActionClasses(tool) {
		if candidate == class {
			return true
		}
	}
	return false
}

// ToolRequiredLanguage returns the tool's language requirement, or "".
func ToolRequiredLanguage(tool Tool) string {
	if reporter, ok := tool.(LanguageReporter); ok {
		return reporter.RequiredLanguage()
	}
	return ""
}

// ToolRequiredLanguages returns every source language the tool can use. A
// single-language reporter is projected into a one-element set.
func ToolRequiredLanguages(tool Tool) []string {
	if reporter, ok := tool.(LanguageSetReporter); ok {
		if languages := reporter.SupportedLanguages(); len(languages) > 0 {
			return languages
		}
	}
	if language := ToolRequiredLanguage(tool); language != "" {
		return []string{language}
	}
	return nil
}

// ToolMutationOperations returns the declared operations, or nil.
func ToolMutationOperations(tool Tool) []contract.ChangeOp {
	if reporter, ok := tool.(MutationReporter); ok {
		return reporter.MutationOperations()
	}
	return nil
}

// ToolOperations returns the repository operations a tool can perform.
//
// It is the capability view of a tool, derived from the same declarations the
// action-space narrowing reads, so the two cannot disagree: a tool offered for
// a task is a tool that can serve that task's required operation.
//
// The mapping is deliberately conservative. Observation classes serve reading;
// execution serves running; a mutation class serves the operations the tool
// declares, or every file operation when it declares none (an undeclared
// mutating tool must not be reported as unable to mutate). Bookkeeping and
// delegation add nothing: writing a todo list is not reading the repository,
// and a child agent is not itself a capability.
func ToolOperations(tool Tool) []contract.Operation {
	var ops []contract.Operation
	add := func(op contract.Operation) {
		for _, existing := range ops {
			if existing == op {
				return
			}
		}
		ops = append(ops, op)
	}
	classes := ToolActionClasses(tool)
	for _, class := range classes {
		switch class {
		case ActionObserve, ActionRelationship, ActionStructure:
			add(contract.OpRead)
		case ActionExecute:
			add(contract.OpExec)
			// A general process capability observes the repository: listing a
			// tree, reading a file, and searching text are commands it can run.
			// Counting it as a read capability is what keeps a shell-only
			// runtime able to answer a repository question, and it is the same
			// reason the sandbox classifies a reading command as read-only.
			add(contract.OpRead)
		case ActionMutate:
			added := false
			for _, change := range ToolMutationOperations(tool) {
				switch change {
				case contract.ChangeCreate:
					add(contract.OpWrite)
					added = true
				case contract.ChangeModify:
					add(contract.OpPatch)
					added = true
				case contract.ChangeDelete:
					add(contract.OpDelete)
					added = true
				case contract.ChangeAny:
					add(contract.OpWrite)
					add(contract.OpPatch)
					added = true
				}
			}
			if !added {
				add(contract.OpWrite)
				add(contract.OpPatch)
				add(contract.OpDelete)
			}
		}
	}
	return ops
}

// AvailableOperations returns every operation a tool set can perform.
//
// It is the control plane's availability fact for a capability preflight: the
// union of what the runtime was actually constructed with, never what it was
// configured to prefer.
func AvailableOperations(tools []Tool) []contract.Operation {
	var ops []contract.Operation
	for _, tool := range tools {
		for _, op := range ToolOperations(tool) {
			duplicate := false
			for _, existing := range ops {
				if existing == op {
					duplicate = true
					break
				}
			}
			if !duplicate {
				ops = append(ops, op)
			}
		}
	}
	return ops
}
