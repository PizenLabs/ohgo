package tools

import (
	"testing"

	"github.com/PizenLabs/ohgo/agent"
)

func TestCommandCapabilityClassifiesExecutionAsExec(t *testing.T) {
	// FUZZ must be able to run tests, builds, and read-only git, so these stay
	// exec and are permitted wherever execution is permitted (§5.1).
	commands := []string{
		"go test ./...",
		"go build ./...",
		"go vet ./...",
		"gofmt -l .",
		"make test",
		"cargo test",
		"npm test",
		"pytest -q",
		"ls -la internal",
		"cat internal/runtime.go",
		"grep -rn TODO .",
		"rg 'func main'",
		"git status --short",
		"git log --oneline -5",
		"git diff HEAD~1",
		"git show HEAD",
		"git branch",
		"git config --get user.name",
		"git stash list",
		"echo done 2>/dev/null",
		"go test ./... 2>&1 | tee /dev/null",
		"FOO=bar go test ./...",
		"sudo git status",
	}
	for _, command := range commands {
		if got := commandCapability(command); got != agent.TierExec {
			t.Errorf("commandCapability(%q) = %s, want exec", command, got)
		}
	}
}

func TestCommandCapabilityClassifiesMutationAsWrite(t *testing.T) {
	// A shell command that rewrites the repository is a mutation, and the
	// authority layer must see it as one so ASK/FUZZ cannot use the shell as a
	// back door (§5.3).
	commands := []string{
		"rm -rf build",
		"rm internal/x.go",
		"mv a.go b.go",
		"cp config.go config.bak.go",
		"mkdir -p internal/tmp",
		"touch internal/new.go",
		"truncate -s 0 log.txt",
		"dd if=/dev/zero of=big.bin bs=1 count=1",
		"chmod +x script.sh",
		"ln -s a b",
		"echo 'package main' > internal/generated.go",
		"echo more >> notes.txt",
		"cat template.txt > out.txt",
		"sed -i 's/a/b/' file.go",
		"perl -i -pe 's/a/b/' file.go",
		"gofmt -w .",
		"goimports -w .",
		"git commit -m 'x'",
		"git add .",
		"git checkout main",
		"git switch -c feature",
		"git reset --hard",
		"git clean -fd",
		"git apply patch.diff",
		"git branch -D old",
		"git config user.name someone",
		"go mod tidy",
		"go get example.com/pkg",
		"go install example.com/tool@latest",
		"cargo add serde",
		"npm install",
		"npm ci",
		"yarn add left-pad",
		"pip install requests",
		"pnpm rm lodash",
		"echo hi 2> err.log",
		"FOO=1 rm file",
		"cd sub && rm x",
		"(rm x)",
		"true; rm x",
		"rm a || rm b",
	}
	for _, command := range commands {
		if got := commandCapability(command); got != agent.TierWrite {
			t.Errorf("commandCapability(%q) = %s, want write", command, got)
		}
	}
}

func TestCommandCapabilityIgnoresQuotedRedirectsAndText(t *testing.T) {
	// A '>' inside quotes is data, not a redirect: a false write classification
	// would block a legitimate read-only FUZZ command.
	commands := []string{
		`echo "a > b"`,
		`grep '>' file.go`,
		`printf '%s\n' "x > y"`,
	}
	for _, command := range commands {
		if got := commandCapability(command); got != agent.TierExec {
			t.Errorf("commandCapability(%q) = %s, want exec", command, got)
		}
	}
}

func TestBashToolCapabilityUsesTheCommand(t *testing.T) {
	tool := NewBashTool(t.TempDir())
	if got := tool.Capability(map[string]any{"command": "go test ./..."}); got != agent.TierExec {
		t.Errorf("go test capability = %s, want exec", got)
	}
	if got := tool.Capability(map[string]any{"command": "rm -rf ."}); got != agent.TierWrite {
		t.Errorf("rm capability = %s, want write", got)
	}
	// Malformed or missing arguments classify as write: a call that might
	// mutate must not be admitted on a parsing failure.
	if got := tool.Capability(map[string]any{}); got != agent.TierWrite {
		t.Errorf("missing command capability = %s, want write", got)
	}
	if got := tool.Capability(map[string]any{"command": 42}); got != agent.TierWrite {
		t.Errorf("non-string command capability = %s, want write", got)
	}
}
