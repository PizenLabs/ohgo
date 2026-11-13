package agent

import "sort"

// MutationOp identifies a filesystem mutation kind.
type MutationOp string

const (
	// MutationCreate is a file that did not exist before.
	MutationCreate MutationOp = "CREATE"
	// MutationModify changes an existing file's contents.
	MutationModify MutationOp = "MODIFY"
	// MutationDelete removes a file.
	MutationDelete MutationOp = "DELETE"
)

// MutationPreview describes an intended mutation before it executes.
//
// It is produced by the execution/tool layer, never by a model, so an
// approval prompt can show what will actually change.
type MutationPreview struct {
	// Path is the target path, as supplied by the caller.
	Path string
	// Operation is the mutation kind.
	Operation MutationOp
	// MutationType mirrors Operation for consumers that name it differently.
	MutationType MutationOp
	// Type mirrors Operation.
	Type MutationOp
	// Op mirrors Operation.
	Op MutationOp
	// Diff is a unified-diff-like representation of the change.
	Diff string
	// Delta mirrors Diff.
	Delta string
	// Content mirrors Diff.
	Content string
	// Text mirrors Diff.
	Text string
	// Binary marks a target that cannot be represented as text.
	Binary bool
	// Unsupported marks a target for which no textual preview is available.
	Unsupported bool
}

// MutationEvidence describes the actual mutation after execution.
//
// It reflects the post-execution filesystem state, not the proposed one.
type MutationEvidence struct {
	Path         string
	Operation    MutationOp
	MutationType MutationOp
	Type         MutationOp
	Op           MutationOp
	Result       string
	Status       string
	Diff         string
	Delta        string
	Content      string
	Text         string
	Binary       bool
	Unsupported  bool
}

// MutationPreviewer is implemented by tools that can describe the mutation
// they are about to perform, before execution.
type MutationPreviewer interface {
	MutationPreview(args map[string]any) (MutationPreview, bool, error)
}

// MutationClass is the evaluator's classification of one observed change.
//
// The classes are deliberately distinct. A known change outside the requested
// scope is a different failure from a change the runtime cannot attribute at
// all, and both are different from a change that landed outside the workspace
// boundary.
type MutationClass string

// Mutation classifications.
const (
	// MutationExpected is a change inside the objective's authorized scope.
	MutationExpected MutationClass = "EXPECTED"
	// MutationUnexpected is an attributable change outside the requested
	// scope.
	MutationUnexpected MutationClass = "UNEXPECTED"
	// MutationUnattributed is a mutation the runtime observed but could not
	// tie to a path. It remains a review boundary.
	MutationUnattributed MutationClass = "UNATTRIBUTED"
	// MutationOutsideWorkspace is a change that landed outside the authorized
	// workspace.
	MutationOutsideWorkspace MutationClass = "OUTSIDE_WORKSPACE"
)

// MutationChange is one path observed to differ across a run.
type MutationChange struct {
	// Path is repository-relative and slash-separated.
	Path string
	// Status is "A", "M", or "D", matching the recovery diff vocabulary.
	Status string
}

// MutationProvenance is the run-level workspace mutation evidence.
//
// It is produced by the host's edit journal, which captures the workspace
// before the first mutating batch and after the run. It is runtime evidence,
// never model output. Captured distinguishes "the journal ran and observed no
// workspace change" from "no journal evidence is available"; the difference is
// load-bearing: only the second may leave a mutation unattributed.
type MutationProvenance struct {
	// RunID, TaskID, and ObjectiveID bind the evidence to the work it belongs
	// to. The journal supplies the run identity; the host supplies task
	// identity when it has one.
	RunID       string
	TaskID      string
	ObjectiveID string
	// CheckpointID and Turn identify the captured edit boundary.
	CheckpointID string
	Turn         int
	// Source names the capturing mechanism, e.g. "recovery.edit".
	Source string
	// Captured reports that a journal observed the run, even when Changes is
	// empty.
	Captured bool
	// Changes are the paths that differ between the pre-mutation capture and
	// the end of the run.
	Changes []MutationChange
}

// Paths returns the unique changed paths in sorted order.
func (p MutationProvenance) Paths() []string {
	seen := map[string]bool{}
	var out []string
	for _, change := range p.Changes {
		if change.Path == "" || seen[change.Path] {
			continue
		}
		seen[change.Path] = true
		out = append(out, change.Path)
	}
	sort.Strings(out)
	return out
}

// ClassifiedMutation is one observed change with its scope verdict.
type ClassifiedMutation struct {
	// Path is repository-relative and slash-separated, or the marker
	// "<unattributed>" when no path could be established.
	Path string
	// Status is the change direction when known.
	Status string
	// Class is the scope verdict.
	Class MutationClass
	// Source names how the path was established: "tool", "journal", or
	// "unattributed".
	Source string
}
