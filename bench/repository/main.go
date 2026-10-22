package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/PizenLabs/ohgo/ai"
	"github.com/PizenLabs/ohgo/ast"
)

const harnessVersion = "phase2-v1"

type metadata struct {
	BenchmarkRevision  string    `json:"benchmark_revision"`
	HarnessVersion     string    `json:"harness_version"`
	TaskHash           string    `json:"task_hash"`
	StartedAt          time.Time `json:"started_at"`
	FinishedAt         time.Time `json:"finished_at"`
	Repository         string    `json:"repository"`
	RepositoryRevision string    `json:"repository_revision"`
	FixtureFiles       int       `json:"fixture_files"`
	FixtureGoFiles     int       `json:"fixture_go_files"`
	Model              string    `json:"model"`
	Provider           string    `json:"provider"`
	API                string    `json:"api"`
	BaseURL            string    `json:"base_url"`
	Temperature        float64   `json:"temperature"`
	MaxTurns           int       `json:"max_turns"`
	MaxTokens          int       `json:"max_tokens"`
	TimeoutSeconds     int       `json:"timeout_seconds"`
	Concurrency        int       `json:"concurrency"`
	Repeats            int       `json:"repeats"`
	Tracks             []string  `json:"tracks"`
	ExcludedPaths      []string  `json:"excluded_paths"`
}

type benchmark struct {
	Metadata metadata `json:"metadata"`
	Tasks    []Task   `json:"tasks"`
	Runs     []Run    `json:"runs"`
}

