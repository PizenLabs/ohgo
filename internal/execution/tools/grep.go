package tools

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/PizenLabs/ohgo/internal/agent"
)

// Grep limits, mirroring the published contract of the original tool.
const (
	// GrepDefaultFileLimit is how many distinct files one response surfaces.
	// The agent paginates the rest with `skip`.
	GrepDefaultFileLimit = 20
	// GrepMaxFileLimit is the ceiling on the above.
	GrepMaxFileLimit = 100
	// GrepMultiFileMatches caps matches per file in a multi-file search, so one
	// hot file cannot crowd out diverse hits.
	GrepMultiFileMatches = 20
	// GrepSingleFileMatches caps matches when the scope is a single file,
	// where there is no diversity concern.
	GrepSingleFileMatches = 200
	// GrepInternalTotalCap bounds total fetched matches before grouping.
	GrepInternalTotalCap = 2000
	// GrepMaxFileBytes is the leading window searched per file. The original's
	// native grep reads a 4 MiB mmap window and drops the tail.
	GrepMaxFileBytes = 4 * 1024 * 1024
	// GrepTimeout bounds a search.
	GrepTimeout = 30 * time.Second
	// GrepMaxLineLength caps the width of one rendered match line.
	GrepMaxLineLength = 500
)

// GrepTool searches file contents by regular expression.
type GrepTool struct {
	Base
	// Root bounds every search.
	Root string
	// Timeout overrides GrepTimeout when non-zero.
	Timeout time.Duration
	// Workers overrides the scan parallelism. Zero picks a default from GOMAXPROCS.
	Workers int
	// LookPath resolves the optional ripgrep binary. Nil uses the shared
	// PATH-plus-standard-directory resolver.
	// A resolution failure selects the native scanner; it is not a call error.
	LookPath func(string) (string, error)
}

// NewGrepTool returns a grep tool rooted at root.
func NewGrepTool(root string) *GrepTool {
	tool := &GrepTool{Root: root}
	tool.Base = NewBase("grep",
		"Search file contents with a regular expression and return matching "+
			"lines grouped by file. This is exact or regex text lookup, not "+
			"structural search: use symbols for declarations, refs for "+
			"references and callers, structure for inventory and impact, "+
			"retrieve for relevance-ranked candidates, and investigate for call "+
			"chains. Do not run grep or rg through bash. The search honours the "+
			"shared repository scope and names ignored paths with the deciding "+
			"rule; ripgrep accelerates the scan when installed, with the same "+
			"limits and native fallback.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{
					"type":        "string",
					"description": "Regular expression to search for.",
				},
				"path": map[string]any{
					"type":        "string",
					"description": "File or directory to search, absolute or relative to the working directory.",
				},
				"case": map[string]any{
					"type":        "boolean",
					"description": "Case-sensitive matching. Defaults to true.",
				},
				"gitignore": map[string]any{
					"type":        "boolean",
					"description": "Honour .gitignore files. Defaults to true.",
				},
				"skip": map[string]any{
					"type":        "integer",
					"description": "Number of matching files to skip, for pagination.",
					"minimum":     0.0,
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Maximum number of matching files to return.",
					"minimum":     1.0,
					"maximum":     float64(GrepMaxFileLimit),
				},
				"context": map[string]any{
					"type":        "integer",
					"description": "Lines of context to show around each match.",
					"minimum":     0.0,
					"maximum":     10.0,
				},
			},
			"required": []string{"pattern"},
		},
		agent.Approval{Tier: agent.TierRead},
	)
	return tool
}

// grepMatch is one matching line.
type grepMatch struct {
	// rel is the Root-relative file path.
	rel string
	// line is the 1-based line number.
	line int
	// text is the matched line.
	text string
}

// grepFile is a file's matches plus metadata for grouping.
type grepFile struct {
	rel string
	// matches are in line order.
	matches []grepMatch
	// truncated is true when the per-file cap cut results short.
	truncated bool
	// partial is true when the search window or a scan error ended the file
	// before its end, so matches beyond that point were never examined.
	partial bool
}

// grepCandidate is one file selected by the shared scope walk.
//
// The size lets the caller keep a file that exceeds the search window on the
// native path: only that scanner can read the leading window and report the
// result as partial.
type grepCandidate struct {
	path string
	size int64
}

