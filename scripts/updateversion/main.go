// Command updateversion keeps the version-bearing files in sync with VERSION
// and cuts the release records for that version.
//
// scripts/update-version.sh is the entry point maintainers use; the logic lives
// here so it can be tested with the rest of the module. Nothing in this command
// talks to git: the tag remains the published authority, and this command only
// keeps the source tree honest about the version it is building.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/PizenLabs/ohgo/internal/update"
)

// The files this command owns. Each one carries a copy of the version because a
// different consumer reads it: the binary, the TUI fallback, the issue form,
// the human history, and the notes embedded in the binary.
const (
	versionPath   = "VERSION"
	mainPath      = "cmd/ohgo/main.go"
	infoPath      = "tui/infoview.go"
	issuePath     = ".github/ISSUE_TEMPLATE/bug_report.yml"
	changelogPath = "CHANGELOG.md"
	notesPath     = "update/changelog.json"
)

const dateLayout = "2006-01-02"

const usage = `usage: update-version.sh [VERSION] [options]

Propagate the release version to every file that embeds it. The VERSION file at
the repository root is the source of truth; passing VERSION (or --bump) writes
it first.

  VERSION            version to apply, with or without a leading "v"
                     (default: the VERSION file)

options:
  --bump KIND        bump VERSION instead of reading it: major, minor, or patch
  --release          move [Unreleased] in CHANGELOG.md to [vVERSION] - DATE and
                     generate its update/changelog.json release notes
  --check            report files that are out of sync and exit 1; writes nothing
  --dry-run          print what would change without writing it
  --date YYYY-MM-DD  release date for --release (default: today)
  --root DIR         repository root (default: the tree that contains go.mod)
  -h, --help         print this help
`

var (
	// mainVersion is the development default the release build overrides with
	// -X main.version. It must stay a single assignment so the override keeps
	// working.
	mainVersion = regexp.MustCompile(`(?m)^var version = "([^"]*)"$`)
	// infoVersion is the first line of the bundled changelog fallback.
	infoVersion = regexp.MustCompile(`(?s)(var defaultChangelog = strings\.Join\(\[\]string\{\n\t")([^"]*)(")`)
	// issueVersion is the example version in the bug report template.
	issueVersion = regexp.MustCompile(`(?m)^(\s*placeholder: ")v[0-9]+\.[0-9]+\.[0-9]+`)
	// nonSlug collapses everything that cannot appear in a change id.
	nonSlug = regexp.MustCompile(`[^a-z0-9]+`)
)

// errHelp asks run to print usage and exit 0.
var errHelp = errors.New("help requested")

// errDrift marks a --check run that found the tree out of sync. Drift is a
// normal negative result, so it exits 1; a usage or I/O failure exits 2.
var errDrift = errors.New("version files are out of sync")

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is the testable entry point. It never touches process globals other than
// the files under the resolved root.
func run(args []string, stdout, stderr io.Writer) int {
	err := execute(args, stdout)
	switch {
	case err == nil:
		return 0
	case errors.Is(err, errHelp):
		_, _ = fmt.Fprint(stdout, usage)
		return 0
	case errors.Is(err, errDrift):
		_, _ = fmt.Fprintf(stderr, "update-version: %v\n", err)
		return 1
	default:
		_, _ = fmt.Fprintf(stderr, "update-version: %v\n", err)
		return 2
	}
}

type options struct {
	version string
	bump    string
	release bool
	check   bool
	dryRun  bool
	date    string
	root    string
}

