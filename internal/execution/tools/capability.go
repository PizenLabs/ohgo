package tools

import (
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/PizenLabs/ohgo/internal/agent"
)

// commandCapability classifies a shell invocation for the runtime authority
// layer.
//
// The declared tier of the bash tool is exec, because that is what it usually
// does. But a shell command can also change the repository, and the authority
// boundary must see that: FUZZ may run a build but must not rewrite a file
// (§5.2, §5.3). This classification is conservative on purpose.
//
// It is a risk and evidence input, not the no-mutation boundary. The security
// decision carries a ReadOnly bit derived from the authority, and the bash tool
// enforces it with an OS sandbox; that is what stops a program this classifier
// does not recognise. A classifier can never be the whole boundary for an
// arbitrary command.
func commandCapability(command string) agent.Tier {
	if commandWrites(command) {
		return agent.TierWrite
	}
	return agent.TierExec
}

// commandCapabilityInRoot classifies a shell invocation against the workspace
// it will run in.
//
// A redirect to an absolute path outside the workspace writes somewhere else,
// not into the repository. FUZZ may execute such a command: starting a local
// server that logs to a temporary file is an observation, not a source change.
// The root-less classifier remains conservative because it cannot prove where a
// target lands.
func commandCapabilityInRoot(command, root string) agent.Tier {
	if commandWritesInRoot(command, root) {
		return agent.TierWrite
	}
	return agent.TierExec
}

// CommandMutates reports whether a shell command provably changes the
// workspace.
//
// It is the exported form of the admission classifier, so the "!" shell escape
// gates on exactly the rule the bash tool's admission uses. Only one
// classification exists; a second copy would be free to drift from what the
// runtime actually enforces.
func CommandMutates(command string) bool {
	return commandWrites(command)
}

// CommandMutatesInRoot reports whether a shell command provably changes the
// workspace rooted at root.
//
// It is the root-aware form of CommandMutates: a redirect to an absolute path
// outside root is not a repository change. Callers that know the workspace use
// this form so an execution workspace does not refuse an ordinary temporary
// probe.
func CommandMutatesInRoot(command, root string) bool {
	return commandWritesInRoot(command, root)
}

// CommandAbsence reports whether a finished shell command's non-zero exit is a
// successful negative observation rather than an execution failure.
//
// Search programs use exit code 1 for "nothing matched": grep, rg, and
// git grep all report a normal empty result that way. Treating it as a runtime
// failure turned a completed task into NEEDS_REVIEW merely because the model
// checked for a string that was no longer there. Exit code 2 and above remain
// real failures. Only the final executed segment is inspected: in a pipeline
// the exit status belongs to the last program, and a wrapper that is not a
// search program did not establish the absence.
func CommandAbsence(command string, exitCode int) bool {
	if exitCode != 1 {
		return false
	}
	segments := shellSegments(command)
	for i := len(segments) - 1; i >= 0; i-- {
		segment := strings.TrimSpace(segments[i])
		if segment == "" {
			continue
		}
		return segmentSearches(segment)
	}
	return false
}

// segmentSearches reports whether one shell segment runs a search program whose
// exit code 1 means "no matches".
func segmentSearches(segment string) bool {
	program, args := programAndArgs(shellWords(segment))
	switch path.Base(program) {
	case "grep", "egrep", "fgrep", "rg", "ag", "ack", "ack-grep":
		return true
	case "git":
		sub, _ := gitSubcommand(args)
		return sub == "grep"
	}
	return false
}

// This is a deterministic pre-execution boundary, not a classifier: it only
// names a target the command's own syntax spells out (a redirect, rm, cp, mv,
// mkdir, sed -i, …). It deliberately does not guess through command
// substitution, globs, or an interpreter whose arguments are opaque. The
// authoritative BUILD workspace is unrestricted for ordinary tool caches
// (npm, Go, cargo), so the boundary rejects persistent writes outside the
// workspace while allowing temporary execution artifacts such as
// /tmp/nextapp.
func OutsideWorkspaceMutation(command, root string) (string, bool) {
	root = strings.TrimSpace(root)
	if root == "" || strings.TrimSpace(command) == "" {
		return "", false
	}
	root = resolveCommandPath(root)
	for _, target := range commandWriteTargets(command, root) {
		if pathWithinRoot(root, target) || isTemporaryPath(target) {
			continue
		}
		return target, true
	}
	return "", false
}

