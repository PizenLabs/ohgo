package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/intelligence/ast"
)

// CheckTool discovers and executes the repository's own checks.
//
// It does not invent a command: test, build, and static-analysis commands come
// from observed manifests and test inventory. Discovery is read-only; run mode
// executes only the fixed commands the plan names. It uses argv, never a shell,
// so a manifest cannot inject shell syntax into a check.
type CheckTool struct {
	Base

	// Root is the workspace the commands run in.
	Root string
	// Source provides the inventory the discovery reads.
	Source *StructuralSource
	// Now is the clock seam.
	Now func() time.Time

	run checkRunner
}

// Check bounds.
const (
	CheckDefaultTimeout = 2 * time.Minute
	CheckMaxTimeout     = 5 * time.Minute
	CheckMaxCommands    = 4
	CheckMaxOutputBytes = 64 << 10
	checkDisplayBytes   = 4000
)

// checkKind is the verification class of one command.
type checkKind string

const (
	checkTest   checkKind = "test"
	checkStatic checkKind = "static"
	checkBuild  checkKind = "build"
)

// checkCommand is one fully resolved argv command.
type checkCommand struct {
	kind  checkKind
	name  string
	argv  []string
	dir   string
	scope string
	// script is the package.json script body when the command delegates to a
	// package manager. It is disclosed and inspected before execution.
	script string
}

func (c checkCommand) String() string { return strings.Join(c.argv, " ") }

// checkExecution is the observed result of running one command.
type checkExecution struct {
	exitCode int
	output   string
	err      error
}

type checkRunner func(ctx context.Context, command checkCommand, timeout time.Duration) checkExecution

// checkPlan is the bounded discovery result.
type checkPlan struct {
	Ecosystems []string
	Commands   []checkCommand
	Notes      []string
	Scope      string
	ScopeKnown bool
	// scopeRoot is the workspace root used only to display command
	// directories as repository-relative coordinates.
	scopeRoot string
}

// checkCounts is the parsed test count. Known is false when the output did not
// support a count, which is different from an observed zero.
type checkCounts struct {
	Known   bool
	Passed  int
	Failed  int
	Skipped int
	NotRun  int
}

// checkResult is one executed command.
type checkResult struct {
	Command checkCommand
	// Executed reports that a process actually started. A refused directory or
	// an empty plan did not execute anything.
	Executed     bool
	Status       string
	ExitCode     int
	Duration     time.Duration
	Counts       checkCounts
	Output       string
	Truncated    bool
	TimedOut     bool
	Verification *agent.VerificationEvidence
}

// NewCheckTool returns the repository check capability for one workspace.
func NewCheckTool(source *StructuralSource) *CheckTool {
	root := ""
	if source != nil {
		root = source.Root()
	}
	tool := &CheckTool{Root: root, Source: source, Now: time.Now, run: runCheckCommand}
	tool.Base = NewBase("check",
		"Discover and run the repository's own checks: tests, static analysis, "+
			"and build. mode=discover (default) reads manifests and test inventory "+
			"and returns the exact commands without executing anything. mode=run "+
			"executes only the selected checks (default test) and returns their "+
			"status, exit code, parsed counts, bounded output, and verification "+
			"evidence. Commands come from go.mod, package.json, Cargo.toml, "+
			"pyproject/requirements, Maven/Gradle, Gemfile, or dotnet manifests; "+
			"the tool never guesses a whole-suite run for an unknown ecosystem. "+
			"A failed check is an observed failure, not a missing observation. "+
			"Use path to scope Go commands; other ecosystems run at the manifest "+
			"root and report that the scope was not applied.",
		map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"mode": map[string]any{
					"type":        "string",
					"enum":        []any{"discover", "run"},
					"description": "Discover the plan without executing, or run the selected checks. Defaults to discover.",
				},
				"checks": map[string]any{
					"type":        "array",
					"description": "Check kinds to include. Defaults to every discovered kind in discover mode and test only in run mode.",
					"maxItems":    3,
					"items": map[string]any{
						"type": "string",
						"enum": []any{"test", "static", "build"},
					},
				},
				"path": map[string]any{
					"type":        "string",
					"description": "Optional repository-relative file or directory. Applied to Go commands; other ecosystems report that the scope was not applied.",
				},
				"timeout_ms": map[string]any{
					"type":        "integer",
					"minimum":     1000.0,
					"maximum":     float64(CheckMaxTimeout.Milliseconds()),
					"description": "Per-check timeout. Defaults to 120000 ms.",
				},
			},
		},
		agent.Approval{Tier: agent.TierExec, Reason: "runs repository test, build, and static-analysis commands"},
	)
	tool.SetActionClasses(agent.ActionExecute)
	return tool
}

// Capability keeps discovery read-only: only run mode executes a process.
func (t *CheckTool) Capability(args map[string]any) agent.Tier {
	mode, _ := args["mode"].(string)
	if strings.EqualFold(strings.TrimSpace(mode), "run") {
		return agent.TierExec
	}
	return agent.TierRead
}