func parseArgs(args []string) (options, error) {
	var opts options
	for i := 0; i < len(args); i++ {
		arg := args[i]
		name, value, hasValue := strings.Cut(arg, "=")
		valueOf := func() (string, error) {
			if hasValue {
				if value == "" {
					return "", fmt.Errorf("%s needs a value", name)
				}
				return value, nil
			}
			if i+1 >= len(args) {
				return "", fmt.Errorf("%s needs a value", name)
			}
			i++
			return args[i], nil
		}
		noValue := func() error {
			if hasValue {
				return fmt.Errorf("%s takes no value", name)
			}
			return nil
		}

		switch name {
		case "-h", "--help":
			if err := noValue(); err != nil {
				return opts, err
			}
			return opts, errHelp
		case "--check":
			if err := noValue(); err != nil {
				return opts, err
			}
			opts.check = true
		case "--release":
			if err := noValue(); err != nil {
				return opts, err
			}
			opts.release = true
		case "--dry-run":
			if err := noValue(); err != nil {
				return opts, err
			}
			opts.dryRun = true
		case "--bump":
			v, err := valueOf()
			if err != nil {
				return opts, err
			}
			opts.bump = v
		case "--date":
			v, err := valueOf()
			if err != nil {
				return opts, err
			}
			opts.date = v
		case "--root":
			v, err := valueOf()
			if err != nil {
				return opts, err
			}
			opts.root = v
		default:
			if hasValue || strings.HasPrefix(arg, "-") {
				return opts, fmt.Errorf("unknown option %q", arg)
			}
			if opts.version != "" {
				return opts, fmt.Errorf("unexpected argument %q; pass one version", arg)
			}
			opts.version = arg
		}
	}
	return opts, nil
}

func execute(args []string, stdout io.Writer) error {
	opts, err := parseArgs(args)
	if err != nil {
		return err
	}
	if opts.check && (opts.release || opts.dryRun || opts.version != "" || opts.bump != "") {
		return errors.New("--check inspects the current tree only; drop the version, --bump, --release, and --dry-run flags")
	}
	if opts.date != "" && !opts.release {
		return errors.New("--date only applies to --release")
	}

	root, err := findRoot(opts.root)
	if err != nil {
		return err
	}
	target, err := resolveVersion(root, opts)
	if err != nil {
		return err
	}
	edits, err := buildEdits(root, target, opts)
	if err != nil {
		return err
	}
	if opts.check {
		return checkState(root, edits)
	}

	changed := 0
	for _, e := range edits {
		if bytes.Equal(e.old, e.new) {
			continue
		}
		changed++
		if opts.dryRun {
			_, _ = fmt.Fprintf(stdout, "would update %s\n", e.path)
			continue
		}
		if err := writeAtomic(root, e.path, e.new); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "updated %s\n", e.path)
	}

	switch {
	case opts.dryRun && changed > 0:
		_, _ = fmt.Fprintf(stdout, "would apply %s\n", target.String())
	case changed == 0:
		_, _ = fmt.Fprintf(stdout, "version %s is already in sync\n", target.String())
	default:
		_, _ = fmt.Fprintf(stdout, "version %s is in sync\n", target.String())
	}
	return nil
}

// findRoot locates the module root so the command can be run from anywhere in
// the tree or pointed at a fixture with --root.
func findRoot(explicit string) (string, error) {
	dir := explicit
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolve working directory: %w", err)
		}
		dir = wd
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w", dir, err)
	}
	for {
		if info, err := os.Stat(filepath.Join(abs, "go.mod")); err == nil && !info.IsDir() {
			return abs, nil
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return "", fmt.Errorf("no go.mod above %s; pass --root", dir)
		}
		abs = parent
	}
}

