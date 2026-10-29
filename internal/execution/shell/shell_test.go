package shell

import (
	"strings"
	"testing"
)

func TestPushReturnsValidUTF8Unchanged(t *testing.T) {
	// The fast path must not allocate or copy for the common case.
	d := New()
	if got := d.Push([]byte("hello")); got != "hello" {
		t.Errorf("got %q, want the input unchanged", got)
	}
	if d.Pending() != 0 {
		t.Errorf("pending = %d, want 0", d.Pending())
	}
}

func TestPushHoldsASplitMultiByteSequence(t *testing.T) {
	// A pipe can split a character across reads. Emitting each read
	// independently would turn that character into a replacement character.
	// "日" is E6 97 A5.
	d := New()

	first := d.Push([]byte("a\xe6\x97"))
	if first != "a" {
		t.Errorf("first push = %q, want only the complete prefix", first)
	}
	if d.Pending() == 0 {
		t.Fatal("the incomplete sequence was not buffered")
	}

	second := d.Push([]byte("\xa5b"))
	if second != "日b" {
		t.Errorf("second push = %q, want the completed character plus b", second)
	}
	if d.Pending() != 0 {
		t.Errorf("pending = %d, want 0 once the sequence completed", d.Pending())
	}
}

func TestPushSplitsOnEveryBoundaryOfAFourByteCharacter(t *testing.T) {
	// "𐐷" is F0 90 90 B7 — the longest form UTF-8 defines, and the one most
	// likely to be split by a pipe read.
	full := []byte("x𐐷y")
	for i := 1; i < len(full); i++ {
		dec := New()
		var out string
		out += dec.Push(full[:i])
		out += dec.Push(full[i:])
		out += dec.Finish()
		if out != "x𐐷y" {
			t.Errorf("split at %d produced %q, want x𐐷y", i, out)
		}
	}
}

func TestFinishFlushesAnIncompleteSequenceAsReplacement(t *testing.T) {
	// At EOF an incomplete sequence can never complete, so it becomes a
	// replacement character rather than being dropped: silently losing a byte
	// would make the output shorter than the bytes the process wrote.
	d := New()
	got := d.Push([]byte("ok\xe6\x97")) + d.Finish()
	if got != "ok"+Replacement {
		t.Errorf("got %q, want %q", got, "ok"+Replacement)
	}
}

func TestPushReplacesAnInvalidLeadByte(t *testing.T) {
	// 0xFF cannot begin a UTF-8 sequence at all, so it is not held pending —
	// it is replaced immediately and decoding continues.
	d := New()
	got := d.Push([]byte("a\xffb"))
	if got != "a"+Replacement+"b" {
		t.Errorf("got %q, want %q", got, "a"+Replacement+"b")
	}
}

func TestPushHandlesManyInvalidSequencesLinearly(t *testing.T) {
	// The original drained the buffer per invalid sequence, which was
	// quadratic. A large buffer of junk must not blow up.
	junk := strings.Repeat("\xff", 400_000)
	d := New()
	got := d.Push([]byte(junk))
	if len(got) != len(junk)*len(Replacement) {
		t.Errorf("produced %d bytes, want one replacement per input byte", len(got))
	}
}

func TestPushReturnsEmptyForAnEmptyRead(t *testing.T) {
	d := New()
	if got := d.Push(nil); got != "" {
		t.Errorf("got %q, want empty", got)
	}
	if got := d.Finish(); got != "" {
		t.Errorf("finish on an empty decoder = %q, want empty", got)
	}
}

func TestDecodeAccumulatesAcrossManyReads(t *testing.T) {
	// A long UTF-8 string streamed in odd chunks must reassemble exactly.
	source := strings.Repeat("日本語テキスト", 500)
	d := New()
	var out strings.Builder
	for i := 0; i < len(source); i += 7 {
		end := i + 7
		if end > len(source) {
			end = len(source)
		}
		out.WriteString(d.Push([]byte(source[i:end])))
	}
	out.WriteString(d.Finish())
	if out.String() != source {
		t.Fatal("chunked decoding did not reassemble the original")
	}
}