// CommandAffectedPaths returns the repository-relative paths a command's own
// syntax deterministically writes.
//
// It is completion evidence, not authorization: a command this function cannot
// parse yields no paths and the host's edit journal remains the only source of
// attribution.
func CommandAffectedPaths(command, root string) []string {
	root = strings.TrimSpace(root)
	if root == "" || strings.TrimSpace(command) == "" {
		return nil
	}
	root = resolveCommandPath(root)
	seen := map[string]bool{}
	var out []string
	for _, target := range commandWriteTargets(command, root) {
		if !pathWithinRoot(root, target) {
			continue
		}
		rel, err := filepath.Rel(root, target)
		if err != nil || rel == "." || rel == "" || strings.HasPrefix(rel, "..") {
			continue
		}
		rel = filepath.ToSlash(rel)
		if !seen[rel] {
			seen[rel] = true
			out = append(out, rel)
		}
	}
	sort.Strings(out)
	return out
}

// commandWriteTargets resolves every deterministic write target in a command.
func commandWriteTargets(command, root string) []string {
	cwd := root
	var out []string
	for _, segment := range shellSegments(command) {
		words := shellWords(segment)
		program, args := programAndArgs(words)
		base := path.Base(program)
		if base == "cd" || base == "pushd" {
			raw := firstNonFlag(args)
			if raw == "" {
				// A bare cd goes to HOME, not to the current directory. Treating
				// it as a no-op would resolve a later relative write to the
				// workspace when the shell would actually write to HOME.
				if home, err := os.UserHomeDir(); err == nil {
					cwd = resolveCommandPath(home)
				}
				continue
			}
			if next, ok := resolveShellPath(raw, cwd); ok {
				cwd = next
			}
			continue
		}
		if base == "popd" {
			// A directory stack is not modelled. Leaving the cwd unchanged is
			// the safe direction for a boundary: it cannot turn an outside
			// write into a claimed workspace write.
			continue
		}
		out = append(out, redirectTargets(segment, cwd)...)
		if program == "" {
			continue
		}
		for _, raw := range writeProgramArgs(base, args) {
			if target, ok := resolveShellPath(raw, cwd); ok {
				out = append(out, target)
			}
		}
	}
	return out
}

// redirectTargets returns the resolved file targets of output redirections in
// one shell segment. Descriptor duplications and character devices are not
// filesystem mutations.
func redirectTargets(segment, cwd string) []string {
	var out []string
	quoted := false
	var quote rune
	runes := []rune(segment)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if quoted {
			if r == quote {
				quoted = false
			}
			continue
		}
		if r == '\'' || r == '"' {
			quoted = true
			quote = r
			continue
		}
		if r != '>' {
			continue
		}
		j := i + 1
		if j < len(runes) && runes[j] == '>' {
			j++
		}
		if j < len(runes) && runes[j] == '&' {
			i = j
			continue
		}
		for j < len(runes) && (runes[j] == ' ' || runes[j] == '\t') {
			j++
		}
		target := readWord(runes, j)
		if target == "" || isCharDevice(target) {
			continue
		}
		if resolved, ok := resolveShellPath(target, cwd); ok {
			out = append(out, resolved)
		}
	}
	return out
}

// writeProgramArgs returns the path arguments of a program whose only effect
// on each argument is a write or a removal.
func writeProgramArgs(program string, args []string) []string {
	nonFlags := func() []string {
		var out []string
		for _, arg := range args {
			if strings.HasPrefix(arg, "-") || arg == "--" {
				continue
			}
			out = append(out, arg)
		}
		return out
	}
	switch program {
	case "rm", "rmdir", "unlink", "shred", "touch", "mknod":
		return nonFlags()
	case "mkdir", "truncate":
		var out []string
		for i := 0; i < len(args); i++ {
			arg := args[i]
			if arg == "-m" || arg == "-s" || arg == "--mode" || arg == "--size" || arg == "-d" {
				i++
				continue
			}
			if strings.HasPrefix(arg, "-") {
				continue
			}
			out = append(out, arg)
		}
		return out
	case "cp", "install", "rsync", "ln":
		// Only the destination is written; the sources may be read from
		// anywhere, including a temporary staging directory.
		values := nonFlags()
		if len(values) < 2 {
			return nil
		}
		return values[len(values)-1:]
	case "mv":
		// The source leaves its path and the destination is written, so both
		// are workspace changes.
		return nonFlags()
	case "tee":
		return nonFlags()
	case "chmod", "chown", "chgrp":
		values := nonFlags()
		if len(values) < 2 {
			return nil
		}
		return values[1:]
	case "dd":
		var out []string
		for _, arg := range args {
			if strings.HasPrefix(arg, "of=") {
				out = append(out, strings.TrimPrefix(arg, "of="))
			}
		}
		return out
	case "sed":
		if !hasInPlaceFlag(program, args) {
			return nil
		}
		values := nonFlags()
		if len(values) < 2 {
			return nil
		}
		return values[1:]
	case "perl":
		if !hasInPlaceFlag(program, args) {
			return nil
		}
		var out []string
		skipScript := false
		for _, arg := range args {
			if skipScript {
				skipScript = false
				continue
			}
			if arg == "-e" || arg == "-E" {
				skipScript = true
				continue
			}
			if strings.HasPrefix(arg, "-") {
				// -eSCRIPT carries the script in the same word.
				continue
			}
			out = append(out, arg)
		}
		return out
	case "gofmt", "goimports":
		if !hasInPlaceFlag(program, args) {
			return nil
		}
		return nonFlags()
	}
	return nil
}