// Execute implements agent.Tool.
func (t *GrepTool) Execute(ctx agent.Context, args map[string]any) agent.Result {
	if err := checkCancelled(ctx.Ctx); err != nil {
		return agent.ErrorResult("%v", err)
	}

	pattern, err := strArg(args, "pattern", "")
	if err != nil {
		return agent.ErrorResult("%v", err)
	}
	if strings.TrimSpace(pattern) == "" {
		return agent.ErrorResult("pattern must not be empty")
	}
	caseSensitive, err := boolArg(args, "case", true)
	if err != nil {
		return agent.ErrorResult("%v", err)
	}
	useGitignore, err := boolArg(args, "gitignore", true)
	if err != nil {
		return agent.ErrorResult("%v", err)
	}
	skip, err := intArg(args, "skip", 0)
	if err != nil {
		return agent.ErrorResult("%v", err)
	}
	if skip < 0 {
		return agent.ErrorResult("skip must be >= 0, got %d", skip)
	}
	fileLimit, err := intArg(args, "limit", GrepDefaultFileLimit)
	if err != nil {
		return agent.ErrorResult("%v", err)
	}
	if fileLimit < 1 {
		fileLimit = GrepDefaultFileLimit
	}
	if fileLimit > GrepMaxFileLimit {
		fileLimit = GrepMaxFileLimit
	}

	matcher, err := newLineMatcher(pattern, caseSensitive)
	if err != nil {
		return agent.ErrorResult("invalid regular expression %q: %v", pattern, err)
	}

	// Resolve the search scope: a file narrows to that file, a directory walks.
	scope := t.Root
	singleFile := false
	var scopeSize int64
	var notes []string
	if raw, ok := args["path"]; ok && raw != nil {
		path, err := strArg(args, "path", "")
		if err != nil {
			return agent.ErrorResult("%v", err)
		}
		if strings.TrimSpace(path) != "" {
			resolved, err := resolvePath(t.Root, path)
			if err != nil {
				return agent.ErrorResult("resolve path: %v", err)
			}
			info, err := os.Stat(resolved)
			if err != nil {
				if os.IsNotExist(err) {
					return absenceResult("stat %q: %v", path, err)
				}
				return agent.ErrorResult("stat %q: %v", path, err)
			}
			scope = resolved
			singleFile = !info.IsDir()
			scopeSize = info.Size()
			if singleFile && scopeSize > GrepMaxFileBytes {
				notes = append(notes, fmt.Sprintf(
					"note: %s exceeds the %d MiB search window; matches past it are not shown",
					relativeToRoot(t.Root, scope), GrepMaxFileBytes/(1024*1024)))
			}
		}
	}

	timeout := t.Timeout
	if timeout <= 0 {
		timeout = GrepTimeout
	}
	searchCtx, cancel := context.WithTimeout(ctx.Ctx, timeout)
	defer cancel()

	perFile := GrepMultiFileMatches
	if singleFile {
		perFile = GrepSingleFileMatches
	}

	ignorer := newIgnorer(t.Root, useGitignore)
	if useGitignore {
		// An explicit target is not silently exempt from the scope. Asking
		// for an ignored directory is exactly the case where an empty result
		// used to send the model probing the same hidden path again; name the
		// rule and the one argument that changes the answer.
		scopeRel := rel(t.Root, scope)
		if scopeRel != "." && scopeRel != "" {
			if excluded, reason := ignorer.exclusion(scopeRel, !singleFile); excluded {
				return agent.Result{
					Text: fmt.Sprintf(
						"%s is excluded by .gitignore (%s); no file was searched. "+
							"Pass \"gitignore\": false to search it, or target a path outside the rule.",
						scopeRel, reason),
					Details: map[string]any{
						"files":         0,
						"pattern":       pattern,
						"ignored":       1,
						"ignored_paths": []map[string]any{{"path": scopeRel, "rule": reason}},
						"gitignore":     true,
					},
					Useless: false,
				}
			}
		}
	}

	var candidates []grepCandidate
	exclusions := &scanExclusions{}
	if singleFile {
		candidates = []grepCandidate{{path: scope, size: scopeSize}}
	} else {
		var walkErr error
		candidates, exclusions, walkErr = collectFiles(t.Root, scope, ignorer)
		if walkErr != nil {
			return agent.ErrorResult("scan %q: %v", relativeToRoot(t.Root, scope), walkErr)
		}
	}

	backend := "native"
	var scanned []*grepFile
	if rgPath := t.ripgrepPath(); rgPath != "" {
		small, large := partitionCandidates(candidates, GrepMaxFileBytes)
		rgUnavailable := false
		if len(small) > 0 {
			var err error
			scanned, err = t.searchRipgrep(searchCtx, rgPath, small, pattern, caseSensitive, perFile)
			if err != nil {
				// LookPath found a name, but the binary may have vanished or
				// be unexecutable between resolution and start. That is the
				// same absent-tool case, not a search failure; run the native
				// scanner over the whole candidate set so no small file is
				// omitted.
				if !ripgrepUnavailable(err) {
					return agent.ErrorResult("%v", err)
				}
				rgUnavailable = true
			} else {
				backend = "rg"
			}
		}
		if rgUnavailable {
			scanned = t.search(searchCtx, candidatePaths(candidates), matcher, perFile)
		} else if len(large) > 0 {
			// The window is a per-file read contract. Handing a huge file to
			// rg would search past it, so only the native scanner can report
			// the same partial result.
			scanned = append(scanned, t.search(searchCtx, candidatePaths(large), matcher, perFile)...)
			if backend == "rg" {
				backend = "rg+native"
			}
		}
	} else {
		scanned = t.search(searchCtx, candidatePaths(candidates), matcher, perFile)
	}
	if note := exclusions.ignoredNote(); note != "" {
		notes = append(notes, "note: "+note)
	}

	if err := checkCancelled(ctx.Ctx); err != nil {
		return agent.ErrorResult("%v", err)
	}
	if searchCtx.Err() != nil {
		notes = append(notes, fmt.Sprintf("note: search stopped after %s; results are partial", timeout))
	}
	withMatches := make([]*grepFile, 0)
	windowed := 0
	for _, file := range scanned {
		if file.partial && !singleFile {
			windowed++
		}
		if len(file.matches) > 0 {
			withMatches = append(withMatches, file)
		}
	}
	if windowed > 0 {
		notes = append(notes, fmt.Sprintf(
			"note: %d file(s) exceed the %d MiB search window; matches past it are not shown",
			windowed, GrepMaxFileBytes/(1024*1024)))
	}
	sortGrepFiles(withMatches)

	totalFiles := len(withMatches)
	if totalFiles == 0 {
		text := "No matches found."
		if len(notes) > 0 {
			text += "\n\n" + strings.Join(notes, "\n")
		}
		details := map[string]any{
			"files":     0,
			"pattern":   pattern,
			"ignored":   exclusions.ignored,
			"excluded":  exclusions.builtin,
			"gitignore": useGitignore,
			"backend":   backend,
		}
		if paths := exclusions.ignoredDetails(); paths != nil {
			details["ignored_paths"] = paths
		}
		return agent.Result{
			Text:    text,
			Details: details,
			Useless: len(notes) == 0,
		}
	}

	start := skip
	if start > len(withMatches) {
		start = len(withMatches)
	}
	window := withMatches[start:]
	more := len(window) > fileLimit
	if more {
		window = window[:fileLimit]
	}

	var sb strings.Builder
	for _, file := range window {
		sb.WriteString(relativeToRoot(t.Root, file.rel))
		sb.WriteByte(':')
		sb.WriteByte('\n')
		for _, match := range file.matches {
			text := match.text
			if len(text) > GrepMaxLineLength {
				text = text[:GrepMaxLineLength] + "…"
			}
			fmt.Fprintf(&sb, "  %d: %s\n", match.line, text)
		}
		if file.truncated {
			fmt.Fprintf(&sb, "  … more matches in this file, capped at %d\n", perFile)
		}
		sb.WriteByte('\n')
	}

	summary := fmt.Sprintf("%d file(s) matched", totalFiles)
	if skip > 0 || more {
		summary += fmt.Sprintf(", showing %d starting at %d", len(window), skip)
	}
	if more {
		summary += fmt.Sprintf(" — pass skip=%d for the next page", skip+len(window))
	}

	text := strings.TrimRight(sb.String(), "\n")
	for _, note := range notes {
		text += "\n\n" + note
	}
	text += "\n\n" + summary

	details := map[string]any{
		"files":     totalFiles,
		"pattern":   pattern,
		"shown":     len(window),
		"ignored":   exclusions.ignored,
		"excluded":  exclusions.builtin,
		"gitignore": useGitignore,
		"backend":   backend,
	}
	if paths := exclusions.ignoredDetails(); paths != nil {
		details["ignored_paths"] = paths
	}
	return agent.Result{
		Text:    text,
		Details: details,
	}
}