// Execute implements agent.Tool.
func (t *CheckTool) Execute(ctx agent.Context, args map[string]any) agent.Result {
	mode, err := strArg(args, "mode", "discover")
	if err != nil {
		return requestError(agent.ToolFailureInvalidArgument, "%v; nothing executed.", err)
	}
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode != "discover" && mode != "run" {
		return requestError(agent.ToolFailureInvalidArgument,
			"unsupported mode %q; nothing executed. Valid modes: discover, run.", mode)
	}
	kinds, err := checkKindsArg(args, mode)
	if err != nil {
		return requestError(agent.ToolFailureInvalidArgument, "%v; nothing executed.", err)
	}
	pathRaw, err := strArg(args, "path", "")
	if err != nil {
		return requestError(agent.ToolFailureInvalidArgument, "%v; nothing executed.", err)
	}
	pathRaw = strings.TrimSpace(pathRaw)
	timeout := CheckDefaultTimeout
	if raw, ok := args["timeout_ms"]; ok && raw != nil {
		ms, err := intArg(args, "timeout_ms", int(CheckDefaultTimeout.Milliseconds()))
		if err != nil {
			return requestError(agent.ToolFailureInvalidArgument, "%v; nothing executed.", err)
		}
		if ms < 1000 || time.Duration(ms)*time.Millisecond > CheckMaxTimeout {
			return requestError(agent.ToolFailureInvalidArgument,
				"timeout_ms must be between 1000 and %d; nothing executed.",
				CheckMaxTimeout.Milliseconds())
		}
		timeout = time.Duration(ms) * time.Millisecond
	}
	if err := checkCancelled(ctx.Ctx); err != nil {
		return agent.ErrorResult("%v", err)
	}

	scope, err := checkPathScope(t.Root, pathRaw)
	if err != nil {
		return requestError(agent.ToolFailureInvalidArgument, "%v; nothing executed.", err)
	}
	inventory, err := t.inventory(ctx.Ctx)
	if err != nil {
		return agent.Result{Text: fmt.Sprintf("read repository inventory: %v", err), IsError: true,
			FailureClass: agent.ToolFailureExecutionFailed}
	}
	plan := buildCheckPlan(t.Root, inventory, scope, kinds)
	if mode == "discover" {
		return checkDiscoveryResult(plan)
	}
	return t.runPlan(ctx, plan, timeout)
}

// inventory reads the shared structural snapshot without parsing source.
func (t *CheckTool) inventory(ctx context.Context) (ast.Inventory, error) {
	if t.Source == nil {
		return ast.Inventory{}, errors.New("no structural source is configured")
	}
	index, err := t.Source.Index()
	if err != nil {
		return ast.Inventory{}, err
	}
	return index.Inventory(ctx, ast.InventoryQuery{MaxFiles: ast.InventoryDefaultMaxFiles})
}

// checkKindsArg resolves the selected check kinds. Discovery defaults to all;
// execution defaults to test only, because a routine question must not pay for
// a full build and static-analysis sweep.
func checkKindsArg(args map[string]any, mode string) (map[checkKind]bool, error) {
	values, err := stringSliceArg(args, "checks")
	if err != nil {
		return nil, err
	}
	out := map[checkKind]bool{}
	if len(values) == 0 {
		if mode == "discover" {
			out[checkTest], out[checkStatic], out[checkBuild] = true, true, true
		} else {
			out[checkTest] = true
		}
		return out, nil
	}
	if len(values) > 3 {
		return nil, fmt.Errorf("argument \"checks\" has %d entries, maximum 3", len(values))
	}
	for _, value := range values {
		switch checkKind(strings.ToLower(strings.TrimSpace(value))) {
		case checkTest:
			out[checkTest] = true
		case checkStatic:
			out[checkStatic] = true
		case checkBuild:
			out[checkBuild] = true
		default:
			return nil, fmt.Errorf("unsupported check %q; use test, static, or build", value)
		}
	}
	return out, nil
}

// checkScope is the validated path selector.
type checkScope struct {
	abs    string
	rel    string
	isFile bool
}

func checkPathScope(root, raw string) (*checkScope, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	abs, err := resolvePath(root, raw)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("scope %q: %w", raw, err)
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return nil, fmt.Errorf("relativize scope %q: %w", raw, err)
	}
	return &checkScope{abs: abs, rel: filepath.ToSlash(rel), isFile: !info.IsDir()}, nil
}

