package main

// Task is one repository-discovery question with evaluator-only ground truth.
//
// The prompt is identical across every track. Only the tool set differs, so a
// measured difference is attributable to discovery mechanism rather than task
// wording or evaluator hints.
type Task struct {
	// ID is stable across reports.
	ID string `json:"id"`
	// Category is one of the benchmark categories A-H.
	Category string `json:"category"`
	// Prompt is the exact user message sent to the model.
	Prompt string `json:"prompt"`
	// ExpectedStatus is the final-answer status the task requires. For a task
	// with several requested targets, the status is still "exact": every
	// requested target was found without ambiguity.
	ExpectedStatus string `json:"expected_status"`
	// Targets are the declarations or lines a correct answer must include.
	Targets []Target `json:"targets"`
	// NegativeNames are symbols that must not exist. They are used both to
	// validate the fixture and to make the negative expectation explicit.
	NegativeNames []string `json:"negative_names,omitempty"`
	// Notes explains what the task is intended to stress. It is never shown
	// to the model.
	Notes string `json:"notes,omitempty"`
}

// Target is one expected answer coordinate.
type Target struct {
	// Path is repository-relative and slash-separated.
	Path string `json:"path"`
	// Symbol is the exact indexed name, or empty for a textual target.
	Symbol string `json:"symbol,omitempty"`
	// AcceptSymbols permits an equivalent declaration spelling for the same
	// edit surface (for example a table variable or its accessor).
	AcceptSymbols []string `json:"accept_symbols,omitempty"`
	// StartLine and EndLine bound the declaration. A textual target uses one
	// line and Contains to make the expectation checkable.
	StartLine int `json:"start_line"`
	EndLine   int `json:"end_line"`
	// Contains is required for a textual target so fixture validation proves
	// the line still says what the task asks for.
	Contains string `json:"contains,omitempty"`
}