// collectFiles gathers candidate files under scope, pruning ignored and hidden
// directories and recording the scope decisions that removed paths.
//
// Pruning a directory requires evaluating ignore rules against the directory
// name itself, so gitignore rules that name a directory exclude it here. The
// same ignoreScope the structural index uses decides membership, including the
// nested-repository boundary, so discovery cannot disagree with structure
// about whether a path belongs to the workspace.
func collectFiles(root, scope string, ignorer *ignorer) ([]grepCandidate, *scanExclusions, error) {
	var files []grepCandidate
	exclusions := &scanExclusions{}
	err := filepath.WalkDir(scope, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			// A permission error or a vanished file is skipped; the rest of
			// the tree is still worth searching.
			if path == scope {
				return err
			}
			return nil
		}
		if entry.IsDir() {
			if path == scope {
				return nil
			}
			name := entry.Name()
			if isBuiltInExclude(name) || name == ".git" {
				exclusions.addBuiltin()
				return filepath.SkipDir
			}
			if isHidden(name) {
				return filepath.SkipDir
			}
			relPath := rel(root, path)
			if ignorer.ignored(relPath, true) {
				exclusions.addIgnored(relPath, ignorer.explain(relPath, true))
				return filepath.SkipDir
			}
			return nil
		}
		relPath := rel(root, path)
		if ignorer.ignored(relPath, false) {
			exclusions.addIgnored(relPath, ignorer.explain(relPath, false))
			return nil
		}
		// Only regular files can be searched; a symlink or device would block
		// the scanner or return nonsense.
		if !entry.Type().IsRegular() {
			return nil
		}
		// A transient stat failure is left to the backend, which reports no
		// match for an unreadable file rather than turning the whole scan into
		// an error.
		size := int64(-1)
		if info, err := entry.Info(); err == nil {
			size = info.Size()
		}
		files = append(files, grepCandidate{path: path, size: size})
		return nil
	})
	return files, exclusions, err
}

