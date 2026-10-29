package shell

import (
	"strings"
)

// MinMinimizeChars is the smallest output the minimizer will rewrite.
//
// Below this, a filter's fixed overhead costs more than it saves and the
// original text is short enough to read directly.
const MinMinimizeChars = 1_000

// DefaultMaxCaptureBytes bounds the input the minimizer will consider.
const DefaultMaxCaptureBytes = 4 * 1024 * 1024

// Names of the known filters.
const (
	FilterPassthrough = "passthrough"
	FilterDisabled    = "disabled"
	FilterTooShort    = "too-short"
	FilterTooLarge    = "too-large"
	FilterPiped       = "piped"
	FilterCompound    = "compound"
	FilterParseError  = "parse-error"
	FilterUnknown     = "unknown"
	FilterExitSkip    = "exit-skip"
	FilterUnsupported = "unsupported"
	FilterGit         = "git"
)

// Identity is a command resolved to the program and subcommand that ran.
type Identity struct {
	// Program is the lowercased program name with any path stripped.
	Program string
	// Subcommand is the lowercased subcommand, or "" when there is none.
	Subcommand string
	// Command is the raw command string as supplied.
	Command string
}

// Recognised reports whether the identity names a known filter.
func (i Identity) Recognised() bool {
	return i.Program != ""
}

// Output is the result of minimizing a capture.
type Output struct {
	// Text is the rewritten output, or the original on a passthrough.
	Text string
	// Changed reports whether the text was rewritten at all.
	Changed bool
	// InputBytes is the size of the captured buffer before minimization.
	InputBytes int
	// OutputBytes is the size of Text after minimization.
	OutputBytes int
	// Filter labels the dispatch path, including the reason for a miss.
	Filter string
	// Original is the un-minimized capture, set only when Changed is true, so
	// a caller can persist it and splice an artifact reference into Text.
	Original string
}

// BytesSaved reports how much the filter removed, or 0 on a passthrough.
func (o Output) BytesSaved() int {
	if !o.Changed || o.OutputBytes >= o.InputBytes {
		return 0
	}
	return o.InputBytes - o.OutputBytes
}

// Passthrough returns the input unchanged, labelled with a reason.
func Passthrough(text, label string) Output {
	return Output{
		Text:        text,
		InputBytes:  len(text),
		OutputBytes: len(text),
		Filter:      label,
	}
}

// Transformed returns rewritten text.
func Transformed(text string, inputBytes int, filter string) Output {
	return Output{
		Text:        text,
		Changed:     true,
		InputBytes:  inputBytes,
		OutputBytes: len(text),
		Filter:      filter,
	}
}

// WithOriginal attaches the un-minimized capture.
//
// It is a no-op on a passthrough: there is nothing to save a reference to when
// the text is already unchanged.
func (o Output) WithOriginal(original string) Output {
	if o.Changed {
		o.Original = original
	}
	return o
}

// Config configures the minimizer.
//
// The zero value has minimization off, which is deliberate: a caller must opt
// in rather than have tool output rewritten by surprise.
type Config struct {
	// Enabled turns minimization on.
	Enabled bool
	// MaxCaptureBytes bounds the input considered. Zero means DefaultMaxCaptureBytes.
	MaxCaptureBytes int
	// Only restricts minimization to these programs when non-empty. An empty
	// list enables every program.
	Only []string
	// Skip skips these programs.
	Skip []string
}

// DefaultConfig returns the built-in configuration.
func DefaultConfig() Config {
	return Config{
		Enabled:         false,
		MaxCaptureBytes: DefaultMaxCaptureBytes,
	}
}

// MaxBytes returns the configured cap, substituting the default for zero.
func (c Config) MaxBytes() int {
	if c.MaxCaptureBytes <= 0 {
		return DefaultMaxCaptureBytes
	}
	return c.MaxCaptureBytes
}

// AllowsProgram reports whether a program may be minimized.
//
// A program on both lists is skipped: an explicit exclusion is the stronger
// statement, and silently honouring Only would ignore it.
func (c Config) AllowsProgram(program string) bool {
	if !c.Enabled {
		return false
	}
	for _, name := range c.Skip {
		if strings.EqualFold(name, program) {
			return false
		}
	}
	if len(c.Only) == 0 {
		return true
	}
	for _, name := range c.Only {
		if strings.EqualFold(name, program) {
			return true
		}
	}
	return false
}