// buildCheckPlan derives commands from observed manifests. It adds at most
// CheckMaxCommands commands and records why anything was omitted.
func buildCheckPlan(root string, inventory ast.Inventory, scope *checkScope, kinds map[checkKind]bool) checkPlan {
	plan := checkPlan{scopeRoot: root}
	if scope != nil {
		plan.Scope = scope.rel
		plan.ScopeKnown = true
	}
	paths := map[string]bool{}
	dirs := map[string]bool{".": true}
	for _, file := range inventory.Files {
		rel := path.Clean(strings.TrimPrefix(file.Path, "./"))
		paths[rel] = true
		for dir := path.Dir(rel); dir != "." && dir != "/" && dir != ""; dir = path.Dir(dir) {
			dirs[dir] = true
		}
	}
	add := func(command checkCommand) {
		if !kinds[command.kind] {
			return
		}
		if len(plan.Commands) >= CheckMaxCommands {
			plan.Notes = appendUnique(plan.Notes,
				fmt.Sprintf("plan is bounded to %d commands; further commands were omitted", CheckMaxCommands))
			return
		}
		for _, existing := range plan.Commands {
			if existing.String() == command.String() && existing.dir == command.dir {
				return
			}
		}
		plan.Commands = append(plan.Commands, command)
	}
	ecosystem := func(name string) {
		plan.Ecosystems = appendUnique(plan.Ecosystems, name)
	}

	if paths["go.mod"] || inventory.Snapshot.Module != "" {
		ecosystem("go")
		dirsToCheck := []string{inventory.Snapshot.ModuleRoot}
		if dirsToCheck[0] == "" {
			dirsToCheck[0] = root
		}
		if inventory.Snapshot.ModuleAmbiguous && len(inventory.Snapshot.ModuleCandidates) > 0 {
			dirsToCheck = dirsToCheck[:0]
			for _, candidate := range inventory.Snapshot.ModuleCandidates {
				if len(dirsToCheck) >= 2 {
					break
				}
				dirsToCheck = append(dirsToCheck, filepath.Join(root, filepath.FromSlash(candidate)))
			}
			plan.Notes = appendUnique(plan.Notes,
				"sibling Go modules have no single owner; commands are planned per module candidate")
		}
		for _, dir := range dirsToCheck {
			if !checkDirWithin(root, dir) {
				plan.Notes = appendUnique(plan.Notes,
					"Go module root is outside the workspace; commands run at the workspace root")
				dir = root
			}
			target, scoped := goCheckTarget(dir, scope)
			if scope != nil && !scoped {
				plan.Notes = appendUnique(plan.Notes,
					"path scope is outside the Go module; commands use the module root")
			}
			add(checkCommand{kind: checkTest, name: "go test", argv: []string{"go", "test", target}, dir: dir, scope: target})
			add(checkCommand{kind: checkStatic, name: "go vet", argv: []string{"go", "vet", target}, dir: dir, scope: target})
			add(checkCommand{kind: checkBuild, name: "go build", argv: []string{"go", "build", target}, dir: dir, scope: target})
		}
	}

	if paths["package.json"] {
		ecosystem("node")
		for candidate := range paths {
			if candidate != "package.json" && path.Base(candidate) == "package.json" {
				plan.Notes = appendUnique(plan.Notes,
					"nested package.json manifests are not expanded; only the root package is planned")
				break
			}
		}
		if commands, notes := nodeCheckCommands(root, paths, dirs, scope); len(commands) > 0 || len(notes) > 0 {
			for _, command := range commands {
				add(command)
			}
			for _, note := range notes {
				plan.Notes = appendUnique(plan.Notes, note)
			}
			if scope != nil {
				plan.Notes = appendUnique(plan.Notes, "path scope is applied to Go commands only; Node checks run at the package root")
			}
		}
	}
	if paths["Cargo.toml"] {
		ecosystem("rust")
		add(checkCommand{kind: checkTest, name: "cargo test", argv: []string{"cargo", "test"}, dir: root, scope: "workspace"})
		add(checkCommand{kind: checkStatic, name: "cargo check", argv: []string{"cargo", "check"}, dir: root, scope: "workspace"})
		add(checkCommand{kind: checkBuild, name: "cargo build", argv: []string{"cargo", "build"}, dir: root, scope: "workspace"})
		if scope != nil {
			plan.Notes = appendUnique(plan.Notes, "path scope is applied to Go commands only; Rust checks run at the workspace root")
		}
	}
	if pythonProject(paths, dirs) {
		ecosystem("python")
		if commands, notes := pythonCheckCommands(root, paths, dirs, scope); len(commands) > 0 || len(notes) > 0 {
			for _, command := range commands {
				add(command)
			}
			for _, note := range notes {
				plan.Notes = appendUnique(plan.Notes, note)
			}
		}
	}
	if paths["pom.xml"] {
		ecosystem("maven")
		mvn := "mvn"
		if paths["mvnw"] {
			mvn = "./mvnw"
		}
		add(checkCommand{kind: checkTest, name: "maven test", argv: []string{mvn, "-q", "test"}, dir: root, scope: "workspace"})
		add(checkCommand{kind: checkBuild, name: "maven package", argv: []string{mvn, "-q", "-DskipTests", "package"}, dir: root, scope: "workspace"})
	}
	if paths["build.gradle"] || paths["build.gradle.kts"] {
		ecosystem("gradle")
		gradle := "gradle"
		if paths["gradlew"] {
			gradle = "./gradlew"
		}
		add(checkCommand{kind: checkTest, name: "gradle test", argv: []string{gradle, "test"}, dir: root, scope: "workspace"})
		add(checkCommand{kind: checkBuild, name: "gradle build", argv: []string{gradle, "build"}, dir: root, scope: "workspace"})
	}
	if paths["Gemfile"] {
		ecosystem("ruby")
		if dirs["spec"] {
			add(checkCommand{kind: checkTest, name: "rspec", argv: []string{"bundle", "exec", "rspec"}, dir: root, scope: "workspace"})
		} else if paths["Rakefile"] {
			add(checkCommand{kind: checkTest, name: "rake test", argv: []string{"bundle", "exec", "rake", "test"}, dir: root, scope: "workspace"})
		}
	}
	for candidate := range paths {
		if strings.HasSuffix(candidate, ".sln") || strings.HasSuffix(candidate, ".csproj") {
			ecosystem("dotnet")
			add(checkCommand{kind: checkTest, name: "dotnet test", argv: []string{"dotnet", "test"}, dir: root, scope: "workspace"})
			add(checkCommand{kind: checkBuild, name: "dotnet build", argv: []string{"dotnet", "build"}, dir: root, scope: "workspace"})
			break
		}
	}
	if paths["mix.exs"] {
		ecosystem("elixir")
		add(checkCommand{kind: checkTest, name: "mix test", argv: []string{"mix", "test"}, dir: root, scope: "workspace"})
		add(checkCommand{kind: checkBuild, name: "mix compile", argv: []string{"mix", "compile"}, dir: root, scope: "workspace"})
	}
	if paths["CMakeLists.txt"] && dirs["build"] {
		ecosystem("cmake")
		add(checkCommand{kind: checkBuild, name: "cmake build", argv: []string{"cmake", "--build", "build"}, dir: root, scope: "workspace"})
	}
	if len(plan.Commands) == 0 {
		plan.Notes = appendUnique(plan.Notes, "no supported test, static-analysis, or build command was discovered")
	}
	sort.SliceStable(plan.Commands, func(i, j int) bool {
		if plan.Commands[i].kind != plan.Commands[j].kind {
			return checkKindOrder(plan.Commands[i].kind) < checkKindOrder(plan.Commands[j].kind)
		}
		return plan.Commands[i].String() < plan.Commands[j].String()
	})
	return plan
}