// resolveVersion applies the argument precedence: explicit version, then a
// bump of the VERSION file, then the VERSION file itself.
func resolveVersion(root string, opts options) (update.Version, error) {
	if opts.version != "" && opts.bump != "" {
		return update.Version{}, errors.New("pass either a version or --bump, not both")
	}

	current, err := readOptional(root, versionPath)
	if err != nil {
		return update.Version{}, err
	}

	raw := opts.version
	if raw == "" && opts.bump != "" {
		if len(current) == 0 {
			return update.Version{}, fmt.Errorf("%s is missing; write a version before --bump", versionPath)
		}
		base, ok := stableVersion(string(current))
		if !ok {
			return update.Version{}, fmt.Errorf("%s: %q is not MAJOR.MINOR.PATCH", versionPath, strings.TrimSpace(string(current)))
		}
		switch opts.bump {
		case "major":
			return update.Version{Major: base.Major + 1}, nil
		case "minor":
			return update.Version{Major: base.Major, Minor: base.Minor + 1}, nil
		case "patch":
			return update.Version{Major: base.Major, Minor: base.Minor, Patch: base.Patch + 1}, nil
		default:
			return update.Version{}, fmt.Errorf("--bump %q: use major, minor, or patch", opts.bump)
		}
	}
	if raw == "" {
		if len(current) == 0 {
			return update.Version{}, fmt.Errorf("%s is missing; pass a version or run --bump", versionPath)
		}
		raw = string(current)
	}

	ver, ok := stableVersion(raw)
	if !ok {
		return update.Version{}, fmt.Errorf("%q is not MAJOR.MINOR.PATCH (prereleases are not supported yet)", strings.TrimSpace(raw))
	}
	return ver, nil
}

// stableVersion parses a version that may be used as a release base. A
// prerelease is rejected rather than normalised: the files this command writes
// distinguish a release from a development build with a single -dev suffix, and
// a prerelease would make that suffix ambiguous.
func stableVersion(raw string) (update.Version, bool) {
	ver, ok := update.ParseVersion(raw)
	if !ok || len(ver.Pre) > 0 {
		return update.Version{}, false
	}
	return ver, true
}

// edit is one file transition. old==new means the file is already correct.
type edit struct {
	path string
	old  []byte
	new  []byte
}

func buildEdits(root string, target update.Version, opts options) ([]edit, error) {
	verText := target.String()
	var edits []edit

	versionOld, err := readOptional(root, versionPath)
	if err != nil {
		return nil, err
	}
	edits = append(edits, edit{path: versionPath, old: versionOld, new: []byte(verText + "\n")})

	mainData, err := readRequired(root, mainPath)
	if err != nil {
		return nil, err
	}
	mainNew, err := replaceOnce(mainPath, mainData, mainVersion, `var version = "`+verText+`-dev"`)
	if err != nil {
		return nil, err
	}
	edits = append(edits, edit{path: mainPath, old: mainData, new: mainNew})

	infoData, err := readRequired(root, infoPath)
	if err != nil {
		return nil, err
	}
	infoNew, err := replaceOnce(infoPath, infoData, infoVersion, "${1}v"+verText+"-dev$3")
	if err != nil {
		return nil, err
	}
	edits = append(edits, edit{path: infoPath, old: infoData, new: infoNew})

	issueData, err := readRequired(root, issuePath)
	if err != nil {
		return nil, err
	}
	issueNew, err := replaceOnce(issuePath, issueData, issueVersion, "${1}v"+verText)
	if err != nil {
		return nil, err
	}
	edits = append(edits, edit{path: issuePath, old: issueData, new: issueNew})

	notesOld, err := readOptional(root, notesPath)
	if err != nil {
		return nil, err
	}
	doc := changelogDoc{Schema: 1, Releases: map[string]update.ReleaseNotes{}}
	if len(notesOld) > 0 {
		parsed, err := update.ParseChangelog(notesOld)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", notesPath, err)
		}
		doc.Schema, doc.Releases = parsed.Schema, parsed.Releases
	}

	if opts.release {
		date, err := releaseDate(opts.date)
		if err != nil {
			return nil, err
		}
		changelogOld, err := readRequired(root, changelogPath)
		if err != nil {
			return nil, err
		}
		changelogNew, changes, err := cutRelease(changelogOld, verText, date)
		if err != nil {
			return nil, err
		}
		doc.Releases["v"+verText] = update.ReleaseNotes{
			Title:   "What's new in v" + verText,
			Changes: changes,
		}
		edits = append(edits, edit{path: changelogPath, old: changelogOld, new: changelogNew})
	}

	if len(notesOld) > 0 || opts.release {
		notesNew, err := encodeNotes(doc)
		if err != nil {
			return nil, err
		}
		// The embedded notes are read by update.ParseChangelog at startup; a
		// document this command writes must pass the same gate.
		if _, err := update.ParseChangelog(notesNew); err != nil {
			return nil, fmt.Errorf("generated %s is invalid: %w", notesPath, err)
		}
		edits = append(edits, edit{path: notesPath, old: notesOld, new: notesNew})
	}

	return edits, nil
}

