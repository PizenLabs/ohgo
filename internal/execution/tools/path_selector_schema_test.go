package tools

import (
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
)

// A selector the tool already treats as "unscoped" must also be valid in its
// published schema. A hosted provider validates generated tool arguments before
// the runtime sees them: an empty string the implementation accepts but the
// schema forbids becomes a rejected request, not a recoverable tool error.
func TestDefaultablePathSelectorsAcceptTheEmptyForm(t *testing.T) {
	root := structureFixture(t, map[string]string{
		"pkg/a.go": "package pkg\n\nfunc A() {}\n",
	})
	source := NewStructuralSource(root)

	for _, tool := range []struct {
		name string
		tool agent.Tool
		args map[string]any
	}{
		{"structure", NewStructureTool(source), map[string]any{"path": ""}},
		{"symbols", NewSymbolsToolWithSource(source), map[string]any{"path": ""}},
		{"refs", NewRefsTool(source), map[string]any{"path": "", "symbol": "A"}},
	} {
		if err := tool.tool.Parameters().Validate(tool.args); err != nil {
			t.Errorf("%s rejects the empty path its implementation defaults: %v", tool.name, err)
		}
	}
}