func goCheckTarget(dir string, scope *checkScope) (string, bool) {
	if scope == nil {
		return "./...", true
	}
	abs := scope.abs
	if scope.isFile {
		abs = filepath.Dir(abs)
	}
	// The index walk root is symlink-resolved while a tool path keeps the
	// workspace spelling (on macOS /var is a symlink to /private/var).
	// Comparing the two spellings would make an in-module scope look outside.
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	rel, err := filepath.Rel(dir, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "./...", false
	}
	rel = filepath.ToSlash(rel)
	if rel == "." {
		return "./...", true
	}
	return "./" + rel + "/...", true
}

func nodeCheckCommands(root string, paths, dirs map[string]bool, scope *checkScope) ([]checkCommand, []string) {
	data, err := readCheckFile(filepath.Join(root, "package.json"), 1<<20)
	if err != nil {
		return nil, []string{"package.json could not be read; Node checks were not planned"}
	}
	var manifest struct {
		Scripts         map[string]string `json:"scripts"`
		PackageManager  string            `json:"packageManager"`
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, []string{"package.json is not valid JSON; Node checks were not planned"}
	}
	manager := nodePackageManager(manifest.PackageManager, paths)
	var out []checkCommand
	var notes []string
	usedScript := false
	addScript := func(kind checkKind, name, scriptName string) bool {
		body, ok := manifest.Scripts[scriptName]
		if !ok {
			return false
		}
		if reason := inspectNodeScript(body); reason != "" {
			notes = append(notes, fmt.Sprintf("package.json script %q was not planned: %s", scriptName, reason))
			return false
		}
		out = append(out, checkCommand{
			kind: kind, name: name, argv: []string{manager, "run", scriptName},
			dir: root, scope: "workspace", script: capCheckText(body, 240),
		})
		usedScript = true
		return true
	}
	if _, declared := manifest.Scripts["test"]; declared {
		addScript(checkTest, "node test", "test")
	} else if runner := nodeTestRunner(manifest.DevDependencies); runner != "" {
		out = append(out, checkCommand{kind: checkTest, name: "node test", argv: nodeExecArgv(manager, nodeTestArgs(runner)...), dir: root, scope: "workspace"})
	} else {
		notes = append(notes, "package.json has no test script and no known test runner dependency")
	}
	addScript(checkBuild, "node build", "build")
	if _, ok := manifest.Scripts["lint"]; ok {
		addScript(checkStatic, "node lint", "lint")
	} else if _, ok := manifest.Scripts["typecheck"]; ok {
		addScript(checkStatic, "node typecheck", "typecheck")
	} else if paths["tsconfig.json"] {
		out = append(out, checkCommand{kind: checkStatic, name: "tsc", argv: nodeExecArgv(manager, "tsc", "--noEmit"), dir: root, scope: "workspace"})
	}
	if usedScript {
		notes = append(notes, "package.json may run pre/post lifecycle scripts; only the named script was inspected")
	}
	return out, notes
}

// nodeScriptUnsafe lists command shapes that must not run as an unreviewed
// package script. It is a conservative blocklist, not a sandbox: a safe script
// can still use the package manager arbitrarily.
var nodeScriptUnsafe = []struct {
	pattern *regexp.Regexp
	reason  string
}{
	{regexp.MustCompile(`\brm\s+-[a-z]*r[a-z]*f[a-z]*\s+(?:/|~|\.|\*|\$)`), "recursive delete of the root, home, current, wildcard, or a variable path"},
	{regexp.MustCompile(`\brm\s+-[a-z]*f[a-z]*r[a-z]*\s+(?:/|~|\.|\*|\$)`), "recursive delete of the root, home, current, wildcard, or a variable path"},
	{regexp.MustCompile(`\bsudo\b`), "privilege escalation"},
	{regexp.MustCompile(`\bcurl\b[^\n|]*\|\s*(?:sh|bash)\b`), "remote script piped to a shell"},
	{regexp.MustCompile(`\bwget\b[^\n|]*\|\s*(?:sh|bash)\b`), "remote script piped to a shell"},
	{regexp.MustCompile(`\b(?:chmod|chown)\s+(?:-R\s+)?(?:777|root)\b`), "permission or ownership change"},
	{regexp.MustCompile(`\bmkfs\b|\bdd\s+if=`), "filesystem or disk write"},
	{regexp.MustCompile(`\b(?:shutdown|reboot|halt|poweroff)\b`), "machine shutdown"},
	{regexp.MustCompile(`:\(\)\s*\{`), "fork bomb"},
	{regexp.MustCompile(`\b(?:nc|ncat|netcat)\s+-|\b(?:ssh|scp)\s+`), "network shell or remote transfer"},
	{regexp.MustCompile(`\bgit\s+push\b|\bnpm\s+publish\b|\bterraform\s+apply\b|\bkubectl\s+(?:apply|delete)\b|\bdocker\s+(?:push|run|system)\b`), "remote or infrastructure side effect"},
	{regexp.MustCompile(`\b(?:aws|gcloud|az)\s+`), "cloud provider command"},
	{regexp.MustCompile(`\bcurl\b[^\n]*(?:--data|-d\s|-X\s*POST)`), "network write"},
	{regexp.MustCompile(`(?:^|[\s/])\.(?:ssh|aws|gnupg|config/gcloud)\b`), "credential directory access"},
}