// resolveShellPath resolves one shell path argument against the command's
// current directory. A shell expansion or an empty value is not deterministic,
// so it returns false rather than guessing.
func resolveShellPath(raw, cwd string) (string, bool) {
	raw = strings.TrimSpace(strings.Trim(raw, `"'`))
	if raw == "" {
		return "", false
	}
	switch {
	case strings.ContainsAny(raw, "$`*?[]{}"):
		return "", false
	case raw == "~" || strings.HasPrefix(raw, "~/"):
		home, err := os.UserHomeDir()
		if err != nil {
			return "", false
		}
		raw = filepath.Join(home, strings.TrimPrefix(raw, "~"))
	case filepath.IsAbs(raw):
	default:
		raw = filepath.Join(cwd, raw)
	}
	return resolveCommandPath(raw), true
}

// pathWithinRoot reports whether target is inside root.
func pathWithinRoot(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// isTemporaryPath reports whether target is inside an operating-system
// temporary directory, where a command may stage execution artifacts.
func isTemporaryPath(target string) bool {
	candidates := []string{os.TempDir()}
	if value := strings.TrimSpace(os.Getenv("TMPDIR")); value != "" {
		candidates = append(candidates, value)
	}
	candidates = append(candidates, "/tmp", "/var/tmp")
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if pathWithinRoot(resolveCommandPath(candidate), target) {
			return true
		}
	}
	return false
}

// CommandDestructive reports whether a shell command is destructive or
// system-level, which the security layer classifies as high risk.
//
// It is conservative in the safe direction: a command this classifier does not
// recognise is merely medium risk, while one it does recognise is surfaced as
// high risk in the approval decision. It never decides authorization itself —
// risk is an input to policy, not a decision.
func CommandDestructive(command string) bool {
	for _, segment := range shellSegments(command) {
		words := shellWords(segment)
		program, args := programAndArgs(words)
		if program == "" {
			continue
		}
		if destructiveProgram(path.Base(program), args) {
			return true
		}
	}
	return false
}

// destructiveProgram reports whether a program is destructive or system-level.
func destructiveProgram(program string, args []string) bool {
	switch program {
	case "rm", "rmdir", "shred", "dd", "truncate", "mknod",
		"mkfs", "wipefs", "fdisk", "parted",
		"shutdown", "reboot", "halt", "poweroff",
		"kill", "killall", "pkill",
		"chmod", "chown", "chgrp",
		"systemctl", "launchctl", "docker", "kubectl":
		return true
	case "git":
		switch firstNonFlag(args) {
		case "clean", "reset", "push", "filter-branch", "gc", "prune", "rebase":
			return true
		}
	}
	return false
}

// commandWrites reports whether a shell command provably mutates the
// filesystem or the repository.
func commandWrites(command string) bool {
	return commandWritesInRoot(command, "")
}

// commandWritesInRoot reports whether a shell command provably mutates the
// repository rooted at root, or any filesystem when root is empty.
func commandWritesInRoot(command, root string) bool {
	if hasOutputRedirectInRoot(command, root) {
		return true
	}
	for _, segment := range shellSegments(command) {
		words := shellWords(segment)
		program, args := programAndArgs(words)
		if program == "" {
			continue
		}
		if mutatingProgramInRoot(path.Base(program), args, root) {
			return true
		}
	}
	return false
}

// mutatingProgramInRoot reports whether a program mutates when run with args.
// The root bounds the programs whose only effect is writing the file they name;
// other programs still mutate regardless of their arguments.
func mutatingProgramInRoot(program string, args []string, root string) bool {
	switch program {
	case "rm", "rmdir", "mv", "cp", "install", "touch", "truncate", "dd",
		"mkdir", "chmod", "chown", "chgrp", "ln", "shred", "rsync",
		"unlink", "mknod", "setfacl":
		return true
	case "tee":
		// tee writes to its targets, but a target that is a character device
		// (/dev/null) is not a repository mutation. An absolute target outside
		// the workspace is execution, for the same reason a redirect is.
		return writesToFileInRoot(args, root)
	case "sed", "perl", "gofmt", "goimports":
		return hasInPlaceFlag(program, args)
	case "npm", "pnpm", "yarn", "pip", "pip3", "cargo", "go", "composer",
		"bundle", "gem", "poetry", "uv":
		return hasMutatingSubcommand(program, args)
	case "git":
		return gitMutates(args)
	case "find":
		return findMutates(args)
	}
	return false
}