func main() {
	repo := flag.String("repo", ".", "source repository to snapshot")
	rev := flag.String("rev", "53bc07b", "git revision to benchmark; empty uses the working tree")
	out := flag.String("out", "docs/benchmarks/results/phase2-baseline.json", "result JSON path")
	modelID := flag.String("model", "cohere/north-mini-code:free", "provider-scoped model id")
	provider := flag.String("provider", "openrouter", "provider key")
	apiName := flag.String("api", string(ai.APIOpenAIChatCompletions), "wire API")
	baseURL := flag.String("base-url", "https://openrouter.ai/api/v1", "provider base URL")
	tracksFlag := flag.String("tracks", "A,B,C", "comma-separated track letters")
	tasksFlag := flag.String("tasks", "", "comma-separated task ids (default: all)")
	repeats := flag.Int("repeats", 1, "repetitions per task x track")
	maxTurns := flag.Int("max-turns", 16, "turn budget per run")
	maxTokens := flag.Int("max-tokens", 2048, "completion budget per request")
	timeout := flag.Duration("timeout", 240*time.Second, "wall-clock budget per run")
	concurrency := flag.Int("concurrency", 1, "parallel independent runs")
	temperature := flag.Float64("temperature", 0, "sampling temperature")
	retries := flag.Int("retries", 5, "stream open retries for transient provider errors")
	minInterval := flag.Duration("min-interval", 0, "minimum delay between provider requests (rate-limit pacing)")
	dryRun := flag.Bool("dry-run", false, "validate fixture and ground truth without model calls")
	static := flag.Bool("static", false, "run deterministic tool-level probes for every task")
	staticOut := flag.String("static-out", "docs/benchmarks/results/phase2-static.json", "tool-level probe output path")
	rescore := flag.Bool("rescore", false, "re-parse final answers in an existing result file without model calls")
	rescoreOut := flag.String("rescore-out", "", "rescored result path (default: overwrite the input)")
	keep := flag.Bool("keep", false, "keep the temporary fixture")
	flag.Parse()

	if *concurrency < 1 {
		*concurrency = 1
	}
	if *repeats < 1 {
		*repeats = 1
	}

	if *rescore {
		target := *rescoreOut
		if target == "" {
			target = *out
		}
		if err := rescoreFile(*out, target); err != nil {
			fatal("rescore: %v", err)
		}
		return
	}

	// Resolve the source revision before copying. An unknown revision must
	// fail loudly: a benchmark that silently measures a different tree is
	// worse than no benchmark.
	repoAbs, err := filepath.Abs(*repo)
	if err != nil {
		fatal("resolve repository: %v", err)
	}
	revision := *rev
	if revision != "" {
		full, err := gitOutput(repoAbs, "rev-parse", revision)
		if err != nil {
			fatal("resolve revision %q: %v", revision, err)
		}
		revision = strings.TrimSpace(full)
	}
	if revision == "" {
		revision = "working-tree"
	}

	fixture, cleanup, err := prepareFixture(repoAbs, *rev)
	if err != nil {
		fatal("prepare fixture: %v", err)
	}
	if !*keep {
		defer cleanup()
	} else {
		fmt.Fprintf(os.Stderr, "fixture kept at %s\n", fixture)
	}

	tasks := phase2Tasks()
	if err := validateTasks(fixture, tasks); err != nil {
		fatal("ground-truth validation: %v", err)
	}
	files, goFiles, err := countFixture(fixture)
	if err != nil {
		fatal("count fixture: %v", err)
	}

	selectedTasks, err := selectTasks(tasks, *tasksFlag)
	if err != nil {
		fatal("%v", err)
	}
	selectedTracks, err := selectTracks(*tracksFlag)
	if err != nil {
		fatal("%v", err)
	}

	taskHash, err := hashTasks(tasks)
	if err != nil {
		fatal("hash tasks: %v", err)
	}

	fmt.Fprintf(os.Stderr, "fixture: %s\n", fixture)
	fmt.Fprintf(os.Stderr, "repository: %s @ %s\n", repoAbs, revision)
	fmt.Fprintf(os.Stderr, "fixture files: %d (%d Go)\n", files, goFiles)
	fmt.Fprintf(os.Stderr, "tasks: %d, tracks: %d, runs: %d\n",
		len(selectedTasks), len(selectedTracks), len(selectedTasks)*len(selectedTracks)*(*repeats))

	if *dryRun {
		fmt.Fprintln(os.Stderr, "dry run: fixture and ground truth validated; no model calls made")
		for _, task := range selectedTasks {
			fmt.Printf("%-42s %-16s targets=%d status=%s\n", task.ID, task.Category, len(task.Targets), task.ExpectedStatus)
		}
		return
	}

	if *static {
		result, err := runStatic(fixture, selectedTasks)
		if err != nil {
			fatal("static probe: %v", err)
		}
		result.TaskHash = taskHash
		if err := writeJSON(*staticOut, result); err != nil {
			fatal("write static results: %v", err)
		}
		summarizeStatic(result.Probes)
		fmt.Printf("wrote %s (%d probes)\n", *staticOut, len(result.Probes))
		return
	}

	registry := ai.NewRegistry()
	if _, err := registry.ResolveAPIKey(*provider); err != nil {
		fatal("credential for %s: %v", *provider, err)
	}
	model := ai.Model{
		ID:              *modelID,
		Name:            *modelID,
		Provider:        *provider,
		API:             ai.API(*apiName),
		BaseURL:         *baseURL,
		ContextWindow:   1_000_000,
		MaxOutputTokens: *maxTokens,
	}
	baseStream := registry.StreamFn()
	stream := func(ctx context.Context, req ai.Request) (*ai.Stream, error) {
		value := *temperature
		req.Temperature = &value
		return baseStream(ctx, req)
	}
	stream = retryStream(stream, *retries)
	if *minInterval > 0 {
		stream = pacedStream(stream, *minInterval)
	}

	cfg := RunConfig{
		Model:       model,
		Stream:      stream,
		MaxTokens:   *maxTokens,
		MaxTurns:    *maxTurns,
		Temperature: *temperature,
		Timeout:     *timeout,
	}

	started := time.Now().UTC()
	fmt.Fprintf(os.Stderr, "model: %s/%s (%s)\n", *provider, *modelID, *baseURL)

	type job struct {
		taskIndex  int
		trackIndex int
		repeat     int
	}
	jobs := make([]job, 0, len(selectedTasks)*len(selectedTracks)*(*repeats))
	for repeat := 0; repeat < *repeats; repeat++ {
		for ti := range selectedTasks {
			for si := range selectedTracks {
				jobs = append(jobs, job{taskIndex: ti, trackIndex: si, repeat: repeat})
			}
		}
	}

	runs := make([]Run, len(jobs))
	work := make(chan job)
	var wg sync.WaitGroup
	for worker := 0; worker < *concurrency; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range work {
				task := selectedTasks[item.taskIndex]
				track := selectedTracks[item.trackIndex]
				fmt.Fprintf(os.Stderr, "run %-42s %-13s\n", task.ID, track.Name)
				run := RunTask(context.Background(), fixture, task, track, cfg)
				run.Repeat = item.repeat
				runs[jobKey(item.taskIndex, item.trackIndex, item.repeat, len(selectedTasks), len(selectedTracks))] = run
				if run.Success {
					fmt.Fprintf(os.Stderr, "  ok   %-20s %s\n", run.Outcome, run.StatusReported)
				} else {
					fmt.Fprintf(os.Stderr, "  fail %-20s %s\n", run.Outcome, run.StatusReported)
				}
			}
		}()
	}
	for _, item := range jobs {
		work <- item
	}
	close(work)
	wg.Wait()

	sort.SliceStable(runs, func(i, j int) bool {
		if runs[i].TaskID != runs[j].TaskID {
			return runs[i].TaskID < runs[j].TaskID
		}
		if runs[i].Track != runs[j].Track {
			return runs[i].Track < runs[j].Track
		}
		return runs[i].Repeat < runs[j].Repeat
	})

	result := benchmark{
		Metadata: metadata{
			BenchmarkRevision:  "phase2-baseline",
			HarnessVersion:     harnessVersion,
			TaskHash:           taskHash,
			StartedAt:          started,
			FinishedAt:         time.Now().UTC(),
			Repository:         repoAbs,
			RepositoryRevision: revision,
			FixtureFiles:       files,
			FixtureGoFiles:     goFiles,
			Model:              *modelID,
			Provider:           *provider,
			API:                *apiName,
			BaseURL:            *baseURL,
			Temperature:        *temperature,
			MaxTurns:           *maxTurns,
			MaxTokens:          *maxTokens,
			TimeoutSeconds:     int(timeout.Seconds()),
			Concurrency:        *concurrency,
			Repeats:            *repeats,
			Tracks:             trackNames(selectedTracks),
			ExcludedPaths:      excludedPaths(),
		},
		Tasks: selectedTasks,
		Runs:  runs,
	}
	if err := writeJSON(*out, result); err != nil {
		fatal("write results: %v", err)
	}
	fmt.Printf("wrote %s (%d runs)\n", *out, len(runs))
	printSummary(runs, selectedTracks)
}

