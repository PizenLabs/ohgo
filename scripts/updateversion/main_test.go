package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/update"
)

const syncedMain = `var version = "0.0.3-dev"`

// fixtures builds the smallest tree the command owns. The stale markers are the
// previous release's, which is the state a maintainer actually starts from.
func fixtures(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"go.mod":  "module example.com/fixture\n",
		"VERSION": "0.0.3\n",
		"cmd/ohgo/main.go": "package main\n\n" +
			"// version is overridden at build time.\n" +
			"var version = \"0.1.0-dev\"\n",
		"tui/infoview.go": "package tui\n\nimport \"strings\"\n\n" +
			"var defaultChangelog = strings.Join([]string{\n" +
			"\t\"v0.1.0-dev\",\n" +
			"\t\"  - Something.\",\n" +
			"}, \"\\n\")\n",
		".github/ISSUE_TEMPLATE/bug_report.yml": "- type: input\n  attributes:\n" +
			"    label: Version\n    placeholder: \"v0.1.0 / ed60b2b-dirty\"\n",
		"CHANGELOG.md": "# Changelog\n\n## [Unreleased]\n\n" +
			"## [v0.0.2] - 2026-10-28\n\n### Fixed\n\n- An old thing.\n",
	}
	for name, body := range files {
		writeFile(t, dir, name, body)
	}
	notes, err := encodeNotes(changelogDoc{Schema: 1, Releases: map[string]update.ReleaseNotes{
		"v0.0.2": {
			Title:   "What's new in v0.0.2",
			Changes: []update.Change{{ID: "an-old-thing", Type: update.ChangeFixed, Title: "An old thing"}},
		},
	}})
	if err != nil {
		t.Fatalf("encode fixture notes: %v", err)
	}
	writeFile(t, dir, "update/changelog.json", string(notes))
	return dir
}

func writeFile(t *testing.T, dir, rel, body string) {
	t.Helper()
	path := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", rel, err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

func readFile(t *testing.T, dir, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(data)
}

func runScript(t *testing.T, dir string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = run(append([]string{"--root", dir}, args...), &out, &errOut)
	return code, out.String(), errOut.String()
}

func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[rel] = string(data)
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	return files
}

func TestSyncWritesEveryVersionMarker(t *testing.T) {
	dir := fixtures(t)

	code, _, stderr := runScript(t, dir, "0.4.0")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}

	if got := readFile(t, dir, "VERSION"); got != "0.4.0\n" {
		t.Errorf("VERSION = %q", got)
	}
	if got := readFile(t, dir, "cmd/ohgo/main.go"); !strings.Contains(got, `var version = "0.4.0-dev"`) {
		t.Errorf("main.go = %q", got)
	}
	if got := readFile(t, dir, "tui/infoview.go"); !strings.Contains(got, `"v0.4.0-dev"`) || strings.Contains(got, "v0.1.0-dev") {
		t.Errorf("infoview.go = %q", got)
	}
	if got := readFile(t, dir, ".github/ISSUE_TEMPLATE/bug_report.yml"); !strings.Contains(got, `placeholder: "v0.4.0 / ed60b2b-dirty"`) {
		t.Errorf("bug_report.yml = %q", got)
	}
}

func TestSyncIsIdempotent(t *testing.T) {
	dir := fixtures(t)
	if code, _, stderr := runScript(t, dir); code != 0 {
		t.Fatalf("first run exit %d: %s", code, stderr)
	}
	before := snapshot(t, dir)

	code, stdout, stderr := runScript(t, dir)
	if code != 0 {
		t.Fatalf("second run exit %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "already in sync") {
		t.Errorf("stdout = %q, want an in-sync report", stdout)
	}
	if after := snapshot(t, dir); !equalSnapshots(before, after) {
		t.Errorf("second run rewrote files")
	}
}

func equalSnapshots(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for name, body := range a {
		if b[name] != body {
			return false
		}
	}
	return true
}

func TestBumpComputesTheNextVersion(t *testing.T) {
	cases := map[string]struct {
		kind string
		want string
	}{
		"patch": {kind: "patch", want: "0.0.4"},
		"minor": {kind: "minor", want: "0.1.0"},
		"major": {kind: "major", want: "1.0.0"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dir := fixtures(t)
			code, _, stderr := runScript(t, dir, "--bump", tc.kind)
			if code != 0 {
				t.Fatalf("exit %d: %s", code, stderr)
			}
			if got := readFile(t, dir, "VERSION"); got != tc.want+"\n" {
				t.Errorf("VERSION = %q, want %q", got, tc.want+"\n")
			}
		})
	}
}

