package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
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
		"find . -name '*.go'",
		"find . -type f -exec grep -l x {} +",
		"echo done 2>/dev/null",
		"go test ./... 2>&1 | tee /dev/null",
		"FOO=bar go test ./...",
		"sudo git status",
		// File names and script text are not option words. A read-only sed or
		// formatter must stay executable in FUZZ even when the path contains
		// the letters of an in-place flag.
		"sed -n '1,120p' src/client/components/Checkout.tsx",
		"sed -n '1,80p' src/client/components/Cart.tsx",
		"gofmt -l internal/widget",
		"goimports -l internal/widget",
		"perl -ne 'print if /i/' src/client/components/Checkout.tsx",
		"perl -Mstrict -e 'print 1'",
		"perl -w -e 'print 1'",
		"sed -e 's/widget/thing/' src/client/Checkout.tsx",
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
		"find . -name '*.tmp' -delete",
		"find . -name '*.go' -fprint list.txt",
		"sed -i 's/a/b/' file.go",
		"sed -i.bak 's/a/b/' file.go",
		"sed --in-place 's/a/b/' file.go",
		"sed -ni 's/a/b/p' file.go",
		"sed -e 's/a/b/' -i file.go",
		"perl -i -pe 's/a/b/' file.go",
		"perl -pi -e 's/a/b/' file.go",
		"perl -e 's/a/b/' -i file.go",
		"gofmt -w .",
		"gofmt -l -w .",
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