func releaseDate(value string) (string, error) {
	if value == "" {
		return time.Now().Format(dateLayout), nil
	}
	if _, err := time.Parse(dateLayout, value); err != nil {
		return "", fmt.Errorf("--date %q: want YYYY-MM-DD", value)
	}
	return value, nil
}

// checkState reports every file that differs from the expected content and the
// changelog invariants, without writing anything.
func checkState(root string, edits []edit) error {
	var problems []string
	for _, e := range edits {
		if !bytes.Equal(e.old, e.new) {
			problems = append(problems, e.path+" is out of date")
		}
	}
	more, err := changelogProblems(root)
	if err != nil {
		return err
	}
	problems = append(problems, more...)
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("%w:\n  %s", errDrift, strings.Join(problems, "\n  "))
}

// changelogProblems enforces the release-record contract: every released
// CHANGELOG section has notes keyed by the same tag, releases are newest first,
// and the notes file is canonically formatted.
func changelogProblems(root string) ([]string, error) {
	changelog, err := readRequired(root, changelogPath)
	if err != nil {
		return nil, err
	}
	tags, err := releasedTags(string(changelog))
	if err != nil {
		return nil, err
	}
	notes, err := readRequired(root, notesPath)
	if err != nil {
		return nil, err
	}
	doc, err := update.ParseChangelog(notes)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", notesPath, err)
	}

	var problems []string
	previous := update.Version{}
	havePrevious := false
	for _, tag := range tags {
		if _, ok := doc.Releases[tag]; !ok {
			problems = append(problems, fmt.Sprintf("%s has no %s entry; the key must match the tag, including the leading v", notesPath, tag))
		}
		parsed, ok := update.ParseVersion(tag)
		if !ok {
			continue
		}
		if havePrevious && parsed.Compare(previous) >= 0 {
			problems = append(problems, fmt.Sprintf("%s: %s must appear below newer releases", changelogPath, tag))
		}
		previous, havePrevious = parsed, true
	}

	canonical, err := encodeNotes(changelogDoc{Schema: doc.Schema, Releases: doc.Releases})
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(notes, canonical) {
		problems = append(problems, notesPath+" is not in canonical form; run the script without --check to reformat it")
	}
	return problems, nil
}

// releasedTags returns the release tags named by CHANGELOG.md in file order and
// validates the heading shape the release workflow relies on.
func releasedTags(text string) ([]string, error) {
	var tags []string
	for _, line := range strings.Split(text, "\n") {
		if !strings.HasPrefix(line, "## [") {
			continue
		}
		open := strings.IndexByte(line, '[')
		end := strings.IndexByte(line, ']')
		if open < 0 || end < open {
			return nil, fmt.Errorf("%s: malformed heading %q", changelogPath, line)
		}
		tag := line[open+1 : end]
		if tag == "Unreleased" {
			continue
		}
		if _, ok := update.ParseVersion(tag); !ok {
			return nil, fmt.Errorf("%s: %q is not a release tag", changelogPath, tag)
		}
		rest := strings.TrimSpace(line[end+1:])
		if _, err := time.Parse(dateLayout, strings.TrimPrefix(rest, "- ")); err != nil {
			return nil, fmt.Errorf("%s: %s needs a - YYYY-MM-DD date", changelogPath, tag)
		}
		tags = append(tags, tag)
	}
	return tags, nil
}