func jobKey(taskIndex, trackIndex, repeat, taskCount, trackCount int) int {
	return (repeat*taskCount+taskIndex)*trackCount + trackIndex
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "bench: "+format+"\n", args...)
	os.Exit(1)
}

// prepareFixture snapshots a revision into a temporary directory and removes
// evaluator-visible material that names the answers.
func prepareFixture(repo, rev string) (string, func(), error) {
	dir, err := os.MkdirTemp("", "ohgo-phase2-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }

	if rev != "" {
		if err := gitArchive(repo, rev, dir); err != nil {
			cleanup()
			return "", nil, err
		}
	} else {
		if err := copyWorkingTree(repo, dir); err != nil {
			cleanup()
			return "", nil, err
		}
	}
	if err := sanitizeFixture(dir); err != nil {
		cleanup()
		return "", nil, err
	}
	return dir, cleanup, nil
}

func gitArchive(repo, rev, dst string) error {
	gitCmd := exec.Command("git", "-C", repo, "archive", "--format=tar", rev)
	tarCmd := exec.Command("tar", "-x", "-C", dst)
	var stderr bytes.Buffer
	gitCmd.Stderr = &stderr
	pipe, err := gitCmd.StdoutPipe()
	if err != nil {
		return err
	}
	tarCmd.Stdin = pipe
	tarCmd.Stderr = &stderr
	if err := tarCmd.Start(); err != nil {
		return err
	}
	if err := gitCmd.Start(); err != nil {
		_ = tarCmd.Wait()
		return err
	}
	gitErr := gitCmd.Wait()
	tarErr := tarCmd.Wait()
	if gitErr != nil {
		return fmt.Errorf("git archive: %w: %s", gitErr, strings.TrimSpace(stderr.String()))
	}
	if tarErr != nil {
		return fmt.Errorf("extract archive: %w: %s", tarErr, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func copyWorkingTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil || rel == "." {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "node_modules", "dist", ".ohgo":
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if entry.Name() == "ohgo" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}

// excludedPaths removes benchmark reports and agent instructions that name
// evaluation targets. The architecture documentation is retained because one
// task deliberately asks for a line in it.
func excludedPaths() []string {
	return []string{
		"docs/report",
		"docs/benchmarks",
		"AGENTS.md",
		"MEMORY.md",
		"PROGRESS.md",
		"PORT.md",
		"README.md",
	}
}

func sanitizeFixture(root string) error {
	for _, rel := range excludedPaths() {
		if err := os.RemoveAll(filepath.Join(root, rel)); err != nil {
			return err
		}
	}
	return nil
}

func countFixture(root string) (int, int, error) {
	total, goFiles := 0, 0
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		total++
		if strings.HasSuffix(entry.Name(), ".go") {
			goFiles++
		}
		return nil
	})
	return total, goFiles, err
}

func validateTasks(root string, tasks []Task) error {
	idx, err := ast.NewIndex(root)
	if err != nil {
		return err
	}
	for _, task := range tasks {
		if task.ExpectedStatus == "not_found" {
			if len(task.NegativeNames) == 0 {
				return fmt.Errorf("%s: negative task has no NegativeNames", task.ID)
			}
			for _, name := range task.NegativeNames {
				matches, err := idx.Lookup(name)
				if err != nil {
					return fmt.Errorf("%s: lookup %q: %w", task.ID, name, err)
				}
				if len(matches) != 0 {
					return fmt.Errorf("%s: negative symbol %q exists at %s:%d", task.ID, name, matches[0].Path, matches[0].Line)
				}
			}
			continue
		}
		if len(task.Targets) == 0 {
			return fmt.Errorf("%s: positive task has no targets", task.ID)
		}
		for _, target := range task.Targets {
			if target.Symbol == "" {
				if err := validateTextTarget(root, target); err != nil {
					return fmt.Errorf("%s: %w", task.ID, err)
				}
				continue
			}
			matches, err := idx.Lookup(target.Symbol)
			if err != nil {
				return fmt.Errorf("%s: lookup %q: %w", task.ID, target.Symbol, err)
			}
			found := false
			for _, match := range matches {
				if match.Path == target.Path && match.Line == target.StartLine && match.EndLine == target.EndLine {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("%s: %s:%d-%d %s not found (matches: %v)",
					task.ID, target.Path, target.StartLine, target.EndLine, target.Symbol, matchSummary(matches))
			}
		}
	}
	return nil
}

func validateTextTarget(root string, target Target) error {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(target.Path)))
	if err != nil {
		return fmt.Errorf("read %s: %w", target.Path, err)
	}
	lines := strings.Split(string(data), "\n")
	if target.StartLine < 1 || target.StartLine > len(lines) {
		return fmt.Errorf("%s line %d out of range", target.Path, target.StartLine)
	}
	if target.Contains != "" && !strings.Contains(lines[target.StartLine-1], target.Contains) {
		return fmt.Errorf("%s:%d does not contain %q", target.Path, target.StartLine, target.Contains)
	}
	return nil
}

func matchSummary(matches []ast.Match) string {
	parts := make([]string, 0, len(matches))
	for _, match := range matches {
		parts = append(parts, fmt.Sprintf("%s:%d-%d", match.Path, match.Line, match.EndLine))
	}
	return strings.Join(parts, ", ")
}

func selectTasks(tasks []Task, raw string) ([]Task, error) {
	if strings.TrimSpace(raw) == "" {
		return tasks, nil
	}
	wanted := map[string]bool{}
	for _, id := range strings.Split(raw, ",") {
		wanted[strings.TrimSpace(id)] = true
	}
	var out []Task
	for _, task := range tasks {
		if wanted[task.ID] {
			out = append(out, task)
			delete(wanted, task.ID)
		}
	}
	if len(wanted) > 0 {
		var missing []string
		for id := range wanted {
			missing = append(missing, id)
		}
		sort.Strings(missing)
		return nil, fmt.Errorf("unknown task ids: %s", strings.Join(missing, ", "))
	}
	return out, nil
}

func selectTracks(raw string) ([]Track, error) {
	all := Tracks()
	if strings.TrimSpace(raw) == "" {
		return all, nil
	}
	wanted := map[string]bool{}
	for _, name := range strings.Split(raw, ",") {
		name = strings.ToUpper(strings.TrimSpace(name))
		wanted[name] = true
	}
	var out []Track
	for _, track := range all {
		if wanted[strings.ToUpper(track.Name[:1])] {
			out = append(out, track)
			delete(wanted, strings.ToUpper(track.Name[:1]))
		}
	}
	if len(wanted) > 0 {
		var missing []string
		for name := range wanted {
			missing = append(missing, name)
		}
		sort.Strings(missing)
		return nil, fmt.Errorf("unknown tracks: %s", strings.Join(missing, ", "))
	}
	return out, nil
}

func trackNames(tracks []Track) []string {
	out := make([]string, 0, len(tracks))
	for _, track := range tracks {
		out = append(out, track.Name)
	}
	return out
}

func hashTasks(tasks []Task) (string, error) {
	data, err := json.Marshal(tasks)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func gitOutput(repo string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// rescoreFile re-evaluates stored final answers. It exists because an
// evaluator bug must not force a model rerun: the model's text is evidence,
// and a parser fix should be separable from a discovery result.
func rescoreFile(input, output string) error {
	data, err := os.ReadFile(input)
	if err != nil {
		return err
	}
	var file benchmark
	if err := json.Unmarshal(data, &file); err != nil {
		return err
	}
	tasks := map[string]Task{}
	for _, task := range file.Tasks {
		tasks[task.ID] = task
	}
	changed := 0
	for i := range file.Runs {
		run := &file.Runs[i]
		task, ok := tasks[run.TaskID]
		if !ok {
			return fmt.Errorf("run %d: unknown task %q", i, run.TaskID)
		}
		if run.Error != "" {
			run.Outcome = "provider_error"
			run.Success = false
			continue
		}
		answer, format := parseFinalAnswer(run.FinalText, "")
		score := scoreAnswer(task, answer, "")
		if run.Success != score.success || run.Outcome != score.outcome || run.StatusReported != answer.Status {
			changed++
		}
		run.AnswerFormat = format
		run.StatusReported = answer.Status
		run.TargetsReported = answer.Targets
		run.Success = score.success
		run.Outcome = score.outcome
		run.MatchedTargets = score.matched
		run.MissingTargets = score.missing
		run.ExtraTargets = score.extra
		run.LineExact = score.lineExact
	}
	file.Metadata.BenchmarkRevision = file.Metadata.BenchmarkRevision + "+rescored"
	if err := writeJSON(output, file); err != nil {
		return err
	}
	fmt.Printf("rescored %d runs, %d changed -> %s\n", len(file.Runs), changed, output)
	return nil
}

// retryStream absorbs provider-side throttling and in-flight credit
// reservations. It is benchmark-only: production Ohgo deliberately surfaces
// transport errors to the caller, but a benchmark that records an infrastructure
// failure as a discovery failure would be dishonest.
func retryStream(base ai.StreamFn, attempts int) ai.StreamFn {
	if attempts < 1 {
		attempts = 1
	}
	return func(ctx context.Context, req ai.Request) (*ai.Stream, error) {
		var last error
		for attempt := 0; attempt < attempts; attempt++ {
			stream, err := base(ctx, req)
			if err == nil {
				return stream, nil
			}
			last = err
			if !retryableProviderError(err) {
				return nil, err
			}
			wait := time.Duration(attempt+1) * 2 * time.Second
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
		}
		return nil, last
	}
}

func retryableProviderError(err error) bool {
	message := strings.ToLower(err.Error())
	for _, marker := range []string{
		"http 402", "http 429", "http 500", "http 502", "http 503", "http 504",
		"in-flight", "rate limit", "temporarily", "timeout", "connection reset",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

// pacedStream serializes request opens and enforces a minimum interval. It is
// benchmark-only: free provider tiers publish requests-per-minute limits, and
// a burst of tool follow-ups would otherwise be recorded as a capability
// failure.
func pacedStream(base ai.StreamFn, interval time.Duration) ai.StreamFn {
	var (
		mu   sync.Mutex
		last time.Time
	)
	return func(ctx context.Context, req ai.Request) (*ai.Stream, error) {
		mu.Lock()
		if !last.IsZero() {
			if wait := interval - time.Since(last); wait > 0 {
				select {
				case <-ctx.Done():
					mu.Unlock()
					return nil, ctx.Err()
				case <-time.After(wait):
				}
			}
		}
		last = time.Now()
		mu.Unlock()
		return base(ctx, req)
	}
}

func printSummary(runs []Run, tracks []Track) {
	fmt.Println()
	fmt.Printf("%-13s %5s %5s %7s %7s %8s %9s %10s %9s %10s\n",
		"track", "runs", "ok", "ok%", "turns", "calls", "cand", "opened", "ctxKB", "durMS")
	for _, track := range tracks {
		var subset []Run
		for _, run := range runs {
			if run.Track == track.Name {
				subset = append(subset, run)
			}
		}
		ok := 0
		var turns, calls, cands, opened, bytes, duration []int
		for _, run := range subset {
			if run.Success {
				ok++
			}
			turns = append(turns, run.Turns)
			calls = append(calls, run.ToolCalls)
			cands = append(cands, run.CandidateFiles)
			opened = append(opened, run.FilesOpened)
			bytes = append(bytes, run.ContextBytes)
			duration = append(duration, int(run.DurationMS))
		}
		rate := 0.0
		if len(subset) > 0 {
			rate = float64(ok) * 100 / float64(len(subset))
		}
		fmt.Printf("%-13s %5d %5d %6.1f%% %7.1f %7.1f %8.1f %10.1f %9.1f %10.0f\n",
			track.Name, len(subset), ok, rate,
			median(turns), median(calls), median(cands), median(opened), median(bytes)/1024, median(duration))
	}
}
