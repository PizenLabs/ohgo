package agent

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// ShellSourceRead is one file a shell command opens as content. It is the
// shared interpretation of commands such as "cd backend && sed -n '1,90p'
// layout/bounded_context.go" and "cat README.md".
//
// WholeFile is true only when the reader emitted the entire file into the
// command's output. A paged read is an observation of that page, not of the
// file's total length.
type ShellSourceRead struct {
	Path      string
	WholeFile bool
}

var (
	unopenedCitationPattern = regexp.MustCompile(
		`(?i)\b(?:not|never)\s+(?:been\s+)?(?:opened|read|inspected|examined)\b`)
	shellReadCommand   = regexp.MustCompile(`(?i)^(?:cat|sed|head|tail|nl|less|more|bat)$`)
	shellReadSeparator = regexp.MustCompile(`\s*(?:&&|\|\||;|\n)\s*`)
	shellSedRange      = regexp.MustCompile(`^[0-9]+(?:,[0-9]+)?[a-zA-Z]?$`)
	shellSedScript     = regexp.MustCompile(`^[sgy][/|#]`)
	shellNumericArg    = regexp.MustCompile(`^-?[0-9]+$`)
)

// ShellSourceReads returns the file paths a shell command opens as content.
//
// Models page large source files through sed/head/tail instead of the read
// tool. Treating those as an unopened file loses real observations and makes
// the audit contradict the transcript. The parser is deliberately bounded: it
// recognizes content readers, tracks cd, and ignores scripts and flags. It is
// not a shell interpreter and does not accept a listing or a search.
func ShellSourceReads(command string) []ShellSourceRead {
	command = filepath.ToSlash(command)
	if strings.TrimSpace(command) == "" {
		return nil
	}
	var reads []ShellSourceRead
	cwd := ""
	for _, segment := range shellReadSeparator.Split(command, -1) {
		segment = strings.TrimSpace(segment)
		if segment == "" {
			continue
		}
		parts := strings.Split(segment, "|")
		first := strings.TrimSpace(parts[0])
		fields := strings.Fields(first)
		index := 0
		for index < len(fields) {
			token := strings.Trim(fields[index], `"'`)
			if token == "sudo" || token == "command" || token == "env" || strings.Contains(token, "=") {
				index++
				continue
			}
			break
		}
		if index >= len(fields) {
			continue
		}
		reader := strings.ToLower(strings.Trim(fields[index], "\"'"))
		if reader == "cd" {
			if index+1 < len(fields) {
				cwd = joinShellPath(cwd, strings.Trim(fields[index+1], "\"'"))
			}
			continue
		}
		if !shellReadCommand.MatchString(reader) {
			continue
		}
		paths, whole := shellReadPaths(reader, fields[index+1:])
		if len(parts) > 1 {
			whole = false
		}
		if len(paths) > 1 {
			whole = false
		}
		for _, path := range paths {
			reads = append(reads, ShellSourceRead{
				Path:      joinShellPath(cwd, path),
				WholeFile: whole,
			})
		}
	}
	return reads
}

// shellReadPaths extracts candidate file arguments. sed's script is not a
// path, and a reader flag is not a path. A redirection makes the output a
// partial capture, so it is never a whole-file measurement.
func shellReadPaths(reader string, args []string) ([]string, bool) {
	whole := reader == "cat" || reader == "nl" || reader == "bat"
	var paths []string
	for index := 0; index < len(args); index++ {
		arg := strings.Trim(args[index], "\"'")
		if arg == "" {
			continue
		}
		if arg == ">" || arg == ">>" || arg == "2>" || arg == "2>>" {
			whole = false
			break
		}
		if strings.ContainsAny(arg, "<>") {
			whole = false
			continue
		}
		if strings.HasPrefix(arg, "-") {
			if (reader == "head" || reader == "tail") && (arg == "-n" || arg == "-c") {
				index++
			}
			if reader == "sed" && (arg == "-e" || arg == "-f" || strings.HasPrefix(arg, "--expression") || strings.HasPrefix(arg, "--file")) {
				index++
			}
			if reader == "sed" && (arg == "-i" || arg == "--in-place") {
				return nil, false
			}
			continue
		}
		if reader == "sed" {
			if shellSedRange.MatchString(arg) || shellSedScript.MatchString(arg) {
				continue
			}
		}
		if (reader == "head" || reader == "tail") && shellNumericArg.MatchString(arg) {
			continue
		}
		paths = append(paths, arg)
	}
	return paths, whole
}

func joinShellPath(base, name string) string {
	name = strings.TrimPrefix(filepath.ToSlash(name), "./")
	if base == "" || strings.HasPrefix(name, "/") {
		return name
	}
	return strings.TrimSuffix(base, "/") + "/" + name
}

// A path citation is an evidence reference: the answer points the reader at the
// file that is supposed to establish its claim.
//
// "Search: FlexSearch with a build-time prebuilt index (lib/search.ts)" reads as
// proof because the coordinate looks inspected, but a listing proves only that
// the path exists. The run never opened the file, so the answer cannot tell a
// wired mechanism from a declared dependency — and the reader has no way to
// notice. This audit is deliberately narrow: it fires on an explicit citation
// form (a parenthesized or bracketed source file), never on a path mentioned in
// prose, so a route table or an inventory answer is not forced into a review.