func TestDetectReadsTheProgramAndSubcommand(t *testing.T) {
	cases := []struct {
		command    string
		program    string
		subcommand string
	}{
		{"git status", "git", "status"},
		{"GIT STATUS", "git", "status"},
		{"/usr/bin/git log", "git", "log"},
		{"git", "git", ""},
		{"go test ./...", "go", "test"},
		// -C consumes a value, so the subcommand is status, not /tmp.
		{"git -C /tmp status", "git", "status"},
		{"git --git-dir=/x status", "git", "status"},
	}
	for _, tc := range cases {
		t.Run(tc.command, func(t *testing.T) {
			got := Detect(tc.command)
			if got.Program != tc.program {
				t.Errorf("program = %q, want %q", got.Program, tc.program)
			}
			if tc.subcommand != "" && got.Subcommand != tc.subcommand {
				t.Errorf("subcommand = %q, want %q", got.Subcommand, tc.subcommand)
			}
		})
	}
}

func TestDetectStripsLaunchPrefixes(t *testing.T) {
	// The minimizer must recognise the program that actually ran, so a wrapper
	// must not hide it.
	cases := []struct {
		command string
		program string
	}{
		{"FOO=1 sudo git status", "git"},
		{"env NODE_ENV=test npm ci", "npm"},
		// `command -v` asks for a pathname, not a command to run, so the
		// original treats it as a parse failure rather than a program lookup.
		{"command -v git", ""},
		{"exec -a myname cargo build", "cargo"},
		{"time go test", "go"},
		{"noglob ls", "ls"},
	}
	for _, tc := range cases {
		t.Run(tc.command, func(t *testing.T) {
			if got := Detect(tc.command); got.Program != tc.program {
				t.Errorf("program = %q, want %q", got.Program, tc.program)
			}
		})
	}
}

func TestDetectFailsOnAPathnameQuery(t *testing.T) {
	// `command -v git` names no program to run, so no identity must be
	// resolved: picking one would minimize the wrong command's output.
	got := Detect("command -v git")
	if got.Program != "" {
		t.Errorf("program = %q, want none for a pathname query", got.Program)
	}
}

func TestDetectNormalisesLauncherNames(t *testing.T) {
	// Windows twins and the compose v1 alias must dispatch to the same filter.
	if got := Detect("gradlew.bat build").Program; got != "gradlew" {
		t.Errorf("gradlew.bat -> %q, want gradlew", got)
	}
	if got := Detect("mvnw.cmd package").Program; got != "mvnw" {
		t.Errorf("mvnw.cmd -> %q, want mvnw", got)
	}
	if got := Detect("docker-compose up").Program; got != "docker" {
		t.Errorf("docker-compose -> %q, want docker", got)
	}
}

func TestDetectSkipsDockerComposeGlobalFlags(t *testing.T) {
	// docker-compose v1 takes global flags before the action, so the
	// subcommand must be the first non-flag argument after them.
	got := Detect("docker-compose -f docker-compose.yml up")
	if got.Subcommand != "up" {
		t.Errorf("subcommand = %q, want up", got.Subcommand)
	}
}

func TestDetectHandlesEnvAssignments(t *testing.T) {
	for _, token := range []string{"FOO=1", "_BAR=2", "A=b", "lower=x"} {
		if !isEnvAssignment(token) {
			t.Errorf("%q not recognised as an assignment", token)
		}
	}
	for _, token := range []string{"FOO", "1FOO=x", "=x", "FO-BAR=x", ""} {
		if isEnvAssignment(token) {
			t.Errorf("%q wrongly recognised as an assignment", token)
		}
	}
}

func TestApplyGatesOnEnabled(t *testing.T) {
	// Minimization is opt-in: tool output must not be rewritten by surprise.
	long := strings.Repeat("x", MinMinimizeChars+100)
	out := Apply("git status", long, 0, Config{})
	if out.Changed || out.Filter != FilterDisabled {
		t.Errorf("out = %+v, want a disabled passthrough", out)
	}
	if out.Text != long {
		t.Error("a disabled minimizer altered the text")
	}
}

func TestApplyGatesOnLength(t *testing.T) {
	cfg := Config{Enabled: true}

	short := strings.Repeat("x", MinMinimizeChars-1)
	if got := Apply("git status", short, 0, cfg); !got.Changed && got.Filter != FilterTooShort {
		t.Errorf("short output = %+v, want a too-short passthrough", got)
	}

	// The threshold is rune-based: a multi-byte string of the same byte length
	// is genuinely longer and must still be minimized.
	multi := strings.Repeat("日", MinMinimizeChars)
	if got := Apply("git status", multi, 0, cfg); got.Filter == FilterTooShort {
		t.Errorf("a %d-rune output was gated as too short", MinMinimizeChars)
	}
}