// findMutates reports find predicates whose own effect is a write: -delete
// removes the matched entries, and the -fprint family writes a file.
//
// -exec is deliberately not modelled. The executed program and its arguments
// are opaque to static parsing, and the OS sandbox remains the boundary that
// stops an unrecognised program.
func findMutates(args []string) bool {
	for _, arg := range args {
		switch arg {
		case "-delete", "-fprint", "-fprint0", "-fprintf":
			return true
		}
	}
	return false
}

// writesToFileInRoot reports whether any non-flag argument names a real file,
// as opposed to a character device or a path outside root.
func writesToFileInRoot(args []string, root string) bool {
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			continue
		}
		if isCharDevice(arg) {
			continue
		}
		if redirectWritesOutsideRoot(arg, root) {
			continue
		}
		return true
	}
	return false
}

// hasInPlaceFlag reports an in-place edit flag (-i, -w), which turns a
// formatting/editing program into a writer.
//
// Only option words are inspected. A file name or script argument routinely
// contains 'i' or 'w' (src/client/Checkout.tsx), and scanning every word as if
// it were a flag classified a read-only sed as a mutation. That false positive
// made FUZZ refuse ordinary inspection.
func hasInPlaceFlag(program string, args []string) bool {
	for _, arg := range args {
		if arg == "--" {
			return false
		}
		if strings.HasPrefix(arg, "--") {
			// sed's --in-place[=SUFFIX] is the only long write flag among the
			// programs classified here. Other long options are not writes.
			if program == "sed" && (arg == "--in-place" || strings.HasPrefix(arg, "--in-place=")) {
				return true
			}
			continue
		}
		if len(arg) < 2 || arg[0] != '-' {
			continue
		}
		if shortOptionWrites(program, arg[1:]) {
			return true
		}
	}
	return false
}

// shortOptionWrites scans one short option cluster for an in-place flag.
//
// Some options consume the rest of their word (-Mstrict), so scanning must stop
// there; otherwise the argument's text is mistaken for more flags.
func shortOptionWrites(program, cluster string) bool {
scan:
	for i := 0; i < len(cluster); i++ {
		letter := cluster[i]
		switch program {
		case "gofmt", "goimports":
			// -w writes; -l and -d only report.
			if letter == 'w' {
				return true
			}
		case "sed":
			if letter == 'i' {
				return true
			}
			if letter == 'e' || letter == 'f' || letter == 'l' {
				// The rest of this word is an argument, not more flags.
				break scan
			}
		case "perl":
			if letter == 'i' {
				return true
			}
			// -w is warnings, not a write. Options that take the rest of the
			// word are scripts, modules, or include paths, not flag clusters.
			if letter == 'e' || letter == 'E' || letter == 'M' || letter == 'm' || letter == 'I' {
				break scan
			}
		}
	}
	return false
}

// hasMutatingSubcommand reports package-manager subcommands that write
// manifests, lockfiles, or installed trees into the project.
func hasMutatingSubcommand(program string, args []string) bool {
	sub := firstNonFlag(args)
	switch program {
	case "go":
		if sub == "get" || sub == "install" || sub == "work" {
			return true
		}
		// "go mod tidy"/"go mod edit" rewrite go.mod/go.sum; "go mod
		// download"/"verify" only populate the module cache.
		if sub == "mod" {
			modSub := firstNonFlagAfter(args, "mod")
			return modSub == "tidy" || modSub == "edit"
		}
		return false
	case "cargo":
		return sub == "add" || sub == "remove" || sub == "install" || sub == "fix"
	case "pip", "pip3", "poetry", "uv", "composer", "bundle", "gem":
		return sub == "install" || sub == "add" || sub == "remove" || sub == "update" || sub == "uninstall"
	case "npm", "pnpm", "yarn":
		switch sub {
		case "install", "i", "ci", "add", "remove", "rm", "uninstall", "update", "upgrade", "publish", "link":
			return true
		}
		return false
	}
	return false
}

// firstNonFlagAfter returns the first non-flag argument that follows sub.
func firstNonFlagAfter(args []string, sub string) string {
	for i, arg := range args {
		if arg != sub {
			continue
		}
		for _, rest := range args[i+1:] {
			if !strings.HasPrefix(rest, "-") {
				return rest
			}
		}
		return ""
	}
	return ""
}