// Detect resolves a command string to its program and subcommand.
//
// Launch prefixes (environment assignments, env, sudo, command, exec, time)
// are stripped first, so `FOO=1 sudo git status` is recognised as `git status`
// rather than as an unknown program.
func Detect(command string) Identity {
	return DetectTokens(Tokenize(command), command)
}

// DetectTokens resolves an already-tokenized command.
func DetectTokens(tokens []string, command string) Identity {
	stripped := stripLaunchPrefix(tokens)
	if len(stripped) == 0 {
		return Identity{Command: command}
	}

	program := normalizedProgram(stripped[0])
	if program == "" {
		return Identity{Command: command}
	}

	identity := Identity{Program: program, Command: command}
	rest := stripped[1:]

	// docker-compose v1 takes global flags before the action, so the real
	// subcommand is the first non-flag argument.
	if strings.EqualFold(program, "docker") && strings.EqualFold(stripped[0], "docker-compose") {
		identity.Subcommand = firstNonGlobalArg(rest, []string{
			"-f", "--file", "--profile", "-p", "--project-name", "--env-file",
			"--parallel", "--progress", "--project-directory", "--workdir", "-w", "--ansi",
		}, nil, nil)
		return identity
	}

	identity.Subcommand = detectSubcommand(program, rest)
	return identity
}

// globalFlagSet is one program's table of flags around its subcommand.
type globalFlagSet struct {
	// withValue are flags that consume the following token.
	withValue []string
	// flagOnly are flags that stand alone.
	flagOnly []string
	// prefixes are prefixes that mark a bare argument, e.g. cargo's "+toolchain".
	prefixes []string
}

// subcommandFlags is the per-program flag table, ported from the original.
//
// It exists because `git -C /tmp status` has `status` as its subcommand, not
// `/tmp`: without the table the first non-flag argument is taken literally and
// the wrong filter is selected.
var subcommandFlags = map[string]globalFlagSet{
	"git": {
		withValue: []string{"-C", "-c", "--git-dir", "--work-tree", "--namespace", "--exec-path", "--html-path"},
		flagOnly: []string{
			"--bare", "--no-pager", "--paginate", "--no-optional-locks",
			"--literal-pathspecs", "--glob-pathspecs", "--noglob-pathspecs",
			"--icase-pathspecs", "--no-replace-objects",
		},
	},
	"yadm": {
		withValue: []string{"-C", "-c", "--git-dir", "--work-tree", "--namespace", "--exec-path", "--html-path"},
		flagOnly: []string{
			"--bare", "--no-pager", "--paginate", "--no-optional-locks",
			"--literal-pathspecs", "--glob-pathspecs", "--noglob-pathspecs",
			"--icase-pathspecs", "--no-replace-objects",
		},
	},
	"cargo": {
		withValue: []string{"-C", "--manifest-path", "--target-dir", "--config", "-Z", "--color", "--jobs", "-j"},
		flagOnly:  []string{"--locked", "--offline", "--frozen", "--workspace", "--all", "--verbose", "-v", "--quiet", "-q"},
		prefixes:  []string{"+"},
	},
	"docker": {
		withValue: []string{
			"--config", "--context", "-c", "--host", "-H", "--log-level",
			"--tlscacert", "--tlscert", "--tlskey",
		},
		flagOnly: []string{"--debug", "-D", "--tls", "--tlsverify"},
	},
}

// detectSubcommand finds the subcommand for a known program.
//
// A program with no registered table falls back to "first non-flag argument",
// which is right for the common case and only wrong for a program whose global
// flags take values.
func detectSubcommand(program string, args []string) string {
	table, known := subcommandFlags[program]
	if !known {
		return firstNonGlobalArg(args, nil, nil, nil)
	}
	return firstNonGlobalArg(args, table.withValue, table.flagOnly, table.prefixes)
}

// normalizedProgram strips any directory and normalises launcher names.
func normalizedProgram(program string) string {
	name := program
	if idx := strings.LastIndexAny(name, "/\\"); idx >= 0 {
		name = name[idx+1:]
	}
	if name == "" {
		return ""
	}
	lowered := strings.ToLower(name)
	switch lowered {
	case "gradlew.bat":
		return "gradlew"
	case "mvnw.cmd":
		return "mvnw"
	case "docker-compose":
		// The compose v1 alias routes through the docker filter, which knows
		// both spellings.
		return "docker"
	}
	return lowered
}

