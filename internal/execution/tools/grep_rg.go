package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

// Ripgrep backend bounds. The candidate tree walk already enforces the
// repository scope; these values bound only how that list is handed to the
// external scanner.
const (
	// ripgrepMaxArgBytes keeps one invocation well below a conservative
	// ARG_MAX, including the inherited environment and the pattern.
	ripgrepMaxArgBytes = 128 * 1024
	// ripgrepWorkers bounds concurrent rg processes. Ripgrep parallelises
	// internally; more than a handful of processes only adds startup cost.
	ripgrepWorkers = 4
	// ripgrepStderrBytes bounds the diagnostic retained from a genuine
	// failure. A version mismatch or a parse error is short.
	ripgrepStderrBytes = 4 * 1024
)

// ripgrepUnavailable reports a start failure that means "the tool is not
// usable", so the caller can run the native scanner. A nonzero search exit is
// deliberately excluded: that is a real failure with a reason to show.
func ripgrepUnavailable(err error) bool {
	return errors.Is(err, exec.ErrNotFound) ||
		errors.Is(err, fs.ErrNotExist) ||
		errors.Is(err, fs.ErrPermission)
}

// ripgrepQuery is the search contract shared by every batch.
type ripgrepQuery struct {
	pattern       string
	literal       bool
	caseSensitive bool
	perFile       int
}

// ripgrepPath resolves the optional scanner. A missing binary is a supported
// configuration, not a failure: the caller runs the native scanner instead.
func (t *GrepTool) ripgrepPath() string {
	look := t.LookPath
	if look == nil {
		look = LookPath
	}
	path, err := look("rg")
	if err != nil || strings.TrimSpace(path) == "" {
		return ""
	}
	return path
}

// searchRipgrep searches every candidate through rg and returns grouped
// matches. The files are already selected by the shared scope walk, so rg is
// invoked with --no-ignore and an explicit file list. That keeps membership
// authoritative in Go while still using rg's fast content scan.
func (t *GrepTool) searchRipgrep(ctx context.Context, rgPath string, candidates []grepCandidate, pattern string, caseSensitive bool, perFile int) ([]*grepFile, error) {
	batches := ripgrepBatches(t.Root, candidates)
	if len(batches) == 0 {
		return nil, nil
	}
	query := ripgrepQuery{
		pattern:       pattern,
		literal:       !hasRegexMeta(pattern),
		caseSensitive: caseSensitive,
		perFile:       perFile,
	}

	rgCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	workers := ripgrepWorkers
	if workers > len(batches) {
		workers = len(batches)
	}
	if workers > runtime.GOMAXPROCS(0) {
		workers = runtime.GOMAXPROCS(0)
	}
	if workers < 1 {
		workers = 1
	}

	var (
		mu        sync.Mutex
		results   []*grepFile
		collected int
		stopped   bool
		firstErr  error
	)
	jobs := make(chan []grepCandidate)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for batch := range jobs {
				mu.Lock()
				stop := stopped
				mu.Unlock()
				if stop || rgCtx.Err() != nil {
					return
				}
				files, err := runRipgrepBatch(rgCtx, t.Root, rgPath, batch, query)
				if rgCtx.Err() != nil {
					// Either this call hit the total cap or another batch
					// failed. The caller keeps only a successful full result.
					return
				}
				mu.Lock()
				if err != nil {
					if firstErr == nil {
						firstErr = err
					}
					stopped = true
					mu.Unlock()
					cancel()
					return
				}
				results = append(results, files...)
				for _, file := range files {
					collected += len(file.matches)
				}
				if collected >= GrepInternalTotalCap {
					stopped = true
					cancel()
				}
				mu.Unlock()
			}
		}()
	}

dispatch:
	for _, batch := range batches {
		mu.Lock()
		stop := stopped
		mu.Unlock()
		if stop || rgCtx.Err() != nil {
			break
		}
		select {
		case jobs <- batch:
		case <-rgCtx.Done():
			break dispatch
		}
	}
	close(jobs)
	wg.Wait()

	if firstErr != nil {
		return nil, firstErr
	}
	return results, nil
}