// citationClaimIssue checks every source file the answer cites as evidence.
func citationClaimIssue(sentence string, turns []Turn) *claimIssue {
	// A sentence that names a path in order to say it was not opened is an
	// explicit limitation, not a citation. The completion audit must not
	// demand evidence the answer already says it did not inspect.
	if unopenedCitationPattern.MatchString(sentence) {
		return nil
	}
	for _, match := range claimPathPattern.FindAllStringIndex(sentence, -1) {
		path := strings.Trim(sentence[match[0]:match[1]], "`'\" ")
		if !isImplementationPath(path) || !citedAsEvidence(sentence, match[0]) {
			continue
		}
		if citationSourceObserved(turns, path) || shellSourceCitationObserved(turns, path) {
			continue
		}
		start, end := citationLineWindow(sentence, match[1])
		if citationSearchObserved(turns, path, start, end) {
			continue
		}
		return &claimIssue{
			Code: "unsupported_source_citation",
			Detail: fmt.Sprintf(
				"the answer cites %s as evidence, but the run never opened it; a listing proves the path exists, not what it implements. Read it or present the path as an unverified pointer",
				path),
			Claim:       path,
			Location:    claimLocation(sentence, path),
			Category:    "citation",
			Requirement: "source content for the cited path",
			Reference:   path,
			Action:      "read the cited path or present it as an unverified pointer",
		}
	}
	return nil
}

// citedAsEvidence reports whether the path is presented in an explicit citation
// form: parentheses or brackets, optionally inside inline code.
func citedAsEvidence(sentence string, start int) bool {
	i := start - 1
	for i >= 0 && (sentence[i] == '`' || sentence[i] == ' ' || sentence[i] == '\t') {
		i--
	}
	return i >= 0 && (sentence[i] == '(' || sentence[i] == '[')
}

// shellSourceCitationObserved reports a successful shell content read of the
// cited path. Models frequently page a large Go file through `sed` instead of
// the read tool; refusing that observation would report a file as unopened even
// though its contents were in the run. Only a content reader counts, so a
// directory listing or a path mentioned in a command is still not evidence.
func shellSourceCitationObserved(turns []Turn, path string) bool {
	path = filepath.ToSlash(strings.TrimSpace(path))
	if path == "" {
		return false
	}
	for _, turn := range turns {
		for _, outcome := range turn.Results {
			if outcome.ToolName != "bash" || outcome.Result.IsError || outcome.Refused() ||
				outcome.Result.Rejected() {
				continue
			}
			command := proposalArgString(outcome.Args, "command")
			for _, read := range ShellSourceReads(command) {
				if claimPathsMatch(read.Path, path) {
					return true
				}
			}
		}
	}
	return false
}

// citationLineWindow returns the line range the answer attaches to a cited
// path, or zero when it gives none. "service.go:490-502" and "service.go:490"
// are explicit matched-line citations.
func citationLineWindow(sentence string, afterPath int) (int, int) {
	if afterPath < 0 || afterPath >= len(sentence) {
		return 0, 0
	}
	rest := sentence[afterPath:]
	limit := len(rest)
	if limit > 24 {
		limit = 24
	}
	rest = strings.TrimLeft(rest[:limit], " `'\"")
	if rest == "" || (rest[0] != ':' && rest[0] != '#') {
		return 0, 0
	}
	rest = rest[1:]
	if rest == "" {
		return 0, 0
	}
	start := 0
	for start < len(rest) && rest[start] >= '0' && rest[start] <= '9' {
		start++
	}
	if start == 0 {
		return 0, 0
	}
	first, ok := claimCountAllowZero(rest[:start])
	if !ok || first <= 0 {
		return 0, 0
	}
	last := first
	if start < len(rest) && rest[start] == '-' {
		digits := start + 1
		end := digits
		for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
			end++
		}
		if end > digits {
			if value, valid := claimCountAllowZero(rest[digits:end]); valid && value >= first {
				last = value
			}
		}
	}
	return first, last
}

// citationSearchObserved reports a grep-shaped observation of the cited path.
// A search that returned "path:line: content" did open those lines; a listing
// or find result that only names the path did not. When the answer gives a
// line window, at least one observed match must fall inside it, so a citation
// cannot borrow an unrelated match from the same file.
func citationSearchObserved(turns []Turn, path string, windowStart, windowEnd int) bool {
	needle := strings.ToLower(filepath.ToSlash(strings.TrimSpace(path)))
	if needle == "" {
		return false
	}
	for _, turn := range turns {
		for _, outcome := range turn.Results {
			if outcome.Result.IsError || outcome.Refused() || outcome.Result.Rejected() {
				continue
			}
			switch outcome.ToolName {
			case "grep", "bash", "search":
			default:
				continue
			}
			for _, line := range strings.Split(outcome.Result.Text, "\n") {
				lower := strings.ToLower(line)
				index := strings.Index(lower, needle)
				if index < 0 {
					continue
				}
				rest := line[index+len(needle):]
				if rest == "" || (rest[0] != ':' && rest[0] != '-') {
					continue
				}
				digits := 1
				for digits < len(rest) && rest[digits] >= '0' && rest[digits] <= '9' {
					digits++
				}
				if digits == 1 {
					continue
				}
				number, ok := claimCountAllowZero(rest[1:digits])
				if !ok || number <= 0 {
					continue
				}
				if windowStart > 0 && (number < windowStart || number > windowEnd) {
					continue
				}
				return true
			}
		}
	}
	return false
}

// citationSourceObserved reports a source observation of the cited path. A
// structural coordinate or a manifest entry is not source content: it says the
// file exists, not what the file does.
func citationSourceObserved(turns []Turn, path string) bool {
	for _, turn := range turns {
		for _, outcome := range turn.Results {
			if outcome.Result.IsError || outcome.Refused() {
				continue
			}
			if !claimPathsMatch(outcomePath(outcome), path) {
				continue
			}
			if sourceObservationOutcome(outcome) {
				return true
			}
		}
	}
	return false
}