// phase2Tasks is the frozen benchmark suite.
//
// Every target was read from the Phase 1 commit (53bc07b) and is revalidated
// against the fixture before a run starts. Keeping the ground truth in code
// rather than in a prompt is what makes the evaluation independent of the
// model's answer style.
func phase2Tasks() []Task {
	return []Task{
		{
			ID:             "A1-exact-new-symbols-tool",
			Category:       "A_EXACT",
			Prompt:         "Find the declaration of `NewSymbolsTool`. Report its repository-relative file, symbol name, and full line range.",
			ExpectedStatus: "exact",
			Targets: []Target{{
				Path: "tools/symbols.go", Symbol: "NewSymbolsTool", StartLine: 39, EndLine: 85,
			}},
			Notes: "An exported constructor with a unique name; the lexical baseline is also strong here.",
		},
		{
			ID:             "A2-exact-index-lookup",
			Category:       "A_EXACT",
			Prompt:         "Find the implementation of the method `Index.Lookup` (the `Lookup` method on the `Index` type). Report the file, qualified symbol, and line range.",
			ExpectedStatus: "exact",
			Targets: []Target{{
				Path: "ast/index.go", Symbol: "Index.Lookup", StartLine: 276, EndLine: 293,
			}},
			Notes: "A receiver-qualified method whose bare name is not indexed.",
		},
		{
			ID:             "A3-exact-grep-cap",
			Category:       "A_EXACT",
			Prompt:         "Find the declaration of the constant `GrepSingleFileMatches`. Report the file, symbol, and line range.",
			ExpectedStatus: "exact",
			Targets: []Target{{
				Path: "tools/grep.go", Symbol: "GrepSingleFileMatches", StartLine: 32, EndLine: 32,
			}},
			Notes: "A one-line constant in a file full of similarly named Grep* constants.",
		},
		{
			ID:             "B1-ambiguous-classify",
			Category:       "B_AMBIGUOUS",
			Prompt:         "Find every declaration named exactly `Classify` in the repository. Report all of them; do not choose one. If more than one exists, preserve the ambiguity.",
			ExpectedStatus: "ambiguous",
			Targets: []Target{
				{Path: "startup/startup.go", Symbol: "Classify", StartLine: 106, EndLine: 111},
				{Path: "workspace/intent.go", Symbol: "Classify", StartLine: 118, EndLine: 118},
			},
			Notes: "Two unrelated Classify functions; ClassifyPrediction and ClassifyRisk are decoys.",
		},
		{
			ID:             "B2-ambiguous-agent-config",
			Category:       "B_AMBIGUOUS",
			Prompt:         "Find the `Config` type that configures the agent loop (the type accepted by `agent.New`). Report the file, symbol, and line range.",
			ExpectedStatus: "exact",
			Targets: []Target{{
				Path: "agent/loop.go", Symbol: "Config", StartLine: 46, EndLine: 129,
			}},
			Notes: "The repository also has shell.Config; the task requires choosing by context.",
		},
		{
			ID:             "B3-ambiguous-execute-methods",
			Category:       "B_AMBIGUOUS",
			Prompt:         "List every method named exactly `Execute` declared in package `tools`. Exclude functions and any name that merely contains `Execute`. Report each method with its file and line range.",
			ExpectedStatus: "ambiguous",
			Targets: []Target{
				{Path: "tools/artifact.go", Symbol: "ArtifactTool.Execute", StartLine: 78, EndLine: 125},
				{Path: "tools/bash.go", Symbol: "BashTool.Execute", StartLine: 120, EndLine: 238},
				{Path: "tools/delete.go", Symbol: "DeleteTool.Execute", StartLine: 38, EndLine: 81},
				{Path: "tools/edit.go", Symbol: "EditTool.Execute", StartLine: 50, EndLine: 159},
				{Path: "tools/glob.go", Symbol: "GlobTool.Execute", StartLine: 126, EndLine: 253},
				{Path: "tools/grep.go", Symbol: "GrepTool.Execute", StartLine: 126, EndLine: 288},
				{Path: "tools/ls.go", Symbol: "LsTool.Execute", StartLine: 60, EndLine: 171},
				{Path: "tools/read.go", Symbol: "ReadTool.Execute", StartLine: 62, EndLine: 152},
				{Path: "tools/read.go", Symbol: "WriteTool.Execute", StartLine: 187, EndLine: 249},
				{Path: "tools/symbols.go", Symbol: "SymbolsTool.Execute", StartLine: 106, EndLine: 323},
				{Path: "tools/task.go", Symbol: "TaskTool.Execute", StartLine: 106, EndLine: 183},
				{Path: "tools/todo.go", Symbol: "TodoTool.Execute", StartLine: 286, EndLine: 335},
				{Path: "tools/wait.go", Symbol: "WaitTool.Execute", StartLine: 69, EndLine: 117},
				{Path: "tools/webfetch.go", Symbol: "WebFetchTool.Execute", StartLine: 64, EndLine: 160},
			},
			Notes: "14 methods across 12 files; package tools has 459 declarations, so the result requires scoping/pagination rather than one exact lookup.",
		},
		{
			ID:             "C1-path-settings-config",
			Category:       "C_PATH_PACKAGE",
			Prompt:         "Find the `Settings` type in package `config`. Report the file, symbol, and line range.",
			ExpectedStatus: "exact",
			Targets: []Target{{
				Path: "config/config.go", Symbol: "Settings", StartLine: 93, EndLine: 147,
			}},
			Notes: "Package-scoped type lookup with no same-name competitor.",
		},
		{
			ID:             "C2-path-project-load",
			Category:       "C_PATH_PACKAGE",
			Prompt:         "Find the `Load` function declared in package `project` (not `config`). Report the file, symbol, and line range.",
			ExpectedStatus: "exact",
			Targets: []Target{{
				Path: "project/project.go", Symbol: "Load", StartLine: 140, EndLine: 158,
			}},
			Notes: "Two Load functions exist; the package constraint chooses one.",
		},
		{
			ID:             "C3-path-workspace-prediction",
			Category:       "C_PATH_PACKAGE",
			Prompt:         "Find the `Prediction` type in package `workspace`. Report the file, symbol, and line range.",
			ExpectedStatus: "exact",
			Targets: []Target{{
				Path: "workspace/intent.go", Symbol: "Prediction", StartLine: 85, EndLine: 94,
			}},
			Notes: "Package-scoped type lookup.",
		},
		{
			ID:             "D1-mod-provider-env-table",
			Category:       "D_MODIFICATION",
			Prompt:         "A new provider needs to be added to the credential-discovery table so its environment variable can be found. Where should that mapping be added? Report the file and the declaration that owns the table.",
			ExpectedStatus: "exact",
			Targets: []Target{{
				Path: "creds/creds.go", Symbol: "providerEnvVars", AcceptSymbols: []string{"ProviderEnvVars"}, StartLine: 50, EndLine: 67,
			}},
			Notes: "The edit surface is the providerEnvVars map; the accessor is an acceptable equivalent.",
		},
		{
			ID:             "D2-mod-tool-registration",
			Category:       "D_MODIFICATION",
			Prompt:         "Where is the tool set assembled when a session starts? A newly added tool must be registered there. Report the file and the enclosing function.",
			ExpectedStatus: "exact",
			Targets: []Target{{
				Path: "cmd/ohgo/run.go", Symbol: "buildTools", StartLine: 382, EndLine: 413,
			}},
			Notes: "The assembly point; a registration change is a single edit surface.",
		},
		{
			ID:             "D3-mod-grep-single-file-cap",
			Category:       "D_MODIFICATION",
			Prompt:         "Where is the per-file match cap for a single-file grep search defined? Report the file, declaration, and line range.",
			ExpectedStatus: "exact",
			Targets: []Target{{
				Path: "tools/grep.go", Symbol: "GrepSingleFileMatches", StartLine: 32, EndLine: 32,
			}},
			Notes: "A named constant used at one policy point.",
		},
		{
			ID:             "D4-mod-prompt-identity",
			Category:       "D_MODIFICATION",
			Prompt:         "Where is the fixed identity text of the system prompt defined? Report the file, declaration, and line range.",
			ExpectedStatus: "exact",
			Targets: []Target{{
				Path: "prompt/prompt.go", Symbol: "Identity", StartLine: 20, EndLine: 38,
			}},
			Notes: "Identity is also a project type and a shell type; the prompt constant is the edit surface.",
		},
		{
			ID:             "E1-cross-grep-def-and-registration",
			Category:       "E_CROSS_FILE",
			Prompt:         "Where is `GrepTool.Execute` defined, and where is `GrepTool` registered in the agent's tool set? Report both locations.",
			ExpectedStatus: "exact",
			Targets: []Target{
				{Path: "tools/grep.go", Symbol: "GrepTool.Execute", StartLine: 126, EndLine: 288},
				{Path: "cmd/ohgo/run.go", Symbol: "buildTools", StartLine: 382, EndLine: 413},
			},
			Notes: "Definition and registration are in different packages.",
		},
		{
			ID:             "E2-cross-lookup-def-and-caller",
			Category:       "E_CROSS_FILE",
			Prompt:         "Where is `Index.Lookup` defined, and which tool method calls it to answer a symbol lookup? Report both locations.",
			ExpectedStatus: "exact",
			Targets: []Target{
				{Path: "ast/index.go", Symbol: "Index.Lookup", StartLine: 276, EndLine: 293},
				{Path: "tools/symbols.go", Symbol: "SymbolsTool.Execute", StartLine: 106, EndLine: 323},
			},
			Notes: "The structural layer and its single model-facing caller.",
		},
		{
			ID:             "E3-cross-close-def-and-run-path",
			Category:       "E_CROSS_FILE",
			Prompt:         "Where is `Store.Close` defined, and where is it called in the one-shot run path? Report both locations.",
			ExpectedStatus: "exact",
			Targets: []Target{
				{Path: "session/session.go", Symbol: "Store.Close", StartLine: 147, EndLine: 152},
				{Path: "cmd/ohgo/run.go", Symbol: "runOnce", StartLine: 1137, EndLine: 1196},
			},
			Notes: "runOnce defers the closeStore closure, so the call is one hop away from session.Store.Close.",
		},
		{
			ID:             "F1-negative-find-references",
			Category:       "F_NEGATIVE",
			Prompt:         "Does a function named `FindReferences` exist anywhere in this repository? If not, report not_found. Do not report similarly named functions.",
			ExpectedStatus: "not_found",
			NegativeNames:  []string{"FindReferences"},
			Notes:          "A plausible capability name that does not exist in the frozen tree.",
		},
		{
			ID:             "F2-negative-call-graph",
			Category:       "F_NEGATIVE",
			Prompt:         "Does a type named `CallGraph` exist? Report not_found if no declaration has that exact name.",
			ExpectedStatus: "not_found",
			NegativeNames:  []string{"CallGraph"},
			Notes:          "A plausible repository-intelligence type that is absent.",
		},
		{
			ID:             "F3-negative-grep-symbol",
			Category:       "F_NEGATIVE",
			Prompt:         "Does a declaration named exactly `Grep` exist? (`GrepTool`, `NewGrepTool`, and `GrepTimeout` are different names.) If no declaration has that exact name, report not_found.",
			ExpectedStatus: "not_found",
			NegativeNames:  []string{"Grep"},
			Notes:          "Lexical search returns many hits; no declaration has the exact requested name.",
		},
		{
			ID:             "G1-noise-classify-risk",
			Category:       "G_NOISE",
			Prompt:         "Find the declaration named exactly `ClassifyRisk`. Report the file, symbol, and line range.",
			ExpectedStatus: "exact",
			Targets: []Target{{
				Path: "security/scope.go", Symbol: "ClassifyRisk", StartLine: 111, EndLine: 132,
			}},
			Notes: "Classify, ClassifyPrediction, and ClassifyRisk compete lexically.",
		},
		{
			ID:             "G2-noise-prompt-identity",
			Category:       "G_NOISE",
			Prompt:         "Find the `Identity` declaration in package `prompt` (the system-prompt constant), not the `Identity` types in other packages. Report the file, symbol, and line range.",
			ExpectedStatus: "exact",
			Targets: []Target{{
				Path: "prompt/prompt.go", Symbol: "Identity", StartLine: 20, EndLine: 38,
			}},
			Notes: "Three declarations named Identity exist in different packages.",
		},
		{
			ID:             "G3-noise-all-load",
			Category:       "G_NOISE",
			Prompt:         "Find every declaration named exactly `Load`. Report all of them; preserve ambiguity if there is more than one.",
			ExpectedStatus: "ambiguous",
			Targets: []Target{
				{Path: "config/config.go", Symbol: "Load", StartLine: 192, EndLine: 216},
				{Path: "project/project.go", Symbol: "Load", StartLine: 140, EndLine: 158},
			},
			Notes: "Two unrelated Load functions.",
		},
		{
			ID:             "H1-lexical-error-text",
			Category:       "H_LEXICAL",
			Prompt:         "Find where the error message text `agent: no tools registered` is defined. Report the file, declaration, and line.",
			ExpectedStatus: "exact",
			Targets: []Target{{
				Path: "agent/loop.go", Symbol: "ErrNoTools", StartLine: 16, EndLine: 16,
			}},
			Notes: "A string literal; a structural symbol lookup cannot find it from the text alone.",
		},
		{
			ID:             "H2-lexical-todo-literal",
			Category:       "H_LEXICAL",
			Prompt:         "Find where the string literal `TODO` is used to label an open task in the task view. Report the file, enclosing function, and line.",
			ExpectedStatus: "exact",
			Targets: []Target{{
				Path: "task/view.go", Symbol: "renderTodoContext", StartLine: 124, EndLine: 144,
			}},
			Notes: "The line is inside a rendering function; the TODO text is a string literal.",
		},
		{
			ID:             "H3-lexical-json-field",
			Category:       "H_LEXICAL",
			Prompt:         "Find the struct field that serializes as the JSON key `tool_choice` in the OpenAI request body. Report the file, enclosing type, and line.",
			ExpectedStatus: "exact",
			Targets: []Target{{
				Path: "ai/openai.go", Symbol: "openAIRequest", StartLine: 39, EndLine: 50,
			}},
			Notes: "A JSON struct tag, not a symbol name.",
		},
		{
			ID:             "H4-lexical-doc-line",
			Category:       "H_LEXICAL",
			Prompt:         "Find the line in the architecture documentation that says `Single owner of ProviderEnvVars`. Report the file and line.",
			ExpectedStatus: "exact",
			Targets: []Target{{
				Path: "docs/architecture/ARCHITECTURE.md", StartLine: 23, EndLine: 23, Contains: "Single owner of ProviderEnvVars",
			}},
			Notes: "Non-Go documentation; a Go symbol index cannot represent it.",
		},
		{
			ID:             "H5-lexical-makefile-target",
			Category:       "H_LEXICAL",
			Prompt:         "Find the Makefile target that runs the race-enabled test suite. Report the file and line.",
			ExpectedStatus: "exact",
			Targets: []Target{{
				Path: "Makefile", StartLine: 49, EndLine: 49, Contains: "race:",
			}},
			Notes: "Build configuration outside Go syntax.",
		},
	}
}