// cutRelease moves the [Unreleased] body under a new dated heading and returns
// the notes it contained. It refuses a version that already has a section so
// released history cannot be rewritten by re-running the command.
func cutRelease(data []byte, version, date string) ([]byte, []update.Change, error) {
	lines := strings.Split(string(data), "\n")
	start := -1
	for i, line := range lines {
		if line == "## [Unreleased]" {
			start = i
			break
		}
	}
	if start < 0 {
		return nil, nil, fmt.Errorf("%s: no \"## [Unreleased]\" heading", changelogPath)
	}
	for _, line := range lines {
		if strings.HasPrefix(line, "## [v"+version+"]") {
			return nil, nil, fmt.Errorf("%s: v%s is already released; bump %s before --release", changelogPath, version, versionPath)
		}
	}

	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "## ") {
			end = i
			break
		}
	}
	body := trimBlank(lines[start+1 : end])
	changes, err := parseNotes(body)
	if err != nil {
		return nil, nil, err
	}
	if len(changes) == 0 {
		return nil, nil, fmt.Errorf("%s: the [Unreleased] section has no release notes; add user-visible entries before --release", changelogPath)
	}

	out := make([]string, 0, len(lines)+4)
	out = append(out, lines[:start]...)
	out = append(out, "## [Unreleased]", "", "## [v"+version+"] - "+date, "")
	out = append(out, body...)
	if end < len(lines) {
		out = append(out, "")
	}
	out = append(out, lines[end:]...)
	return []byte(strings.Join(out, "\n")), changes, nil
}

// parseNotes reads the category headings and their bullets. It is strict on
// purpose: a paragraph the generator does not understand is reported instead of
// silently omitted from the release card.
func parseNotes(lines []string) ([]update.Change, error) {
	var (
		changes  []update.Change
		kind     update.ChangeType
		haveKind bool
		bullet   []string
		seen     = map[string]bool{}
	)
	flush := func() {
		if len(bullet) == 0 {
			return
		}
		text := strings.Join(bullet, " ")
		bullet = nil
		title, description := splitNote(text)
		if title == "" {
			return
		}
		id := uniqueID(slug(title), seen)
		seen[id] = true
		changes = append(changes, update.Change{ID: id, Type: kind, Title: title, Description: description})
	}
	for _, raw := range lines {
		line := strings.TrimRight(raw, "\r")
		switch {
		case strings.HasPrefix(line, "### "):
			flush()
			t, ok := changeTypeFor(line[4:])
			if !ok {
				return nil, fmt.Errorf("%s: unsupported section %q; use Added, Improved, Changed, or Fixed", changelogPath, strings.TrimSpace(line[4:]))
			}
			kind, haveKind = t, true
		case strings.HasPrefix(line, "- "):
			if !haveKind {
				return nil, fmt.Errorf("%s: bullet %q appears before a section heading", changelogPath, strings.TrimSpace(line))
			}
			flush()
			bullet = []string{strings.TrimSpace(line[2:])}
		case strings.TrimSpace(line) == "":
			flush()
		case (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")) && len(bullet) > 0:
			bullet = append(bullet, strings.TrimSpace(line))
		default:
			return nil, fmt.Errorf("%s: line %q is not a section heading or bullet", changelogPath, line)
		}
	}
	flush()
	return changes, nil
}

func changeTypeFor(heading string) (update.ChangeType, bool) {
	switch strings.ToLower(strings.TrimSpace(heading)) {
	case "added":
		return update.ChangeAdded, true
	case "improved":
		return update.ChangeImproved, true
	case "changed":
		return update.ChangeChanged, true
	case "fixed":
		return update.ChangeFixed, true
	}
	return "", false
}

// splitNote derives the card-sized title and the optional detail from one
// changelog bullet. The update card renders titles only, so the title must stand
// alone; the rest of the sentence becomes the description rather than being
// dropped.
func splitNote(text string) (title, description string) {
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return "", ""
	}
	if i := strings.Index(text, ": "); i > 0 && i <= 72 {
		return trimTitle(text[:i]), strings.TrimSpace(text[i+2:])
	}
	if i := strings.Index(text, ". "); i > 0 && i <= 100 {
		return trimTitle(text[:i]), strings.TrimSpace(text[i+2:])
	}
	if len(text) <= 100 {
		return trimTitle(text), ""
	}
	// A long sentence without punctuation: cut on a word boundary and keep the
	// remainder as the description so no information is lost.
	cut := strings.LastIndex(text[:100], " ")
	if cut <= 0 {
		cut = 100
	}
	return trimTitle(text[:cut]), strings.TrimSpace(text[cut:])
}