// ripgrepBatches splits candidates into invocations whose path arguments fit
// one command line. A single path larger than the budget still gets its own
// batch, because dropping it would create a silent false negative.
func ripgrepBatches(root string, candidates []grepCandidate) [][]grepCandidate {
	budget := ripgrepMaxArgBytes
	if runtime.GOOS == "windows" {
		// CreateProcess has a much smaller command-line limit than POSIX
		// ARG_MAX; the inherited environment shares the same budget.
		budget = 24 * 1024
	}
	var batches [][]grepCandidate
	var batch []grepCandidate
	size := 0
	for _, candidate := range candidates {
		relPath := rel(root, candidate.path)
		if relPath == "" || relPath == "." {
			continue
		}
		if len(batch) > 0 && size+len(relPath)+1 > budget {
			batches = append(batches, batch)
			batch = nil
			size = 0
		}
		batch = append(batch, candidate)
		size += len(relPath) + 1
	}
	if len(batch) > 0 {
		batches = append(batches, batch)
	}
	return batches
}

// runRipgrepBatch executes one rg invocation and decodes its JSON events.
//
// Exit code 1 means "no matches", which is a successful empty observation.
// Any other non-zero exit is a genuine failure: it carries stderr, so the
// model sees the parser or runtime reason instead of a fabricated empty tree.
func runRipgrepBatch(ctx context.Context, root, rgPath string, batch []grepCandidate, query ripgrepQuery) ([]*grepFile, error) {
	allowed := make(map[string]grepCandidate, len(batch))
	paths := make([]string, 0, len(batch))
	for _, candidate := range batch {
		relPath := rel(root, candidate.path)
		allowed[relPath] = candidate
		paths = append(paths, relPath)
	}

	cmd := exec.CommandContext(ctx, rgPath, ripgrepArgs(query, paths)...)
	cmd.Dir = root
	// A user config could change hidden, ignore, or binary behaviour under a
	// documented tool contract. An empty RIPGREP_CONFIG_PATH disables it.
	cmd.Env = ripgrepEnv()
	var stderr cappedWriter
	stderr.limit = ripgrepStderrBytes
	cmd.Stderr = &stderr

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("rg: open stdout: %w", err)
	}
	// Wait closes the pipe after the process exits, but a Start failure would
	// otherwise leak the read end.
	defer func() { _ = stdout.Close() }()
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("rg: start %q: %w", rgPath, err)
	}
	files, decodeErr := decodeRipgrep(stdout, allowed, query.perFile)
	if decodeErr != nil {
		// Stop the producer before Wait: a decoder that stopped early leaves
		// the pipe unread and rg would block on a full pipe.
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()

	// Cancellation is the cap or the caller's deadline. Partial results are
	// dropped; the caller reports the search as partial from its context.
	if ctx.Err() != nil {
		return nil, nil
	}
	if decodeErr != nil {
		return nil, fmt.Errorf("rg: decode output: %w", decodeErr)
	}
	if waitErr == nil {
		return files, nil
	}
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		switch exitErr.ExitCode() {
		case 0:
			return files, nil
		case 1:
			// Zero matches, not an execution failure.
			return nil, nil
		}
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = waitErr.Error()
		}
		return nil, fmt.Errorf("rg exited %d: %s", exitErr.ExitCode(), message)
	}
	return nil, fmt.Errorf("rg: %w", waitErr)
}

// ripgrepArgs builds the invocation. The candidate list is explicit, so every
// ignore decision was already made by the shared scope; --no-ignore keeps a
// user or repository rule from silently changing that decision again.
func ripgrepArgs(query ripgrepQuery, paths []string) []string {
	args := []string{
		"--json",
		"--no-ignore",
		"--no-follow",
		"--no-messages",
		"--line-number",
		"--no-heading",
		// One extra matching line is the truncation probe. The decoder emits
		// only perFile matches and marks the file capped when the extra line
		// arrives; asking for exactly perFile would erase the evidence that
		// more matches exist.
		"--max-count", strconv.Itoa(query.perFile + 1),
	}
	if query.literal {
		args = append(args, "--fixed-strings")
	}
	if query.caseSensitive {
		args = append(args, "--case-sensitive")
	} else {
		args = append(args, "--ignore-case")
	}
	args = append(args, "-e", query.pattern, "--")
	return append(args, paths...)
}

// ripgrepEnv returns the process environment with user configuration disabled.
func ripgrepEnv() []string {
	env := os.Environ()
	out := make([]string, 0, len(env)+1)
	for _, entry := range env {
		name, _, _ := strings.Cut(entry, "=")
		// The variable is case-insensitive on Windows; the child must not read
		// a user config under any spelling.
		if strings.EqualFold(name, "RIPGREP_CONFIG_PATH") {
			continue
		}
		out = append(out, entry)
	}
	return append(out, "RIPGREP_CONFIG_PATH=")
}

