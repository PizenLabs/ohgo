package tools

import (
	"path"
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
	if hasOutputRedirect(command) {
		return true
	}
	for _, segment := range shellSegments(command) {
		words := shellWords(segment)
		program, args := programAndArgs(words)
		if program == "" {
			continue
		}
		if mutatingProgram(path.Base(program), args) {
			return true
		}
	}
	return false
}

// mutatingProgram reports whether a program mutates when run with args.
func mutatingProgram(program string, args []string) bool {
	switch program {
	case "rm", "rmdir", "mv", "cp", "install", "touch", "truncate", "dd",
		"mkdir", "chmod", "chown", "chgrp", "ln", "shred", "rsync",
		"unlink", "mknod", "setfacl":
		return true
	case "tee":
		// tee writes to its targets, but a target that is a character device
		// (/dev/null) is not a repository mutation.
		return writesToFile(args)
	case "sed", "perl", "gofmt", "goimports":
		return hasInPlaceFlag(args)
	case "npm", "pnpm", "yarn", "pip", "pip3", "cargo", "go", "composer",
		"bundle", "gem", "poetry", "uv":
		return hasMutatingSubcommand(program, args)
	case "git":
		return gitMutates(args)
	}
	return false
}

// writesToFile reports whether any non-flag argument names a real file, as
// opposed to a character device.
func writesToFile(args []string) bool {
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			continue
		}
		if isCharDevice(arg) {
			continue
		}
		return true
	}
	return false
}

// hasInPlaceFlag reports an in-place edit flag (-i, -w), which turns a
// formatting/editing program into a writer.
func hasInPlaceFlag(args []string) bool {
	for _, arg := range args {
		trimmed := strings.TrimLeft(arg, "-")
		for _, letter := range trimmed {
			if letter == 'i' || letter == 'w' {
				return true
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

// gitMutates reports whether a git subcommand changes the worktree, the index,
// or the repository. Read subcommands (status, log, diff, show, blame, …) leave
// it untouched and stay exec.
func gitMutates(args []string) bool {
	sub := firstNonFlag(args)
	switch sub {
	case "commit", "add", "rm", "mv", "checkout", "switch", "restore", "reset",
		"clean", "merge", "rebase", "cherry-pick", "revert", "apply",
		"am", "init", "clone", "fetch", "pull", "push", "tag", "gc", "prune",
		"filter-branch", "submodule":
		return true
	case "stash":
		// "git stash" with no subcommand pushes (mutating); list and show
		// only read.
		sub := firstNonFlagAfter(args, "stash")
		return sub != "list" && sub != "show"
	case "branch":
		// "git branch" lists; "git branch -d/-m/-D/-M/-c/C" mutates refs.
		for _, arg := range args {
			if strings.HasPrefix(arg, "-d") || strings.HasPrefix(arg, "-D") ||
				strings.HasPrefix(arg, "-m") || strings.HasPrefix(arg, "-M") ||
				strings.HasPrefix(arg, "-c") || strings.HasPrefix(arg, "-C") {
				return true
			}
		}
		return false
	case "config":
		// Reading config is exec; writing it is mutation.
		for _, arg := range args {
			if arg == "--add" || arg == "--unset" || arg == "--replace-all" {
				return true
			}
		}
		// "git config key value" writes when a value is present.
		return len(nonFlagArgsAfter(args, "config")) >= 2
	case "worktree", "notes":
		for _, arg := range args {
			switch arg {
			case "add", "remove", "prune", "move", "lock", "unlock":
				return true
			}
		}
		return false
	}
	return false
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

// nonFlagArgsAfter counts non-flag arguments after the leading subcommand.
func nonFlagArgsAfter(args []string, sub string) []string {
	var out []string
	seen := false
	for _, arg := range args {
		if !seen {
			if arg == sub {
				seen = true
			}
			continue
		}
		if !strings.HasPrefix(arg, "-") {
			out = append(out, arg)
		}
	}
	return out
}

// hasOutputRedirect reports whether the command redirects a stream into a real
// file. Redirects to a character device (/dev/null, /dev/stdout, …) and
// file-descriptor duplications (`>&2`, `2>&1`) do not mutate the repository
// and are ignored.
func hasOutputRedirect(command string) bool {
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
		return true
	}
	return false
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