// gitMutates reports whether a git invocation changes the worktree, the index,
// or the repository. Read subcommands (status, log, diff, show, blame, …) leave
// it untouched and stay exec.
//
// The subcommand is resolved past the global options that consume a value.
// `git -C repo commit` and `git -c core.pager=cat commit` change the repository
// exactly like `git commit`; treating the option's value as the subcommand made
// the runtime miss the change and report execution instead of mutation.
func gitMutates(args []string) bool {
	sub, rest := gitSubcommand(args)
	switch sub {
	case "commit", "add", "rm", "mv", "checkout", "switch", "restore", "reset",
		"clean", "merge", "rebase", "cherry-pick", "revert", "apply",
		"am", "init", "clone", "fetch", "pull", "push", "gc", "prune",
		"filter-branch", "submodule":
		return true
	case "tag":
		return tagMutates(rest)
	case "stash":
		// "git stash" with no subcommand pushes (mutating); list and show
		// only read.
		sub := firstNonFlag(rest)
		return sub != "list" && sub != "show"
	case "branch":
		return branchMutates(rest)
	case "config":
		return configMutates(rest)
	case "worktree":
		return subcommandIn(rest, "add", "remove", "prune", "move", "lock", "unlock")
	case "notes":
		return subcommandIn(rest, "add", "copy", "append", "edit", "merge", "prune", "remove")
	case "remote":
		return subcommandIn(rest, "add", "remove", "rm", "rename", "set-url", "set-head", "prune", "update")
	}
	return false
}

// gitGlobalValueOptions are the git options whose next word is a value, not the
// subcommand. An option spelled with an attached value (`--git-dir=.git`)
// already starts with a dash and needs no special case.
var gitGlobalValueOptions = map[string]bool{
	"-C": true, "-c": true, "--git-dir": true, "--work-tree": true,
	"--namespace": true, "--exec-path": true, "--super-prefix": true,
	"--config-env": true,
}

// gitSubcommand resolves the subcommand a git invocation runs and the arguments
// that follow it. An invocation with no subcommand returns "", nil.
//
// A shell wrapper or an assignment is already removed by programAndArgs, so the
// remaining words are git's own. Reading the first non-flag word directly was
// wrong for the value-taking options above: it returned the option's value, so
// a mutating subcommand appeared to be absent.
func gitSubcommand(args []string) (string, []string) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			if i+1 < len(args) {
				return args[i+1], args[i+2:]
			}
			return "", nil
		case gitGlobalValueOptions[arg]:
			i++
		case strings.HasPrefix(arg, "-"):
			// Another option. Options that take an attached value
			// (--git-dir=.git) are one word, so nothing else is skipped.
		default:
			return arg, args[i+1:]
		}
	}
	return "", nil
}

// subcommandIn reports whether the first positional argument is one of names.
func subcommandIn(args []string, names ...string) bool {
	first := firstNonFlag(args)
	for _, name := range names {
		if first == name {
			return true
		}
	}
	return false
}

// branchMutates reports whether `git branch` with args changes refs.
//
// Listing flags (and a pattern after --list) only read. A delete, move, or copy
// flag writes. So does any bare name, because `git branch feature` creates that
// branch: classifying it as a read made a real ref change invisible.
func branchMutates(args []string) bool {
	listing := false
	for _, arg := range args {
		switch {
		case arg == "-l", arg == "--list", arg == "-a", arg == "--all",
			arg == "-r", arg == "--remotes", arg == "-v", arg == "-vv",
			arg == "--show-current", arg == "-i", arg == "--ignore-case":
			listing = true
		case strings.HasPrefix(arg, "--contains"), strings.HasPrefix(arg, "--merged"),
			strings.HasPrefix(arg, "--no-merged"), strings.HasPrefix(arg, "--points-at"),
			strings.HasPrefix(arg, "--sort"), strings.HasPrefix(arg, "--format"),
			strings.HasPrefix(arg, "--column"):
			listing = true
		case strings.HasPrefix(arg, "-d"), strings.HasPrefix(arg, "-D"),
			strings.HasPrefix(arg, "-m"), strings.HasPrefix(arg, "-M"),
			strings.HasPrefix(arg, "-c"), strings.HasPrefix(arg, "-C"),
			arg == "-u", arg == "--set-upstream-to", arg == "--unset-upstream",
			arg == "--edit-description":
			return true
		}
	}
	if listing {
		return false
	}
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			return true
		}
	}
	return false
}

// configMutates reports whether `git config` with args writes configuration.
//
// Reading a key (`git config user.name`, `git config --get user.name`) and
// listing are reads. Two positional words are key and value, which writes. The
// values of the options that consume one (--file, --type, --default, …) are not
// positional words and must not be counted as a write.
func configMutates(args []string) bool {
	positional := 0
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--add", arg == "--unset", arg == "--unset-all",
			arg == "--replace-all", arg == "--rename-section", arg == "--remove-section",
			arg == "-e", arg == "--edit":
			return true
		case arg == "-f", arg == "--file", arg == "--blob", arg == "-t",
			arg == "--type", arg == "--default", arg == "--comment":
			i++
		case strings.HasPrefix(arg, "-"):
			// A flag, or an option with an attached value.
		default:
			positional++
		}
	}
	return positional >= 2
}