func trimTitle(title string) string {
	return strings.TrimSuffix(strings.TrimSpace(title), ".")
}

func slug(title string) string {
	s := nonSlug.ReplaceAllString(strings.ToLower(title), "-")
	s = strings.Trim(s, "-")
	if len(s) > 48 {
		s = strings.Trim(s[:48], "-")
	}
	if s == "" {
		return "change"
	}
	return s
}

func uniqueID(base string, seen map[string]bool) string {
	id := base
	for n := 2; seen[id]; n++ {
		id = fmt.Sprintf("%s-%d", base, n)
	}
	return id
}

func trimBlank(lines []string) []string {
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// changelogDoc is the notes document with a deterministic release order. Go
// maps have no order, and a stable file keeps release diffs to the entry that
// changed.
type changelogDoc struct {
	Schema   int
	Releases map[string]update.ReleaseNotes
}

func (d changelogDoc) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	schema, err := json.Marshal(d.Schema)
	if err != nil {
		return nil, err
	}
	buf.WriteString(`{"schema":`)
	buf.Write(schema)
	buf.WriteString(`,"releases":{`)
	for i, tag := range d.tags() {
		if i > 0 {
			buf.WriteByte(',')
		}
		key, err := json.Marshal(tag)
		if err != nil {
			return nil, err
		}
		value, err := json.Marshal(d.Releases[tag])
		if err != nil {
			return nil, err
		}
		buf.Write(key)
		buf.WriteByte(':')
		buf.Write(value)
	}
	buf.WriteString(`}}`)
	return buf.Bytes(), nil
}

// tags returns the release keys oldest first so a new release appends to the
// end of the file.
func (d changelogDoc) tags() []string {
	tags := make([]string, 0, len(d.Releases))
	for tag := range d.Releases {
		tags = append(tags, tag)
	}
	sort.Slice(tags, func(i, j int) bool {
		a, aOK := update.ParseVersion(tags[i])
		b, bOK := update.ParseVersion(tags[j])
		switch {
		case aOK && bOK:
			if c := a.Compare(b); c != 0 {
				return c < 0
			}
			return tags[i] < tags[j]
		case aOK != bOK:
			return aOK
		default:
			return tags[i] < tags[j]
		}
	})
	return tags
}

func encodeNotes(doc changelogDoc) ([]byte, error) {
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode %s: %w", notesPath, err)
	}
	return append(data, '\n'), nil
}

func replaceOnce(rel string, data []byte, re *regexp.Regexp, replacement string) ([]byte, error) {
	switch matches := re.FindAllIndex(data, -1); len(matches) {
	case 1:
		return re.ReplaceAll(data, []byte(replacement)), nil
	case 0:
		return nil, fmt.Errorf("%s: no version marker matched %s; the file layout changed", rel, re)
	default:
		return nil, fmt.Errorf("%s: %d version markers matched %s; want exactly one", rel, len(matches), re)
	}
}

func readRequired(root, rel string) ([]byte, error) {
	data, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", rel, err)
	}
	return data, nil
}

func readOptional(root, rel string) ([]byte, error) {
	data, err := os.ReadFile(filepath.Join(root, rel))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", rel, err)
	}
	return data, nil
}

// writeAtomic replaces the file through a sibling temporary file so a failure
// never leaves a half-written version marker behind.
func writeAtomic(root, rel string, data []byte) error {
	path := filepath.Join(root, rel)
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".update-version-*")
	if err != nil {
		return fmt.Errorf("write %s: %w", rel, err)
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write %s: %w", rel, err)
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write %s: %w", rel, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write %s: %w", rel, err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("write %s: %w", rel, err)
	}
	return nil
}