// A tag listing is read-only. Classifying it as a mutation makes the runtime
// demand a check for a change that never happened; the run that motivated this
// test then spent its remaining turns trying to discover the change and was
// stopped as stalled with the release workflow still unedited.
func TestCommandCapabilityKeepsTagListingExecutable(t *testing.T) {
	listing := []string{
		"git tag",
		"git tag -l",
		"git tag --list",
		"git tag --list 'v*'",
		"git tag -n",
		"git tag -n5",
		"git tag -v v0.1.2",
		"git tag --contains HEAD",
		"git tag --sort=-v:refname",
		"cd repo && git tag | head -50 && git branch -a && echo '---status---' && git status --short",
	}
	for _, command := range listing {
		if got := commandCapability(command); got != agent.TierExec {
			t.Errorf("commandCapability(%q) = %s, want exec: listing tags does not change the repository", command, got)
		}
	}

	writing := []string{
		"git tag v0.1.3",
		"git tag -a v0.1.3 -m release",
		"git tag -d v0.1.2",
		"git tag -f v0.1.2 HEAD~1",
	}
	for _, command := range writing {
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

// A redirect outside the workspace is execution, not a repository change. A
// FUZZ investigation must be able to start a local server that logs to a
// temporary file; refusing that as a mutation stranded the run before it could
// answer.
func TestBashToolCapabilityKeepsOutsideRedirectsExecutable(t *testing.T) {
	root := t.TempDir()
	tool := NewBashTool(root)
	temp := t.TempDir()
	outside := filepath.Join(temp, "server.log")

	commands := []string{
		"python3 -m http.server 8000 > " + outside + " 2>&1 & sleep 1; curl -sI http://localhost:8000 | head",
		"echo started > " + filepath.Join(temp, "started.txt"),
		"echo failure 2>" + filepath.Join(temp, "error.log"),
		"printf started | tee " + outside,
		"nohup go test ./... > " + outside + " 2>&1 &",
	}
	for _, command := range commands {
		if got := tool.Capability(map[string]any{"command": command}); got != agent.TierExec {
			t.Errorf("Capability(%q) = %s, want exec: the target is outside the workspace", command, got)
		}
	}

	inside := []string{
		"echo changed > server.log",
		"echo changed > " + filepath.Join(root, "nested", "server.log"),
		"echo failure 2>error.log",
		"printf changed | tee server.log",
	}
	for _, command := range inside {
		if got := tool.Capability(map[string]any{"command": command}); got != agent.TierWrite {
			t.Errorf("Capability(%q) = %s, want write: the target is inside the workspace", command, got)
		}
	}
}

// The exported escape classifier follows the same root-aware rule, so a human
// escape and an agent shell call cannot disagree about an outside redirect.
func TestCommandMutatesInRootDistinguishesInsideAndOutsideTargets(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "log.txt")

	if CommandMutatesInRoot("echo x > "+outside, root) {
		t.Fatalf("CommandMutatesInRoot classified an outside redirect as a workspace mutation")
	}
	if !CommandMutatesInRoot("echo x > inside.txt", root) {
		t.Fatalf("CommandMutatesInRoot accepted a relative write")
	}
	if !CommandMutates("echo x > " + outside) {
		t.Fatalf("the root-less classifier stopped being conservative")
	}
}

// outsideWorkspacePath returns a path that is outside both root and the
// operating system's temporary area, without touching the filesystem.
func outsideWorkspacePath() string {
	temp := filepath.Clean(os.TempDir())
	return filepath.Join(filepath.Dir(temp), "ohgo-outside-workspace-test")
}

// A destructive command that names an outside target is refused before
// execution, even when the target is quoted or appears after another command.
func TestOutsideWorkspaceMutationRejectsAbsoluteAndTraversalWrites(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspace")
	outside := outsideWorkspacePath()
	for _, command := range []string{
		"rm -rf " + outside,
		`rm -rf "` + outside + `"`,
		"true; rm -rf " + outside,
		"echo x > " + outside,
		"cp inside.txt " + outside,
		"mkdir -p " + outside,
		"sed -i s/a/b/ " + outside,
	} {
		if target, rejected := OutsideWorkspaceMutation(command, root); !rejected {
			t.Errorf("OutsideWorkspaceMutation(%q) = allowed, want rejected", command)
		} else if target == "" {
			t.Errorf("OutsideWorkspaceMutation(%q) reported no target", command)
		}
	}

	relative, err := filepath.Rel(root, outside)
	if err != nil {
		t.Fatal(err)
	}
	if _, rejected := OutsideWorkspaceMutation("rm -rf "+relative, root); !rejected {
		t.Fatalf("a .. traversal outside the workspace was allowed: %s", relative)
	}
}

// An absolute path inside the workspace is authorized, and temporary staging is
// not a persistent repository write.
func TestOutsideWorkspaceMutationAllowsInsideAndTemporaryTargets(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspace")
	inside := filepath.Join(root, "app", "page.tsx")
	if _, rejected := OutsideWorkspaceMutation("rm -f "+inside, root); rejected {
		t.Fatalf("an absolute path inside the workspace was rejected")
	}
	temp := filepath.Join(os.TempDir(), "nextapp")
	for _, command := range []string{
		"mkdir -p " + temp,
		"rm -rf " + temp,
		"cp -r " + temp + "/. .",
	} {
		if target, rejected := OutsideWorkspaceMutation(command, root); rejected {
			t.Errorf("temporary staging %q was rejected at %q", command, target)
		}
	}
}

// A command's own syntax is deterministic evidence of the workspace paths it
// writes. This is the direct attribution path the journal supplements.
func TestCommandAffectedPaths(t *testing.T) {
	root := t.TempDir()
	cases := map[string]string{
		"echo x > app/page.tsx":        "app/page.tsx",
		`echo x > "app/page.tsx"`:      "app/page.tsx",
		"mkdir -p components/button":   "components/button",
		"cp notes.txt app/page.tsx":    "app/page.tsx",
		"rm -f app/page.tsx":           "app/page.tsx",
		"mv app/old.tsx app/new.tsx":   "app/new.tsx",
		"tee app/page.tsx < /dev/null": "app/page.tsx",
	}
	for command, want := range cases {
		found := false
		for _, path := range CommandAffectedPaths(command, root) {
			if path == want {
				found = true
			}
		}
		if !found {
			t.Errorf("CommandAffectedPaths(%q) = %v, want %q", command, CommandAffectedPaths(command, root), want)
		}
	}
}

// A git mutation stays a mutation when a global option sits between "git" and
// the subcommand. Reading the first non-flag word treated the option's value as
// the subcommand, so `git -C . commit` was admitted as an execution and the
// change it made never reached completion evidence.
func TestCommandCapabilitySeesGitMutationsBehindGlobalOptions(t *testing.T) {
	commands := []string{
		"git -C . commit -m x",
		"git -c core.pager=cat commit -m x",
		"git --git-dir .git commit -m x",
		"git --work-tree . commit -m x",
		"git -C . reset --hard",
		"git -C . clean -fd",
	}
	for _, command := range commands {
		if got := commandCapability(command); got != agent.TierWrite {
			t.Errorf("commandCapability(%q) = %s, want write: the subcommand mutates", command, got)
		}
	}

	// The same options on a read subcommand stay exec, so a global option is not
	// itself treated as a write.
	for _, command := range []string{
		"git -C . status",
		"git -c core.pager=cat log --oneline -5",
		"git --git-dir .git diff",
	} {
		if got := commandCapability(command); got != agent.TierExec {
			t.Errorf("commandCapability(%q) = %s, want exec", command, got)
		}
	}
}

// A positional name creates a branch or a remote, and a note subcommand writes
// notes. Only the flag forms were classified, so these writes looked like reads.
func TestCommandCapabilitySeesPositionalGitWrites(t *testing.T) {
	writing := []string{
		"git branch feature-x",
		"git branch feature-x main",
		"git remote add origin https://example.com/x.git",
		"git remote remove origin",
		"git notes add -m note HEAD",
	}
	for _, command := range writing {
		if got := commandCapability(command); got != agent.TierWrite {
			t.Errorf("commandCapability(%q) = %s, want write", command, got)
		}
	}

	reading := []string{
		"git branch",
		"git branch -a",
		"git branch -l 'release/*'",
		"git branch --show-current",
		"git remote",
		"git remote -v",
		"git notes list",
	}
	for _, command := range reading {
		if got := commandCapability(command); got != agent.TierExec {
			t.Errorf("commandCapability(%q) = %s, want exec", command, got)
		}
	}
}

// A complete enumeration against this workspace is absence evidence; a filtered,
// piped, conditional, or other-repository invocation is not.
func TestGitAbsenceNamespacesRequiresACompleteLocalEnumeration(t *testing.T) {
	root := "/workspace/project"
	cases := []struct {
		name     string
		command  string
		output   string
		exitCode int
		want     []string
	}{
		{"tags", "git tag -l", "", 0, []string{"tag"}},
		{"bare tag", "git tag", "", 0, []string{"tag"}},
		{"branches", "git branch -a", "", 0, []string{"branch"}},
		{"remotes", "git remote -v", "", 0, []string{"remote"}},
		{"compound", "git branch -a; git tag -l; git diff --stat", "a\nb\n", 0, []string{"branch", "tag"}},
		{"inside root", "git -C backend tag -l", "", 0, []string{"tag"}},
		{"unborn branch", "git log --oneline -5", "fatal: your current branch 'master' does not have any commits yet\n", 128, []string{"commit"}},
		{"scoped tag", "git tag -l 'v*'", "", 0, nil},
		{"piped tag", "git tag -l | head -20", "", 0, nil},
		{"conditional", "git tag -l || true", "", 0, nil},
		{"other repo", "git -C /other tag -l", "", 0, nil},
		{"other repo commit text", "git -C /other log", "No commits yet\n", 128, nil},
		{"not a repo", "git tag -l", "fatal: not a git repository\n", 0, nil},
		{"failed enumeration", "git branch -a", "", 1, nil},
		{"truncated enumeration", "git tag -l", "[output truncated at 30000 bytes]", 0, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := GitAbsenceNamespaces(tc.command, tc.output, tc.exitCode, root)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("GitAbsenceNamespaces(%q) = %v, want %v", tc.command, got, tc.want)
			}
		})
	}
}

// A successful read subcommand is a version-control observation; a mutating
// one, or a failed run, is not. A VCS-state question needs the former.
func TestGitObservationExcludesMutationsAndFailures(t *testing.T) {
	reading := []string{"git status", "git log --oneline -5", "git branch -a", "git tag -l", "git diff --stat"}
	for _, command := range reading {
		if !GitObservation(command, 0, "/workspace/project") {
			t.Errorf("GitObservation(%q) = false, want a completed inspection", command)
		}
	}
	for _, command := range []string{"git commit -m x", "git push", "git -C . reset --hard"} {
		if GitObservation(command, 0, "/workspace/project") {
			t.Errorf("GitObservation(%q) = true, want a change excluded", command)
		}
	}
	if GitObservation("git status", 128, "/workspace/project") {
		t.Error("a failed git command was recorded as an observation")
	}
	if GitObservation("go test ./...", 0, "/workspace/project") {
		t.Error("a non-git command was recorded as a version-control observation")
	}
	if GitObservation("git -C /other status", 0, "/workspace/project") {
		t.Error("another repository's state was recorded as this workspace's observation")
	}
}