// stripLaunchPrefix skips wrappers that sit in front of the real program.
func stripLaunchPrefix(tokens []string) []string {
	index := 0
	for {
		before := index
		for index < len(tokens) && isEnvAssignment(tokens[index]) {
			index++
		}
		if index >= len(tokens) {
			return nil
		}

		switch tokens[index] {
		case "env":
			index = skipEnvOptions(tokens, index+1)
		case "sudo":
			index = skipOptionValues(tokens, index+1, []string{
				"-u", "--user", "-g", "--group", "-h", "--host", "-p", "--prompt",
				"-C", "--close-from", "-r", "--role", "-t", "--type",
			})
		case "command":
			// -v and -V ask for a pathname, not a command to run, so there is
			// no program to identify. That is a parse failure rather than a
			// guess, because parsing one would misidentify the command.
			next := skipCommandOptions(tokens, index+1)
			if next < 0 {
				return nil
			}
			index = next
		case "builtin", "noglob":
			index++
		case "exec":
			index = skipOptionValues(tokens, index+1, []string{"-a", "--name", "-l"})
		case "time":
			index = skipOptionValues(tokens, index+1, []string{"-f", "--format", "-o", "--output"})
		}
		if index == before || index < 0 {
			break
		}
	}
	if index >= len(tokens) {
		return nil
	}
	return tokens[index:]
}

// skipCommandOptions skips the `command` builtin's own flags.
//
// It returns -1 for "-v" / "-V" or any unrecognised flag, because those forms
// do not name a program to run.
func skipCommandOptions(tokens []string, index int) int {
	for index < len(tokens) {
		switch {
		case tokens[index] == "--":
			return index + 1
		case tokens[index] == "-p":
			index += 2
		case tokens[index] == "-v", tokens[index] == "-V":
			return -1
		case strings.HasPrefix(tokens[index], "-"):
			return -1
		default:
			return index
		}
	}
	return -1
}

// skipEnvOptions skips env's own flags, stopping at the program.
func skipEnvOptions(tokens []string, index int) int {
	for index < len(tokens) {
		switch {
		case tokens[index] == "--":
			return index + 1
		case tokens[index] == "-S", tokens[index] == "--split-string":
			// -S takes a command string that this tokenizer cannot split.
			return -1
		case tokens[index] == "-i", tokens[index] == "-", tokens[index] == "--ignore-environment":
			index++
		case tokens[index] == "-u", tokens[index] == "--unset",
			tokens[index] == "-C", tokens[index] == "--chdir":
			index = skipOptionValues(tokens, index, nil)
		case strings.HasPrefix(tokens[index], "--unset="),
			strings.HasPrefix(tokens[index], "--chdir="):
			index++
		case isEnvAssignment(tokens[index]):
			index++
		default:
			return index
		}
	}
	return -1
}

// skipOptionValues advances past `token value` pairs for the named flags.
//
// A nil names list skips only the token itself, for flags that take a value
// in a form this tokenizer treats as separate.
func skipOptionValues(tokens []string, index int, names []string) int {
	for index < len(tokens) {
		token := tokens[index]
		if !takesValue(token, names) {
			return index
		}
		// A "--flag=value" form carries its value inline.
		if strings.Contains(token, "=") {
			index++
			continue
		}
		index += 2
	}
	return index
}

// takesValue reports whether a token is a flag that consumes the next token.
func takesValue(token string, names []string) bool {
	for _, name := range names {
		if token == name {
			return true
		}
	}
	return false
}

// isEnvAssignment reports whether a token looks like NAME=value.
func isEnvAssignment(token string) bool {
	name, _, ok := strings.Cut(token, "=")
	if !ok || name == "" {
		return false
	}
	first := name[0]
	if first != '_' && !isAlpha(first) {
		return false
	}
	for i := 1; i < len(name); i++ {
		c := name[i]
		if c != '_' && !isAlpha(c) && !isDigit(c) {
			return false
		}
	}
	return true
}