// tagMutates reports whether `git tag` with args changes the repository.
//
// Listing and verifying only read: `git tag`, `git tag -l`, `git tag -n`,
// `git tag -v <name>`, and the filtering and display selectors below. Deleting,
// forcing, annotating, signing, or messaging writes, and so does any positional
// tag name, which the command treats as a tag to create.
//
// This argument-aware form exists because classifying a plain listing as a
// mutation does not merely over-report: the runtime then demands a check for a
// change that never happened, so the task cannot leave the verification stage
// and a BUILD run deadlocks against an obligation only it can see.
func tagMutates(args []string) bool {
	for _, arg := range args {
		switch arg {
		case "-d", "--delete", "-f", "--force", "-a", "--annotate",
			"-s", "--sign", "-m", "--message", "-F", "--file", "-u", "--local-user":
			return true
		}
	}
	for _, arg := range args {
		switch {
		case arg == "-l", arg == "--list", arg == "-v", arg == "--verify",
			strings.HasPrefix(arg, "-n"),
			strings.HasPrefix(arg, "--contains"),
			strings.HasPrefix(arg, "--points-at"),
			strings.HasPrefix(arg, "--merged"),
			strings.HasPrefix(arg, "--no-merged"),
			strings.HasPrefix(arg, "--no-contains"),
			strings.HasPrefix(arg, "--sort"),
			strings.HasPrefix(arg, "--format"),
			strings.HasPrefix(arg, "--column"):
			// A selector cannot create or delete a tag, even when a pattern,
			// commit, or format string follows it.
			return false
		}
	}
	// With no selector and no flag, `git tag` lists. A positional argument is
	// the tag name to create.
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			return true
		}
	}
	return false
}

// GitObservation reports whether a finished shell command inspected
// version-control state without changing it. A VCS-state question is answered
// by that inspection, so the run can prove it looked rather than guessed.
//
// A mutating invocation is excluded: a commit or a push changes the repository
// and is a change obligation, not an observation of the current state.
func GitObservation(command string, exitCode int, root string) bool {
	if exitCode != 0 {
		return false
	}
	for _, segment := range shellSegments(command) {
		words := shellWords(segment)
		program, args := programAndArgs(words)
		if path.Base(program) != "git" || !gitRepositoryInRoot(args, root) {
			continue
		}
		if !gitMutates(args) {
			return true
		}
	}
	return false
}

// GitAbsenceNamespaces reports the VCS namespaces one finished shell command
// established empty for the workspace rooted at root.
//
// A complete enumeration (`git tag -l`, `git branch -a`, `git remote`) is
// evidence, not narration: an empty listing is the whole list, so an answer
// reporting no tags is a bounded observation rather than an unproven absence.
// The tool publishes it so the completion gate can tell the two apart without
// re-implementing shell grammar in the agent kernel.
//
// It is deliberately narrow. Only a successful, unpiped command whose git
// segment unambiguously enumerates a whole namespace counts: a selector, a
// pattern, a pipeline, or an invocation that names another repository
// establishes nothing about this workspace. An output that states the absence
// outright ("No commits yet") counts regardless of exit status, because that
// sentence is itself the observation.
func GitAbsenceNamespaces(command, output string, exitCode int, root string) []string {
	if strings.Contains(command, "`") || strings.Contains(command, "$(") ||
		strings.Contains(command, "||") || strings.Contains(command, "|") {
		return nil
	}
	lowerOutput := strings.ToLower(output)
	if strings.Contains(lowerOutput, "not a git repository") {
		return nil
	}
	// The observation must be about this workspace. A command that only names
	// another repository establishes nothing here, including the wording of its
	// output ("No commits yet").
	local := false
	for _, segment := range shellSegments(command) {
		words := shellWords(segment)
		program, args := programAndArgs(words)
		if path.Base(program) == "git" && gitRepositoryInRoot(args, root) {
			local = true
			break
		}
	}
	if !local {
		return nil
	}
	seen := map[string]bool{}
	var namespaces []string
	add := func(name string) {
		if !seen[name] {
			seen[name] = true
			namespaces = append(namespaces, name)
		}
	}
	if strings.Contains(lowerOutput, "no commits yet") ||
		strings.Contains(lowerOutput, "does not have any commits yet") {
		add("commit")
	}
	// A truncated listing is not the whole namespace, so it proves nothing.
	if exitCode != 0 || strings.Contains(output, "[output truncated") {
		return namespaces
	}
	for _, segment := range shellSegments(command) {
		words := shellWords(segment)
		program, args := programAndArgs(words)
		if path.Base(program) != "git" || !gitRepositoryInRoot(args, root) {
			continue
		}
		sub, rest := gitSubcommand(args)
		if namespace := completeGitEnumeration(sub, rest); namespace != "" {
			add(namespace)
		}
	}
	return namespaces
}