func TestBumpRejectsAnUnknownKind(t *testing.T) {
	dir := fixtures(t)
	if code, _, stderr := runScript(t, dir, "--bump", "huge"); code == 0 {
		t.Fatalf("unknown bump kind succeeded: %s", stderr)
	}
	if got := readFile(t, dir, "VERSION"); got != "0.0.3\n" {
		t.Errorf("VERSION changed to %q", got)
	}
}

func TestParseArgsRejectsMalformedOptions(t *testing.T) {
	for _, arg := range []string{"--check=yes", "--root=", "--bump=", "--wat"} {
		if _, err := parseArgs([]string{arg}); err == nil {
			t.Errorf("parseArgs accepted %q", arg)
		}
	}
}

func TestCheckReportsDriftWithoutWriting(t *testing.T) {
	dir := fixtures(t)
	before := snapshot(t, dir)

	code, _, stderr := runScript(t, dir, "--check")
	if code != 1 {
		t.Fatalf("exit %d, want 1 for drift; stderr = %s", code, stderr)
	}
	if !strings.Contains(stderr, "cmd/ohgo/main.go") {
		t.Errorf("stderr = %q, want the stale path named", stderr)
	}
	if after := snapshot(t, dir); !equalSnapshots(before, after) {
		t.Errorf("--check wrote to the tree")
	}
}

func TestCheckPassesAfterSync(t *testing.T) {
	dir := fixtures(t)
	if code, _, stderr := runScript(t, dir); code != 0 {
		t.Fatalf("sync exit %d: %s", code, stderr)
	}
	if code, _, stderr := runScript(t, dir, "--check"); code != 0 {
		t.Fatalf("check exit %d: %s", code, stderr)
	}
	if got := readFile(t, dir, "cmd/ohgo/main.go"); !strings.Contains(got, syncedMain) {
		t.Errorf("main.go = %q", got)
	}
}

func TestReleaseMovesNotesIntoADatedSection(t *testing.T) {
	dir := fixtures(t)
	writeFile(t, dir, "CHANGELOG.md", `# Changelog

## [Unreleased]

### Added

- Explicit self-update: it resolves the published release.
- MCP capability layer: normalized identities.

### Fixed

- A short sentence.

## [v0.0.2] - 2026-10-28

### Fixed

- An old thing.
`)

	code, _, stderr := runScript(t, dir, "--release", "--date", "2026-10-29")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}

	changelog := readFile(t, dir, "CHANGELOG.md")
	if !strings.Contains(changelog, "## [Unreleased]\n\n## [v0.0.3] - 2026-10-29\n") {
		t.Errorf("changelog does not start the new section:\n%s", changelog)
	}
	if !strings.Contains(changelog, "## [v0.0.2] - 2026-10-28") {
		t.Errorf("changelog lost the previous release:\n%s", changelog)
	}
	if body := sectionBody(changelog, "## [Unreleased]", "## [v0.0.3]"); strings.Contains(body, "- ") {
		t.Errorf("[Unreleased] still holds notes: %q", body)
	}

	notes := readFile(t, dir, "update/changelog.json")
	doc, err := update.ParseChangelog([]byte(notes))
	if err != nil {
		t.Fatalf("generated notes are invalid: %v", err)
	}
	release, ok := doc.Release("v0.0.3")
	if !ok {
		t.Fatal("generated notes have no v0.0.3 entry")
	}
	if release.Title != "What's new in v0.0.3" {
		t.Errorf("title = %q", release.Title)
	}
	want := []struct {
		id, typ, title, description string
	}{
		{"explicit-self-update", "added", "Explicit self-update", "it resolves the published release."},
		{"mcp-capability-layer", "added", "MCP capability layer", "normalized identities."},
		{"a-short-sentence", "fixed", "A short sentence", ""},
	}
	if len(release.Changes) != len(want) {
		t.Fatalf("changes = %+v", release.Changes)
	}
	for i, expected := range want {
		got := release.Changes[i]
		if got.ID != expected.id || string(got.Type) != expected.typ || got.Title != expected.title || got.Description != expected.description {
			t.Errorf("change %d = %+v, want %+v", i, got, expected)
		}
	}
	if got := readFile(t, dir, "cmd/ohgo/main.go"); !strings.Contains(got, syncedMain) {
		t.Errorf("main.go = %q", got)
	}
}