// ripgrepMessage is one JSON Lines event from `rg --json`.
type ripgrepMessage struct {
	Type string `json:"type"`
	Data struct {
		Path           ripgrepText `json:"path"`
		Lines          ripgrepText `json:"lines"`
		LineNumber     *int        `json:"line_number"`
		AbsoluteOffset *int64      `json:"absolute_offset"`
		BinaryOffset   *int64      `json:"binary_offset"`
	} `json:"data"`
}

// ripgrepText is rg's text-or-bytes encoding. A non-UTF-8 path arrives as
// base64 in bytes; decoding it keeps the path usable instead of dropping the
// match from a repository whose filenames are not valid UTF-8.
type ripgrepText struct {
	Text  string `json:"text"`
	Bytes string `json:"bytes"`
}

// value returns the decoded text.
func (t ripgrepText) value() string {
	if t.Text != "" {
		return t.Text
	}
	if t.Bytes == "" {
		return ""
	}
	decoded, err := base64.StdEncoding.DecodeString(t.Bytes)
	if err != nil {
		return ""
	}
	return string(decoded)
}

// decodeRipgrep groups matching events by file. A file whose end event reports
// a binary offset is discarded: rg may emit matches before the NUL byte, and a
// text search must not present those as source content.
func decodeRipgrep(reader io.Reader, allowed map[string]grepCandidate, perFile int) ([]*grepFile, error) {
	decoder := json.NewDecoder(reader)
	files := make(map[string]*grepFile)
	for {
		var message ripgrepMessage
		if err := decoder.Decode(&message); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return ripgrepFiles(files), err
		}
		switch message.Type {
		case "match":
			candidate, ok := allowed[normalizeRipgrepPath(message.Data.Path.value())]
			if !ok || message.Data.LineNumber == nil {
				continue
			}
			state := files[candidate.path]
			if state == nil {
				state = &grepFile{rel: candidate.path}
				files[candidate.path] = state
			}
			if message.Data.AbsoluteOffset != nil && *message.Data.AbsoluteOffset >= GrepMaxFileBytes {
				// Defensive: candidate partitioning already keeps windowed
				// files native. This keeps a stray event from reporting a
				// match the native contract would not have read.
				state.partial = true
				continue
			}
			if len(state.matches) >= perFile {
				state.truncated = true
				continue
			}
			line := strings.TrimRight(message.Data.Lines.value(), "\r\n")
			if strings.IndexByte(line, 0) >= 0 {
				delete(files, candidate.path)
				continue
			}
			if len(line) > GrepMaxLineLength {
				line = line[:GrepMaxLineLength] + "…"
			}
			state.matches = append(state.matches, grepMatch{
				rel:  candidate.path,
				line: *message.Data.LineNumber,
				text: line,
			})
		case "end":
			candidate, ok := allowed[normalizeRipgrepPath(message.Data.Path.value())]
			if !ok {
				continue
			}
			state := files[candidate.path]
			if state == nil {
				continue
			}
			if message.Data.BinaryOffset != nil {
				delete(files, candidate.path)
				continue
			}
			if len(state.matches) == 0 && !state.partial {
				delete(files, candidate.path)
			}
		}
	}
	return ripgrepFiles(files), nil
}

// normalizeRipgrepPath maps rg's printed path back to the root-relative key
// the batch supplied. rg prints an explicit argument verbatim except for a
// possible leading "./".
func normalizeRipgrepPath(path string) string {
	path = filepath.ToSlash(path)
	for strings.HasPrefix(path, "./") {
		path = strings.TrimPrefix(path, "./")
	}
	return path
}

// ripgrepFiles flattens the per-file accumulators.
func ripgrepFiles(files map[string]*grepFile) []*grepFile {
	out := make([]*grepFile, 0, len(files))
	for _, file := range files {
		if len(file.matches) > 0 || file.partial {
			out = append(out, file)
		}
	}
	return out
}

// cappedWriter retains at most limit bytes and reports the full write as
// accepted, so a child process never blocks on a diagnostic we do not need.
type cappedWriter struct {
	buf   []byte
	limit int
}

// Write implements io.Writer.
func (w *cappedWriter) Write(p []byte) (int, error) {
	n := len(p)
	if len(w.buf) < w.limit {
		remaining := w.limit - len(w.buf)
		if remaining > len(p) {
			remaining = len(p)
		}
		w.buf = append(w.buf, p[:remaining]...)
	}
	return n, nil
}

// String returns the retained diagnostic.
func (w *cappedWriter) String() string { return string(w.buf) }
