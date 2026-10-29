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

// MutationReporter reports the repository change operations a tool performs.
//
// The op set is what lets a create-only task hide an edit/delete tool without
// guessing from its name. An empty result means the tool's mutation surface is
// not declared; such a tool is never hidden for an operation mismatch.
type MutationReporter interface {
	MutationOperations() []contract.ChangeOp
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

// ToolMutationOperations returns the declared operations, or nil.
func ToolMutationOperations(tool Tool) []contract.ChangeOp {
	if reporter, ok := tool.(MutationReporter); ok {
		return reporter.MutationOperations()
	}
	return nil
}