// inspectNodeScript reports why a script is too risky to execute unreviewed.
// An empty result means the script contains no known destructive shape.
func inspectNodeScript(body string) string {
	lower := strings.ToLower(body)
	for _, rule := range nodeScriptUnsafe {
		if match := rule.pattern.FindString(lower); match != "" {
			return rule.reason + " (" + capCheckText(strings.TrimSpace(match), 80) + ")"
		}
	}
	return ""
}

func capCheckText(text string, limit int) string {
	if limit <= 0 || len(text) <= limit {
		return text
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}

func nodePackageManager(declared string, paths map[string]bool) string {
	if declared != "" {
		name := declared
		if index := strings.IndexByte(name, '@'); index > 0 {
			name = name[:index]
		}
		switch name {
		case "npm", "pnpm", "yarn", "bun":
			return name
		}
	}
	switch {
	case paths["pnpm-lock.yaml"]:
		return "pnpm"
	case paths["yarn.lock"]:
		return "yarn"
	case paths["bun.lockb"], paths["bun.lock"]:
		return "bun"
	default:
		return "npm"
	}
}

func nodeTestRunner(devDependencies map[string]string) string {
	switch {
	case devDependencies["vitest"] != "":
		return "vitest"
	case devDependencies["jest"] != "":
		return "jest"
	case devDependencies["mocha"] != "":
		return "mocha"
	default:
		return ""
	}
}

// nodeExecArgv runs a package binary without letting npm-style option parsing
// consume its flags. Bun uses `bunx`; the others accept `exec --`.
func nodeExecArgv(manager string, args ...string) []string {
	if manager == "bun" {
		return append([]string{"bunx"}, args...)
	}
	return append([]string{manager, "exec", "--"}, args...)
}

// nodeTestArgs adds the non-watch form for a runner discovered from
// dependencies. A bare `vitest` would start watch mode and never exit.
func nodeTestArgs(runner string) []string {
	switch runner {
	case "vitest":
		return []string{"vitest", "run"}
	case "jest":
		return []string{"jest", "--ci"}
	default:
		return []string{runner}
	}
}

func pythonProject(paths, dirs map[string]bool) bool {
	if paths["pyproject.toml"] || paths["requirements.txt"] || paths["setup.py"] ||
		paths["pytest.ini"] || paths["tox.ini"] || paths["conftest.py"] ||
		dirs["tests"] || dirs["test"] {
		return true
	}
	for candidate := range paths {
		base := path.Base(candidate)
		if strings.HasPrefix(base, "test_") && strings.HasSuffix(base, ".py") {
			return true
		}
	}
	return false
}

func pythonCheckCommands(root string, paths, dirs map[string]bool, scope *checkScope) ([]checkCommand, []string) {
	var out []checkCommand
	var notes []string
	hasTests := dirs["tests"] || dirs["test"]
	for candidate := range paths {
		base := path.Base(candidate)
		if strings.HasPrefix(base, "test_") && strings.HasSuffix(base, ".py") {
			hasTests = true
			break
		}
	}
	if hasTests || paths["pytest.ini"] || paths["tox.ini"] {
		argv := []string{"python3", "-m", "pytest"}
		if scope != nil {
			argv = append(argv, scope.rel)
		}
		out = append(out, checkCommand{kind: checkTest, name: "pytest", argv: argv, dir: root, scope: "workspace"})
	}
	pyproject := ""
	if paths["pyproject.toml"] {
		if data, err := readCheckFile(filepath.Join(root, "pyproject.toml"), 1<<20); err == nil {
			pyproject = string(data)
		}
	}
	if strings.Contains(pyproject, "[tool.ruff") || paths["ruff.toml"] || paths[".ruff.toml"] {
		out = append(out, checkCommand{kind: checkStatic, name: "ruff", argv: []string{"ruff", "check", "."}, dir: root, scope: "workspace"})
	} else if strings.Contains(pyproject, "[tool.mypy") || paths["mypy.ini"] {
		out = append(out, checkCommand{kind: checkStatic, name: "mypy", argv: []string{"mypy", "."}, dir: root, scope: "workspace"})
	}
	if len(out) == 0 {
		notes = append(notes, "no pytest configuration or test file was discovered")
	}
	return out, notes
}

func readCheckFile(file string, limit int64) ([]byte, error) {
	info, err := os.Stat(file)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("%s is a directory", file)
	}
	if info.Size() > limit {
		return nil, fmt.Errorf("%s exceeds %d bytes", file, limit)
	}
	return os.ReadFile(file)
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func checkKindOrder(kind checkKind) int {
	switch kind {
	case checkTest:
		return 0
	case checkStatic:
		return 1
	default:
		return 2
	}
}

func checkDiscoveryResult(plan checkPlan) agent.Result {
	var text strings.Builder
	text.WriteString("check plan: discovery only; nothing executed\n")
	if len(plan.Ecosystems) > 0 {
		fmt.Fprintf(&text, "ecosystems: %s\n", strings.Join(plan.Ecosystems, ", "))
	}
	if plan.ScopeKnown {
		fmt.Fprintf(&text, "scope: %s\n", plan.Scope)
	}
	for _, command := range plan.Commands {
		fmt.Fprintf(&text, "- %s: %s", command.kind, command.String())
		if command.scope != "" {
			fmt.Fprintf(&text, " (scope %s)", command.scope)
		}
		fmt.Fprintf(&text, " in %s\n", checkDisplayDir(plan.scopeRoot, command.dir))
		if command.script != "" {
			fmt.Fprintf(&text, "  script: %s\n", command.script)
		}
	}
	for _, note := range plan.Notes {
		fmt.Fprintf(&text, "note: %s\n", note)
	}
	details := map[string]any{
		"status":     "DISCOVERED",
		"mode":       "discover",
		"ecosystems": plan.Ecosystems,
		"commands":   checkCommandDetails(plan.Commands, plan.scopeRoot),
		"notes":      plan.Notes,
		"scope":      plan.Scope,
	}
	if len(plan.Commands) == 0 {
		return agent.Result{Text: strings.TrimRight(text.String(), "\n"), Details: details, Useless: true}
	}
	return agent.Result{Text: strings.TrimRight(text.String(), "\n"), Details: details}
}

func (t *CheckTool) runPlan(ctx agent.Context, plan checkPlan, timeout time.Duration) agent.Result {
	if len(plan.Commands) == 0 {
		return agent.Result{
			Text:    "checks: NOT_RUN (no supported command was discovered)",
			Details: map[string]any{"status": "NOT_RUN", "mode": "run", "ecosystems": plan.Ecosystems, "notes": plan.Notes},
			Useless: true,
		}
	}
	runner := t.run
	if runner == nil {
		runner = runCheckCommand
	}
	if t.Now == nil {
		t.Now = time.Now
	}
	results := make([]checkResult, 0, len(plan.Commands))
	for _, command := range plan.Commands {
		if err := checkCancelled(ctx.Ctx); err != nil {
			return agent.ErrorResult("%v", err)
		}
		if len(command.argv) == 0 || !checkDirWithin(t.Root, command.dir) {
			results = append(results, checkResult{
				Command:  command,
				Status:   "FAIL",
				ExitCode: -1,
				Output:   "command refused: the working directory is outside the workspace or the argv is empty",
			})
			continue
		}
		started := t.Now()
		execution := runner(ctx.Ctx, command, timeout)
		result := checkResult{
			Command: command, Executed: true, ExitCode: execution.exitCode,
			Duration: t.Now().Sub(started),
		}
		if execution.err != nil && !errors.Is(execution.err, context.Canceled) &&
			!errors.Is(execution.err, context.DeadlineExceeded) {
			result.Output = execution.output
			result.Status = "FAIL"
			result.ExitCode = -1
			results = append(results, result)
			continue
		}
		if errors.Is(execution.err, context.DeadlineExceeded) {
			result.TimedOut = true
			result.Status = "FAIL"
			result.ExitCode = -1
		} else {
			result.Status, result.Counts = classifyCheck(command.kind, execution.exitCode, execution.output)
		}
		result.Output, result.Truncated = tailCheckOutput(execution.output, checkDisplayBytes)
		if result.Status == "PASS" {
			result.Verification = checkVerificationEvidence(t.Root, command)
		}
		results = append(results, result)
	}
	return checkRunResult(plan, results)
}

func classifyCheck(kind checkKind, exit int, output string) (string, checkCounts) {
	counts := parseCheckCounts(kind, output)
	status := "PASS"
	if exit != 0 {
		status = "FAIL"
	}
	if kind == checkTest && status == "PASS" && counts.Known &&
		counts.Passed+counts.Failed+counts.Skipped == 0 {
		// A successful command that reported no executed test is not a
		// passing test run. NotRun may be the packages that carry no tests;
		// keeping the two separate prevents "0 tests" from becoming
		// completion evidence.
		status = "NOT_RUN"
	}
	return status, counts
}

var (
	goTestPass    = regexp.MustCompile(`(?m)^\s*--- PASS:`)
	goTestFail    = regexp.MustCompile(`(?m)^\s*--- FAIL:`)
	goTestSkip    = regexp.MustCompile(`(?m)^\s*--- SKIP:`)
	goPackageOK   = regexp.MustCompile(`(?m)^ok\s+\S+`)
	goPackageFail = regexp.MustCompile(`(?m)^FAIL\s+\S+`)
	goPackageNone = regexp.MustCompile(`(?m)^\?\s+\S+`)
	pytestPass    = regexp.MustCompile(`(\d+)\s+passed`)
	pytestFail    = regexp.MustCompile(`(\d+)\s+failed`)
	pytestError   = regexp.MustCompile(`(\d+)\s+error`)
	pytestSkip    = regexp.MustCompile(`(\d+)\s+skipped`)
	cargoCounts   = regexp.MustCompile(`test result: (?:ok|FAILED)\.\s+(\d+) passed;\s+(\d+) failed;\s+(\d+) ignored`)
	genericFailed = regexp.MustCompile(`(\d+)\s+failed`)
	genericPassed = regexp.MustCompile(`(\d+)\s+passed`)
	genericSkip   = regexp.MustCompile(`(\d+)\s+skipped`)
	genericIgnore = regexp.MustCompile(`(\d+)\s+ignored`)
)

func parseCheckCounts(kind checkKind, output string) checkCounts {
	if kind != checkTest {
		return checkCounts{}
	}
	if values, ok := regexCounts(output, cargoCounts, 3); ok {
		return checkCounts{Known: true, Passed: values[0], Failed: values[1], Skipped: values[2]}
	}
	passed := len(goTestPass.FindAllString(output, -1))
	failed := len(goTestFail.FindAllString(output, -1))
	skipped := len(goTestSkip.FindAllString(output, -1))
	if passed+failed+skipped > 0 {
		return checkCounts{Known: true, Passed: passed, Failed: failed, Skipped: skipped}
	}
	if pytestFail.MatchString(output) || pytestPass.MatchString(output) ||
		pytestError.MatchString(output) || pytestSkip.MatchString(output) {
		return checkCounts{
			Known:   true,
			Passed:  regexFirst(pytestPass, output),
			Failed:  regexFirst(pytestFail, output) + regexFirst(pytestError, output),
			Skipped: regexFirst(pytestSkip, output),
		}
	}
	if genericPassed.MatchString(output) || genericFailed.MatchString(output) ||
		genericSkip.MatchString(output) || genericIgnore.MatchString(output) {
		return checkCounts{
			Known:   true,
			Passed:  regexFirst(genericPassed, output),
			Failed:  regexFirst(genericFailed, output),
			Skipped: regexFirst(genericSkip, output) + regexFirst(genericIgnore, output),
		}
	}
	// Non-verbose go test reports packages, not individual tests. A package
	// line is not a test count: mixing the two units would make "1 passed"
	// mean one package for Go and one test elsewhere. The one exception is a
	// package with no test files, which is an explicit NOT_RUN signal.
	ok := len(goPackageOK.FindAllString(output, -1))
	bad := len(goPackageFail.FindAllString(output, -1))
	none := len(goPackageNone.FindAllString(output, -1))
	if ok+bad+none > 0 {
		if ok+bad == 0 {
			return checkCounts{Known: true, NotRun: none}
		}
		return checkCounts{}
	}
	return checkCounts{}
}

func regexCounts(output string, pattern *regexp.Regexp, want int) ([]int, bool) {
	match := pattern.FindStringSubmatch(output)
	if len(match) != want+1 {
		return nil, false
	}
	out := make([]int, 0, want)
	for _, value := range match[1:] {
		number, err := strconv.Atoi(value)
		if err != nil {
			return nil, false
		}
		out = append(out, number)
	}
	return out, true
}

func regexFirst(pattern *regexp.Regexp, output string) int {
	match := pattern.FindStringSubmatch(output)
	if len(match) < 2 {
		return 0
	}
	value, err := strconv.Atoi(match[1])
	if err != nil {
		return 0
	}
	return value
}

func tailCheckOutput(output string, limit int) (string, bool) {
	if len(output) <= limit {
		return output, false
	}
	return output[len(output)-limit:], true
}

func checkVerificationEvidence(root string, command checkCommand) *agent.VerificationEvidence {
	line := command.String()
	if evidence := VerificationForIn(root, line); evidence != nil {
		return evidence
	}
	check := contract.CheckAny
	switch command.kind {
	case checkTest:
		check = contract.CheckTest
	case checkBuild:
		check = contract.CheckBuild
	case checkStatic:
		check = contract.CheckVet
	}
	return &agent.VerificationEvidence{Command: line, Check: check}
}

func checkRunResult(plan checkPlan, results []checkResult) agent.Result {
	passed, failed, notRun := 0, 0, 0
	for _, result := range results {
		switch result.Status {
		case "PASS":
			passed++
		case "FAIL":
			failed++
		default:
			notRun++
		}
	}
	overall := "NOT_RUN"
	switch {
	case failed > 0:
		overall = "FAIL"
	case passed > 0:
		overall = "PASS"
	}
	verification := strongestCheckVerification(results)
	verifies := overall == "PASS" && passed > 0
	executed := false
	for _, result := range results {
		if result.Executed {
			executed = true
			break
		}
	}
	var text strings.Builder
	fmt.Fprintf(&text, "checks: %s (%d checks", overall, len(results))
	if len(plan.Ecosystems) > 0 {
		fmt.Fprintf(&text, " · %s", strings.Join(plan.Ecosystems, ", "))
	}
	if plan.ScopeKnown {
		fmt.Fprintf(&text, " · scope %s", plan.Scope)
	}
	text.WriteString(")\n")
	for _, result := range results {
		fmt.Fprintf(&text, "- %s %s: %s", result.Command.kind, result.Command.String(), result.Status)
		if result.Status == "FAIL" && !result.TimedOut {
			fmt.Fprintf(&text, " (exit %d)", result.ExitCode)
		}
		if result.TimedOut {
			text.WriteString(" (timed out)")
		}
		if result.Counts.Known {
			fmt.Fprintf(&text, " (%d passed, %d failed, %d skipped, %d not run)",
				result.Counts.Passed, result.Counts.Failed, result.Counts.Skipped, result.Counts.NotRun)
		}
		if result.Duration > 0 {
			fmt.Fprintf(&text, " %s", result.Duration.Round(time.Millisecond))
		}
		text.WriteString("\n")
		if result.Status == "FAIL" && result.Output != "" {
			text.WriteString("  output:\n")
			for _, line := range strings.Split(strings.TrimRight(result.Output, "\n"), "\n") {
				if len(line) > 500 {
					line = line[:500]
				}
				fmt.Fprintf(&text, "  | %s\n", line)
			}
		}
	}
	for _, note := range plan.Notes {
		fmt.Fprintf(&text, "note: %s\n", note)
	}
	details := map[string]any{
		"status":       overall,
		"mode":         "run",
		"ecosystems":   plan.Ecosystems,
		"checks":       checkResultDetails(results, plan.scopeRoot),
		"notes":        plan.Notes,
		"scope":        plan.Scope,
		"verifies":     verifies,
		"executed":     executed,
		"verification": verificationDetail(verification),
	}
	result := agent.Result{
		Text:         strings.TrimRight(text.String(), "\n"),
		Details:      details,
		Executes:     executed,
		Finished:     executed,
		Verifies:     verifies,
		Verification: verification,
	}
	if overall == "FAIL" {
		result.IsError = true
		result.FailureClass = agent.ToolFailureExecutionFailed
	}
	if overall == "NOT_RUN" {
		result.Useless = true
	}
	return result
}

func strongestCheckVerification(results []checkResult) *agent.VerificationEvidence {
	var best *agent.VerificationEvidence
	bestRank := -1
	for _, result := range results {
		if result.Status != "PASS" || result.Verification == nil {
			continue
		}
		rank := checkRank(result.Verification.EffectiveCheck())
		if rank > bestRank {
			best = result.Verification
			bestRank = rank
		}
	}
	return best
}

func checkRank(check contract.VerificationCheck) int {
	switch check {
	case contract.CheckTest:
		return 3
	case contract.CheckBuild:
		return 2
	case contract.CheckVet:
		return 1
	default:
		return 0
	}
}

func checkResultDetails(results []checkResult, root string) []map[string]any {
	out := make([]map[string]any, 0, len(results))
	for _, result := range results {
		out = append(out, map[string]any{
			"kind":        string(result.Command.kind),
			"name":        result.Command.name,
			"command":     result.Command.String(),
			"dir":         checkDisplayDir(root, result.Command.dir),
			"scope":       result.Command.scope,
			"status":      result.Status,
			"executed":    result.Executed,
			"exit_code":   result.ExitCode,
			"duration_ms": result.Duration.Milliseconds(),
			"timed_out":   result.TimedOut,
			"output":      result.Output,
			"output_cut":  result.Truncated,
			"test_counts": map[string]any{
				"known":   result.Counts.Known,
				"passed":  result.Counts.Passed,
				"failed":  result.Counts.Failed,
				"skipped": result.Counts.Skipped,
				"not_run": result.Counts.NotRun,
			},
			"verification": verificationDetail(result.Verification),
		})
	}
	return out
}

func checkCommandDetails(commands []checkCommand, root string) []map[string]any {
	out := make([]map[string]any, 0, len(commands))
	for _, command := range commands {
		out = append(out, map[string]any{
			"kind":    string(command.kind),
			"name":    command.name,
			"command": command.String(),
			"dir":     checkDisplayDir(root, command.dir),
			"scope":   command.scope,
			"script":  command.script,
		})
	}
	return out
}

// checkDisplayDir keeps host absolute paths out of the model-facing result.
func checkDisplayDir(root, dir string) string {
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return "."
	}
	rel = filepath.ToSlash(rel)
	if rel == "" {
		return "."
	}
	return rel
}

