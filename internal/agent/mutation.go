package agent

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