func TestApplyGatesOnSize(t *testing.T) {
	cfg := Config{Enabled: true, MaxCaptureBytes: 100}
	big := strings.Repeat("x", 5000)
	if got := Apply("git status", big, 0, cfg); got.Filter != FilterTooLarge {
		t.Errorf("out.Filter = %q, want too-large", got.Filter)
	}
}

func TestApplyReportsAMissWithAReason(t *testing.T) {
	// Telemetry that cannot explain itself is telemetry nobody trusts when the
	// numbers look wrong.
	cfg := Config{Enabled: true}
	if got := Apply("definitely-not-real status",
		strings.Repeat("x", MinMinimizeChars+10), 0, cfg); got.Filter != FilterUnknown {
		t.Errorf("filter = %q, want unknown", got.Filter)
	}
	if got := Apply("", strings.Repeat("x", MinMinimizeChars+10), 0, cfg); got.Filter != FilterParseError {
		t.Errorf("filter = %q, want parse-error", got.Filter)
	}
}

func TestApplySkipsANonZeroExit(t *testing.T) {
	// A failing command's output is diagnostic; rewriting it would hide the
	// error the user needs to see.
	cfg := Config{Enabled: true}
	out := Apply("git status", strings.Repeat("x", MinMinimizeChars+10), 1, cfg)
	if out.Changed || out.Filter != FilterExitSkip {
		t.Errorf("out = %+v, want an exit-skip passthrough", out)
	}
}

func TestConfigAllowsProgramHonoursBothLists(t *testing.T) {
	cfg := Config{Enabled: true}
	if !cfg.AllowsProgram("git") {
		t.Error("an empty Only list must enable every program")
	}

	only := Config{Enabled: true, Only: []string{"git"}}
	if !only.AllowsProgram("git") {
		t.Error("an allowlisted program was refused")
	}
	if only.AllowsProgram("cargo") {
		t.Error("a program outside the allowlist was permitted")
	}

	skip := Config{Enabled: true, Skip: []string{"git"}}
	if skip.AllowsProgram("git") {
		t.Error("a skipped program was permitted")
	}
	// An explicit exclusion must beat an allowlist, or Skip is meaningless.
	both := Config{Enabled: true, Only: []string{"git"}, Skip: []string{"git"}}
	if both.AllowsProgram("git") {
		t.Error("a program on both lists was permitted")
	}

	if (Config{}).AllowsProgram("git") {
		t.Error("a disabled config permitted minimization")
	}
}

func TestConfigMaxBytesDefaultsForZero(t *testing.T) {
	if got := (Config{}).MaxBytes(); got != DefaultMaxCaptureBytes {
		t.Errorf("MaxBytes = %d, want the default", got)
	}
	if got := (Config{MaxCaptureBytes: 10}).MaxBytes(); got != 10 {
		t.Errorf("MaxBytes = %d, want the configured value", got)
	}
}

func TestOutputBytesSaved(t *testing.T) {
	out := Transformed("abc", 10, FilterGit)
	if got := out.BytesSaved(); got != 7 {
		t.Errorf("saved = %d, want 7", got)
	}
	// A passthrough must never claim a saving.
	if got := (Passthrough("x", FilterDisabled)).BytesSaved(); got != 0 {
		t.Errorf("passthrough saved = %d, want 0", got)
	}
	// A filter that grew the output must not report a negative saving.
	if got := (Transformed("abcdef", 3, FilterGit)).BytesSaved(); got != 0 {
		t.Errorf("growth reported as %d saved", got)
	}
}

func TestWithOriginalOnlyRecordsAChange(t *testing.T) {
	// There is nothing to persist a reference to when the text is unchanged.
	out := Passthrough("x", FilterDisabled).WithOriginal("x")
	if out.Original != "" {
		t.Error("a passthrough recorded an original")
	}
	out = Transformed("y", 3, FilterGit).WithOriginal("x")
	if out.Original != "x" {
		t.Errorf("original = %q, want the un-minimized capture", out.Original)
	}
}

func TestTokenizeHonoursQuotes(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"git status", []string{"git", "status"}},
		{`git commit -m "a message"`, []string{"git", "commit", "-m", "a message"}},
		{"git   status", []string{"git", "status"}},
		{"  git  ", []string{"git"}},
		{"", nil},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := Tokenize(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("token %d = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}