// candidatePaths extracts the filesystem paths a native scan needs.
func candidatePaths(candidates []grepCandidate) []string {
	paths := make([]string, len(candidates))
	for i, candidate := range candidates {
		paths[i] = candidate.path
	}
	return paths
}

// partitionCandidates splits files at the native search window. A file larger
// than the window stays native so its partial result is reported honestly.
func partitionCandidates(candidates []grepCandidate, window int64) (small, large []grepCandidate) {
	for _, candidate := range candidates {
		if candidate.size > window {
			large = append(large, candidate)
			continue
		}
		small = append(small, candidate)
	}
	return small, large
}

func rel(root, path string) string {
	if root == path {
		return "."
	}
	out, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(out)
}

// search scans files in parallel, bounded by a worker pool.
//
// It stops dispatching once the internal cap is reached, because the caller
// only needs the file window plus enough headroom to report a total count.
// Results are collected under a mutex and re-sorted by the caller, so worker
// completion order never reaches the output.
func (t *GrepTool) search(ctx context.Context, files []string, matcher *lineMatcher, perFile int) []*grepFile {
	workers := t.Workers
	if workers <= 0 {
		workers = runtime.GOMAXPROCS(0)
	}
	if workers > len(files) {
		workers = len(files)
	}
	if workers < 1 {
		workers = 1
	}

	var (
		mu        sync.Mutex
		results   []*grepFile
		collected int
		done      bool
	)

	jobs := make(chan string)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for path := range jobs {
				mu.Lock()
				stopped := done
				mu.Unlock()
				if stopped {
					return
				}
				file := t.scanFile(ctx, path, matcher, perFile)
				if file == nil {
					continue
				}
				mu.Lock()
				results = append(results, file)
				collected += len(file.matches)
				done = collected >= GrepInternalTotalCap
				mu.Unlock()
			}
		}()
	}

dispatch:
	for _, path := range files {
		mu.Lock()
		stop := done
		mu.Unlock()
		if stop || ctx.Err() != nil {
			break dispatch
		}
		select {
		case jobs <- path:
		case <-ctx.Done():
			mu.Lock()
			done = true
			mu.Unlock()
			break dispatch
		}
	}
	close(jobs)
	wg.Wait()
	return results
}