// checkDirWithin re-checks the execution directory after symlink resolution.
// The plan already clamps Go module roots, but this is the last boundary
// before a process starts.
func checkDirWithin(root, dir string) bool {
	if root == "" || dir == "" {
		return false
	}
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	return withinRoot(root, dir)
}

// checkEnvironment removes credential-shaped variables from the process the
// check starts. This is a boundary, not a sandbox: a check can still read
// credential files under HOME, which is why approval remains TierExec.
func checkEnvironment(base []string) []string {
	out := make([]string, 0, len(base))
	for _, entry := range base {
		key := entry
		if index := strings.IndexByte(entry, '='); index >= 0 {
			key = entry[:index]
		}
		if sensitiveCheckEnv(key) {
			continue
		}
		out = append(out, entry)
	}
	return out
}

func sensitiveCheckEnv(key string) bool {
	upper := strings.ToUpper(strings.TrimSpace(key))
	for _, marker := range []string{
		"TOKEN", "SECRET", "PASSWORD", "PASSWD", "API_KEY", "APIKEY",
		"CREDENTIAL", "PRIVATE_KEY", "AUTH", "SESSION", "COOKIE",
	} {
		if strings.Contains(upper, marker) {
			return true
		}
	}
	switch {
	case strings.HasPrefix(upper, "AWS_"):
		return upper != "AWS_REGION" && upper != "AWS_DEFAULT_REGION"
	case strings.HasPrefix(upper, "NPM_CONFIG_") && strings.Contains(upper, "AUTH"):
		return true
	default:
		return false
	}
}