func isAlpha(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// firstNonGlobalArg returns the first argument that is neither a global flag
// nor its value.
//
// `--` terminates the flag scan and the very next token is the subcommand, as
// in `git -- status`.
func firstNonGlobalArg(args []string, withValue, flagOnly, prefixes []string) string {
	for i := 0; i < len(args); i++ {
		arg := args[i]

		if arg == "--" {
			if i+1 < len(args) {
				return strings.ToLower(args[i+1])
			}
			return ""
		}
		if matchesAnyPrefix(arg, prefixes) {
			continue
		}
		if containsString(flagOnly, arg) {
			continue
		}
		if takesValue(arg, withValue) {
			// A "--flag=value" form carries its value inline and consumes
			// nothing extra.
			if !strings.Contains(arg, "=") {
				i++
			}
			continue
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		return strings.ToLower(arg)
	}
	return ""
}

// matchesAnyPrefix reports whether arg starts with any of the prefixes.
func matchesAnyPrefix(arg string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(arg, prefix) {
			return true
		}
	}
	return false
}

// containsString reports whether list holds value.
func containsString(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

// Tokenize splits a command line on whitespace, honouring quotes.
//
// It is deliberately simple: full shell parsing is out of scope, and the
// minimizer only needs the program and subcommand, which never contain quoting.
func Tokenize(command string) []string {
	var (
		tokens []string
		cur    strings.Builder
		quote  rune
		has    bool
	)
	for _, r := range command {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
				continue
			}
			cur.WriteRune(r)
		case r == '\'' || r == '"':
			quote = r
			has = true
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			if has || cur.Len() > 0 {
				tokens = append(tokens, cur.String())
				cur.Reset()
				has = false
			}
		default:
			cur.WriteRune(r)
		}
	}
	if has || cur.Len() > 0 {
		tokens = append(tokens, cur.String())
	}
	return tokens
}

// Apply runs the minimizer over a captured buffer.
//
// Every miss is labelled with the reason rather than silently passing through:
// telemetry that cannot explain itself is telemetry nobody trusts when the
// numbers look wrong.
func Apply(command string, captured string, exitCode int, config Config) Output {
	if !config.Enabled {
		return Passthrough(captured, FilterDisabled)
	}
	// A short capture is cheaper to read than to rewrite.
	if runeCount(captured) < MinMinimizeChars {
		return Passthrough(captured, FilterTooShort)
	}
	// A huge capture is not worth the memory of rewriting.
	if len(captured) > config.MaxBytes() {
		return Passthrough(captured, FilterTooLarge)
	}

	identity := Detect(command)
	if identity.Program == "" {
		return Passthrough(captured, FilterParseError)
	}
	if !config.AllowsProgram(identity.Program) {
		return Passthrough(captured, FilterUnsupported)
	}

	// A failing command's output is diagnostic; rewriting it would hide the
	// error the user needs to see.
	if exitCode != 0 && identity.Program != "cargo" {
		return Passthrough(captured, FilterExitSkip)
	}

	filter := filterFor(identity)
	if filter == nil {
		return Passthrough(captured, FilterUnknown)
	}

	out := filter(captured, identity)
	if out.Changed {
		return out.WithOriginal(captured)
	}
	return out
}

// filterFor returns the filter for an identity, or nil when unrecognised.
//
// Only the high-value filters are registered; the rest fall through to
// FilterUnknown rather than being silently dropped.
func filterFor(identity Identity) func(string, Identity) Output {
	switch identity.Program {
	case "git":
		return filterGit
	}
	return nil
}

// runeCount counts runes rather than bytes, so a multi-byte output is not
// mistaken for a long one and minimized unnecessarily.
func runeCount(text string) int {
	count := 0
	for range text {
		count++
	}
	return count
}

// filterGit collapses git's own verbose output.
//
// It is the highest-value single filter: `git status` and `git diff` produce
// hundreds of lines of which a handful matter.
func filterGit(captured string, identity Identity) Output {
	switch identity.Subcommand {
	case "status", "log", "diff", "branch":
		return collapseBlankRuns(captured, FilterGit)
	}
	return Passthrough(captured, FilterUnsupported)
}

// collapseBlankRuns squeezes runs of blank lines.
//
// It is a conservative first filter: it never removes content, only the
// whitespace that separates it, so it is safe to run on output whose schema is
// not yet understood.
func collapseBlankRuns(text, filter string) Output {
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	blank := false
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			if blank {
				continue
			}
			blank = true
			out = append(out, "")
			continue
		}
		blank = false
		out = append(out, line)
	}
	result := strings.Join(out, "\n")
	if result == text {
		return Passthrough(text, filter)
	}
	return Transformed(result, len(text), filter)
}