// scanFile searches one file. A nil return means no matches.
//
// The file is read through a buffered scanner capped at the per-file match
// budget, so a pathological file (a minified bundle, a lockfile) cannot exhaust
// memory even though it may match thousands of times.
func (t *GrepTool) scanFile(ctx context.Context, path string, matcher *lineMatcher, perFile int) *grepFile {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = file.Close() }()

	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil
	}

	// A larger file is read only up to the window, matching the original's
	// mmap behaviour. Reading the tail would change which matches appear.
	reader := bufio.NewReaderSize(file, 64*1024)
	if info.Size() > GrepMaxFileBytes {
		reader = bufio.NewReaderSize(&ioLimitReader{file, GrepMaxFileBytes}, 64*1024)
	}

	// A line may be as long as the window itself. A smaller cap would end the
	// scan at the first minified or generated line and silently drop the rest
	// of the file.
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), GrepMaxFileBytes)

	out := &grepFile{rel: path}
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Bytes()
		// A NUL byte marks a binary stream. Ripgrep's default mode discards
		// such a file, and a text search that returned matches from an object
		// file or image would be a false source observation. Checking the line
		// the scanner already produced costs one pass and needs no second read.
		if bytes.IndexByte(line, 0) >= 0 {
			return nil
		}
		if !matcher.matchBytes(line) {
			// Cancellation is checked per line so a huge scan aborts within a
			// bounded number of matches rather than at the end.
			if lineNum%4096 == 0 && ctx.Err() != nil {
				return nil
			}
			continue
		}
		if len(out.matches) >= perFile {
			out.truncated = true
			break
		}
		text := strings.TrimRight(scanner.Text(), "\r")
		if len(text) > GrepMaxLineLength {
			text = text[:GrepMaxLineLength] + "…"
		}
		out.matches = append(out.matches, grepMatch{rel: path, line: lineNum, text: text})
	}
	out.partial = info.Size() > GrepMaxFileBytes || scanner.Err() != nil
	if len(out.matches) == 0 && !out.partial {
		return nil
	}
	return out
}

// ioLimitReader reads at most n bytes from a file starting at offset zero.
type ioLimitReader struct {
	file *os.File
	left int64
}

// Read implements io.Reader.
func (r *ioLimitReader) Read(p []byte) (int, error) {
	if r.left <= 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > r.left {
		p = p[:r.left]
	}
	n, err := r.file.Read(p)
	r.left -= int64(n)
	return n, err
}

// sortGrepFiles orders files by path so output is deterministic.
func sortGrepFiles(files []*grepFile) {
	for i := 1; i < len(files); i++ {
		for j := i; j > 0 && files[j].rel < files[j-1].rel; j-- {
			files[j], files[j-1] = files[j-1], files[j]
		}
	}
}

// lineMatcher reports whether a line matches the search pattern.
//
// A literal pattern takes a substring path. This is not a micro-optimisation:
// searching a symbol name is the overwhelmingly common query, and regexp
// matching a pattern with no metacharacters is roughly an order of magnitude
// slower than a byte scan. BenchmarkGrep below is the guard for that claim.
//
// Case-insensitive matching still goes through regexp, because Go's (?i)
// performs Unicode case folding that a hand-rolled ASCII fold would silently
// differ from.
type lineMatcher struct {
	// re is nil when the pattern is a plain literal.
	re *regexp.Regexp
	// literal holds the lowercased-free raw bytes of a literal pattern.
	literal []byte
}

// newLineMatcher builds a matcher, returning an error for an invalid regex.
func newLineMatcher(pattern string, caseSensitive bool) (*lineMatcher, error) {
	// Only a case-sensitive literal can take the fast path. A case-insensitive
	// match still needs the regexp engine, whose (?i) folding is Unicode-aware
	// where an ASCII fold would differ.
	if caseSensitive && !hasRegexMeta(pattern) {
		return &lineMatcher{literal: []byte(pattern)}, nil
	}
	effective := pattern
	if !caseSensitive {
		// Go has no case-insensitive compile flag, so fold it into the pattern.
		effective = "(?i)" + pattern
	}
	re, err := regexp.Compile(effective)
	if err != nil {
		return nil, err
	}
	return &lineMatcher{re: re}, nil
}

// matchBytes reports whether the line matches.
func (m *lineMatcher) matchBytes(line []byte) bool {
	if m.re != nil {
		return m.re.Match(line)
	}
	return bytes.Contains(line, m.literal)
}

// hasRegexMeta reports whether a pattern contains regex metacharacters.
//
// A pattern without them matches literally, which is what a fast path checks.
// Notably "." is counted: users typing "file.go" expect the dot to match a
// literal dot more often than they expect it to be a wildcard, and a regex
// path preserves the old behaviour exactly.
func hasRegexMeta(pattern string) bool {
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '\\', '^', '$', '.', '|', '?', '*', '+', '(', ')', '[', ']', '{', '}':
			return true
		}
	}
	return false
}