// completeGitEnumeration names the namespace one git invocation lists in full,
// or "" when the invocation selects, filters, verifies, or writes instead.
func completeGitEnumeration(sub string, args []string) string {
	switch sub {
	case "tag":
		for _, arg := range args {
			switch arg {
			case "-l", "--list", "-n", "--column", "-i", "--ignore-case":
				continue
			}
			if strings.HasPrefix(arg, "-n") || strings.HasPrefix(arg, "--sort") ||
				strings.HasPrefix(arg, "--format") || strings.HasPrefix(arg, "--column") {
				continue
			}
			// A positional word is a pattern, a verify target, or a tag to
			// create; none of them lists the whole namespace.
			return ""
		}
		return "tag"
	case "branch":
		for _, arg := range args {
			switch arg {
			case "-a", "--all", "-r", "--remotes", "-l", "--list", "-v", "-vv",
				"--show-current", "-i", "--ignore-case":
				continue
			}
			if strings.HasPrefix(arg, "--contains") || strings.HasPrefix(arg, "--merged") ||
				strings.HasPrefix(arg, "--no-merged") || strings.HasPrefix(arg, "--points-at") ||
				strings.HasPrefix(arg, "--sort") || strings.HasPrefix(arg, "--format") ||
				strings.HasPrefix(arg, "--column") {
				continue
			}
			return ""
		}
		return "branch"
	case "remote":
		for _, arg := range args {
			if arg != "-v" && arg != "--verbose" {
				return ""
			}
		}
		return "remote"
	case "stash":
		if firstNonFlag(args) == "list" {
			return "stash"
		}
	case "worktree":
		if firstNonFlag(args) == "list" {
			return "worktree"
		}
	case "notes":
		if firstNonFlag(args) == "" || firstNonFlag(args) == "list" {
			return "note"
		}
	}
	return ""
}

// gitRepositoryInRoot reports whether a git invocation whose repository the
// command names explicitly still names this workspace. An empty root means the
// caller did not bound the observation, so it is accepted.
func gitRepositoryInRoot(args []string, root string) bool {
	root = strings.TrimSpace(root)
	if root == "" {
		return true
	}
	root = resolveCommandPath(root)
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "-C":
			if i+1 >= len(args) {
				return false
			}
			target, ok := resolveShellPath(args[i+1], root)
			if !ok || !pathWithinRoot(root, target) {
				return false
			}
			i++
		case args[i] == "--git-dir", args[i] == "--work-tree",
			strings.HasPrefix(args[i], "--git-dir="), strings.HasPrefix(args[i], "--work-tree="):
			// The repository or worktree is named explicitly and is not resolved
			// here, so the observation cannot be attributed to this workspace.
			return false
		}
	}
	return true
}

// firstNonFlag returns the first argument that is not a flag.
func firstNonFlag(args []string) string {
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			return arg
		}
	}
	return ""
}

// hasOutputRedirectInRoot reports whether the command redirects a stream into a
// real file. Redirects to a character device (/dev/null, /dev/stdout, …) and
// file-descriptor duplications (`>&2`, `2>&1`) do not mutate the repository
// and are ignored.
//
// With a root, an absolute target outside that root is also ignored: it writes
// outside the repository, which is execution rather than a source change.
func hasOutputRedirectInRoot(command, root string) bool {
	quoted := false
	var quote rune
	runes := []rune(command)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if quoted {
			if r == quote {
				quoted = false
			}
			continue
		}
		if r == '\'' || r == '"' {
			quoted = true
			quote = r
			continue
		}
		if r != '>' {
			continue
		}
		// Skip the second '>' of a ">>" append, and any fd duplication.
		j := i + 1
		if j < len(runes) && runes[j] == '>' {
			j++
		}
		// ">&N" and "N>&M" duplicate a descriptor, they do not open a file.
		if j < len(runes) && runes[j] == '&' {
			i = j
			continue
		}
		for j < len(runes) && (runes[j] == ' ' || runes[j] == '\t') {
			j++
		}
		target := readWord(runes, j)
		if target == "" || isCharDevice(target) {
			i = j
			continue
		}
		if redirectWritesOutsideRoot(target, root) {
			i = j
			continue
		}
		return true
	}
	return false
}