func TestReleaseRefusesAnAlreadyReleasedVersion(t *testing.T) {
	dir := fixtures(t)
	writeFile(t, dir, "VERSION", "0.0.2\n")
	before := readFile(t, dir, "CHANGELOG.md")

	code, _, stderr := runScript(t, dir, "--release")
	if code == 0 {
		t.Fatal("releasing an existing version succeeded")
	}
	if !strings.Contains(stderr, "already released") {
		t.Errorf("stderr = %q", stderr)
	}
	if after := readFile(t, dir, "CHANGELOG.md"); after != before {
		t.Errorf("changelog was rewritten")
	}
}

func TestReleaseRefusesAnEmptyUnreleasedSection(t *testing.T) {
	dir := fixtures(t)
	before := readFile(t, dir, "CHANGELOG.md")

	code, _, stderr := runScript(t, dir, "--release", "--date", "2026-10-29")
	if code == 0 {
		t.Fatal("releasing an empty section succeeded")
	}
	if !strings.Contains(stderr, "no release notes") {
		t.Errorf("stderr = %q", stderr)
	}
	if after := readFile(t, dir, "CHANGELOG.md"); after != before {
		t.Errorf("changelog was rewritten")
	}
}

func TestSyncReformatsNotesWithoutLosingEntries(t *testing.T) {
	dir := fixtures(t)
	raw := `{"schema":1,"releases":{"v0.0.2":{"title":"B","changes":[{"id":"b","type":"fixed","title":"B"}]},"v0.0.1":{"title":"A","changes":[{"id":"a","type":"added","title":"A"}]}}}`
	writeFile(t, dir, "update/changelog.json", raw)

	code, _, stderr := runScript(t, dir)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}

	want, err := encodeNotes(changelogDoc{Schema: 1, Releases: map[string]update.ReleaseNotes{
		"v0.0.1": {Title: "A", Changes: []update.Change{{ID: "a", Type: update.ChangeAdded, Title: "A"}}},
		"v0.0.2": {Title: "B", Changes: []update.Change{{ID: "b", Type: update.ChangeFixed, Title: "B"}}},
	}})
	if err != nil {
		t.Fatalf("encode expected notes: %v", err)
	}
	if got := readFile(t, dir, "update/changelog.json"); got != string(want) {
		t.Errorf("notes =\n%s\nwant\n%s", got, want)
	}
}

func TestSplitNoteDerivesACardTitle(t *testing.T) {
	cases := []struct {
		in, title, description string
	}{
		{"Explicit self-update: it resolves the release.", "Explicit self-update", "it resolves the release."},
		{"First sentence. Second sentence.", "First sentence", "Second sentence."},
		{"A short sentence.", "A short sentence", ""},
	}
	for _, tc := range cases {
		title, description := splitNote(tc.in)
		if title != tc.title || description != tc.description {
			t.Errorf("splitNote(%q) = (%q, %q), want (%q, %q)", tc.in, title, description, tc.title, tc.description)
		}
	}
}

func TestSplitNoteBoundsALongSentence(t *testing.T) {
	long := strings.Repeat("word ", 40)
	title, description := splitNote(long)
	if len(title) > 100 {
		t.Errorf("title is %d bytes; the card needs a bounded title", len(title))
	}
	if description == "" {
		t.Error("the remainder was dropped instead of becoming the description")
	}
}

func TestDuplicateTitlesGetDistinctIDs(t *testing.T) {
	changes, err := parseNotes([]string{"### Added", "", "- Same title: first.", "- Same title: second."})
	if err != nil {
		t.Fatalf("parseNotes: %v", err)
	}
	if len(changes) != 2 {
		t.Fatalf("changes = %+v", changes)
	}
	if changes[0].ID == changes[1].ID {
		t.Fatalf("both changes share id %q; ids must be unique within a release", changes[0].ID)
	}
}

func sectionBody(text, heading, next string) string {
	start := strings.Index(text, heading)
	if start < 0 {
		return ""
	}
	start += len(heading)
	end := strings.Index(text[start:], next)
	if end < 0 {
		return text[start:]
	}
	return text[start : start+end]
}