func verificationDetail(evidence *agent.VerificationEvidence) map[string]any {
	if evidence == nil {
		return nil
	}
	scopes := make([]map[string]any, 0, len(evidence.Scopes))
	for _, scope := range evidence.Scopes {
		scopes = append(scopes, map[string]any{
			"path": scope.Path, "recursive": scope.Recursive, "all": scope.All,
		})
	}
	return map[string]any{
		"command": evidence.Command,
		"check":   string(evidence.EffectiveCheck()),
		"scopes":  scopes,
	}
}

// checkBuffer keeps the tail of command output. A failing test suite can print
// far more than a tool result should carry, and the tail holds the failure.
type checkBuffer struct {
	data      []byte
	limit     int
	truncated bool
}

func (b *checkBuffer) Write(p []byte) (int, error) {
	b.data = append(b.data, p...)
	if len(b.data) > b.limit {
		b.data = b.data[len(b.data)-b.limit:]
		b.truncated = true
	}
	return len(p), nil
}

func (b *checkBuffer) String() string { return string(b.data) }

func runCheckCommand(ctx context.Context, command checkCommand, timeout time.Duration) checkExecution {
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	process := exec.CommandContext(runCtx, command.argv[0], command.argv[1:]...)
	process.Dir = command.dir
	process.Env = checkEnvironment(os.Environ())
	buffer := &checkBuffer{limit: CheckMaxOutputBytes}
	process.Stdout = buffer
	process.Stderr = buffer
	err := process.Run()
	exit := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exit = exitErr.ExitCode()
		} else {
			exit = -1
		}
	}
	if runCtx.Err() == context.DeadlineExceeded {
		err = context.DeadlineExceeded
		exit = -1
	}
	return checkExecution{exitCode: exit, output: buffer.String(), err: err}
}