// redirectWritesOutsideRoot reports whether the redirect target is an absolute
// path that resolves outside root. A relative target, a shell expansion, or an
// unresolved path is not proof of an outside write, so the caller treats it as
// a workspace write.
func redirectWritesOutsideRoot(target, root string) bool {
	root = strings.TrimSpace(root)
	target = strings.TrimSpace(target)
	if root == "" || target == "" {
		return false
	}
	if target == "~" || strings.HasPrefix(target, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return false
		}
		target = filepath.Join(home, strings.TrimPrefix(target, "~"))
	}
	if !filepath.IsAbs(target) {
		return false
	}
	root = resolveCommandPath(root)
	target = resolveCommandPath(target)
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// resolveCommandPath resolves a path to the spelling the OS compares, including
// symlinks on the nearest existing ancestor. A target file usually does not
// exist yet, so the unresolved suffix is reattached after the ancestor is
// resolved.
func resolveCommandPath(path string) string {
	absolute, err := filepath.Abs(path)
	if err != nil {
		absolute = filepath.Clean(path)
	}
	dir := absolute
	var suffix []string
	for {
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return resolved
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return filepath.Clean(absolute)
		}
		suffix = append(suffix, filepath.Base(dir))
		dir = parent
	}
}

// readWord reads one shell word starting at index j.
func readWord(runes []rune, j int) string {
	var b strings.Builder
	quoted := false
	var quote rune
	for ; j < len(runes); j++ {
		r := runes[j]
		if quoted {
			if r == quote {
				quoted = false
				continue
			}
			b.WriteRune(r)
			continue
		}
		if r == '\'' || r == '"' {
			quoted = true
			quote = r
			continue
		}
		if r == ' ' || r == '\t' || r == '\n' || r == ';' || r == '&' || r == '|' {
			break
		}
		b.WriteRune(r)
	}
	return b.String()
}

// isCharDevice reports whether a redirect target is a device file, which is
// not a repository mutation.
func isCharDevice(target string) bool {
	switch target {
	case "/dev/null", "/dev/stdout", "/dev/stderr", "/dev/tty", "/dev/zero", "/dev/fd/1", "/dev/fd/2":
		return true
	}
	return strings.HasPrefix(target, "/dev/fd/")
}

// shellSegments splits a command on unquoted statement and pipeline
// separators, so each program in a compound command is classified.
func shellSegments(command string) []string {
	var (
		segments []string
		current  strings.Builder
		quoted   bool
		quote    rune
	)
	flush := func() {
		segments = append(segments, current.String())
		current.Reset()
	}
	for _, r := range command {
		if quoted {
			current.WriteRune(r)
			if r == quote {
				quoted = false
			}
			continue
		}
		switch r {
		case '\'', '"':
			quoted = true
			quote = r
			current.WriteRune(r)
		case ';', '&', '|', '\n', '(', ')':
			flush()
		default:
			current.WriteRune(r)
		}
	}
	flush()
	return segments
}

// shellWords splits a segment into words, dropping simple assignment prefixes
// and wrapper programs so the real command is classified.
func shellWords(segment string) []string {
	var (
		words   []string
		current strings.Builder
		quoted  bool
		quote   rune
	)
	flush := func() {
		if current.Len() > 0 {
			words = append(words, current.String())
			current.Reset()
		}
	}
	for _, r := range segment {
		if quoted {
			if r == quote {
				quoted = false
				continue
			}
			current.WriteRune(r)
			continue
		}
		switch r {
		case '\'', '"':
			quoted = true
			quote = r
		case ' ', '\t':
			flush()
		default:
			current.WriteRune(r)
		}
	}
	flush()
	return words
}

// programAndArgs finds the program a segment runs, skipping environment
// assignments and the wrapper programs that only prefix another command.
func programAndArgs(words []string) (string, []string) {
	i := 0
	for i < len(words) {
		word := words[i]
		if strings.Contains(word, "=") && !strings.HasPrefix(word, "=") &&
			!strings.HasPrefix(word, "-") && isAssignment(word) {
			i++
			continue
		}
		break
	}
	for i < len(words) {
		switch path.Base(words[i]) {
		case "sudo", "command", "env", "nice", "ionice", "time", "nohup", "exec", "stdbuf":
			i++
			// Skip a wrapper's own flags and assignments.
			for i < len(words) && (strings.HasPrefix(words[i], "-") || isAssignment(words[i])) {
				i++
			}
			continue
		}
		break
	}
	if i >= len(words) {
		return "", nil
	}
	return words[i], words[i+1:]
}

// isAssignment reports whether a word is a NAME=value environment assignment.
func isAssignment(word string) bool {
	name, _, ok := strings.Cut(word, "=")
	if !ok || name == "" {
		return false
	}
	for i, r := range name {
		if r == '_' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
			continue
		}
		if i > 0 && r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}
