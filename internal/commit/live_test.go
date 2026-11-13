package commit

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/provider/catalog"
)

// TestLiveCommitProposal is the opt-in real-model check for the /commit
// control plane. It is skipped in the normal suite: it makes provider calls and
// requires a credential.
//
// Run with:
//
//	OHGO_COMMIT_LIVE=1 GROQ_API_KEY=... go test ./internal/commit -run TestLiveCommitProposal -v
//
// The default target is Groq's openai/gpt-oss-20b. Override the endpoint with
// OHGO_COMMIT_MODEL, OHGO_COMMIT_BASE_URL, OHGO_COMMIT_API, and
// OHGO_COMMIT_KEY_ENV. The test measures scope containment, classification,
// bounded turns, token usage, and latency. It never executes a commit.
func TestLiveCommitProposal(t *testing.T) {
	if os.Getenv("OHGO_COMMIT_LIVE") != "1" {
		t.Skip("set OHGO_COMMIT_LIVE=1 to run the live COMMIT proposal check")
	}
	model, stream := liveCommitModel(t)

	t.Run("single feature with tests", func(t *testing.T) {
		repo := newTestRepo(t)
		initialCommit(t, repo, "internal/commit/a.go", "package commit\n")
		writeTestFile(t, repo, "internal/commit/a.go", "package commit\n\nfunc A() {}\n")
		writeTestFile(t, repo, "internal/commit/a_test.go", "package commit\n\nfunc TestA(t *testing.T) {}\n")

		request, diag := livePropose(t, repo, model, stream)
		assertScope(t, request, "internal/commit/a.go", "internal/commit/a_test.go")
		assertProposalValid(t, request)
		if diag.Attempts < 1 || diag.Attempts > 2 {
			t.Errorf("attempts = %d, want the bounded proposal plus at most one repair", diag.Attempts)
		}
		if diag.OutputTokens == 0 {
			t.Error("the provider reported no output tokens")
		}
		if diag.SelectedFiles != len(request.Snapshot.Scope) {
			t.Errorf("selected diagnostic = %d, scope = %d", diag.SelectedFiles, len(request.Snapshot.Scope))
		}
	})

	t.Run("mixed unrelated changes", func(t *testing.T) {
		repo := newTestRepo(t)
		initialCommit(t, repo, "internal/commit/a.go", "package commit\n")
		initialCommit(t, repo, "internal/commit/b.go", "package commit\n")
		initialCommit(t, repo, "internal/tui/view.go", "package tui\n")
		writeTestFile(t, repo, "internal/commit/a.go", "package commit\n\nfunc A() {}\n")
		writeTestFile(t, repo, "internal/commit/b.go", "package commit\n\nfunc B() {}\n")
		writeTestFile(t, repo, "internal/tui/view.go", "package tui\n\nfunc View() {}\n")

		request, _ := livePropose(t, repo, model, stream)
		for _, change := range request.Snapshot.Scope {
			if strings.HasPrefix(change.Path, "internal/tui/") {
				t.Errorf("unrelated TUI change entered the scope: %s", renderChanges(request.Snapshot.Scope))
			}
		}
		if len(request.Snapshot.Context.Excluded) == 0 {
			t.Error("the unrelated group was not reported as excluded")
		}
		assertProposalValid(t, request)
	})

	t.Run("documentation only", func(t *testing.T) {
		repo := newTestRepo(t)
		initialCommit(t, repo, "base.txt", "base\n")
		writeTestFile(t, repo, "docs/commit.md", "# commit\n")

		request, _ := livePropose(t, repo, model, stream)
		assertScope(t, request, "docs/commit.md")
		if request.Proposal.Type != "docs" {
			t.Errorf("type = %q, want docs for a documentation-only change", request.Proposal.Type)
		}
		assertProposalValid(t, request)
	})

	t.Run("tests only", func(t *testing.T) {
		repo := newTestRepo(t)
		initialCommit(t, repo, "internal/commit/a.go", "package commit\n")
		writeTestFile(t, repo, "internal/commit/a_test.go", "package commit\n\nfunc TestA(t *testing.T) {}\n")

		request, _ := livePropose(t, repo, model, stream)
		assertScope(t, request, "internal/commit/a_test.go")
		if request.Proposal.Type != "test" {
			t.Errorf("type = %q, want test for a tests-only change", request.Proposal.Type)
		}
		assertProposalValid(t, request)
	})

	t.Run("build only", func(t *testing.T) {
		repo := newTestRepo(t)
		initialCommit(t, repo, "go.mod", "module example.com/test\n\ngo 1.27\n")
		writeTestFile(t, repo, "go.mod", "module example.com/test\n\ngo 1.27\n\nrequire example.com/x v1.0.0\n")

		request, _ := livePropose(t, repo, model, stream)
		assertScope(t, request, "go.mod")
		switch request.Proposal.Type {
		case "build", "ci", "chore":
		default:
			t.Errorf("type = %q, want build/ci/chore for a build-only change", request.Proposal.Type)
		}
		assertProposalValid(t, request)
	})

	t.Run("protected local state", func(t *testing.T) {
		repo := newTestRepo(t)
		initialCommit(t, repo, "internal/commit/a.go", "package commit\n")
		writeTestFile(t, repo, "internal/commit/a.go", "package commit\n\nfunc A() {}\n")
		writeTestFile(t, repo, ".env", "LIVE_SECRET=must-not-be-sent\n")

		request, _ := livePropose(t, repo, model, stream)
		assertScope(t, request, "internal/commit/a.go")
		if len(request.Snapshot.Context.Protected) != 1 {
			t.Errorf("protected = %s, want .env", renderChanges(request.Snapshot.Context.Protected))
		}
		if strings.Contains(request.Snapshot.Evidence(), "LIVE_SECRET") {
			t.Error("the live secret reached the model evidence")
		}
		assertProposalValid(t, request)
	})
	t.Run("cross-directory logical grouping", func(t *testing.T) {
		repo := newTestRepo(t)
		initialCommit(t, repo, "base.txt", "base\n")
		writeTestFile(t, repo, "src/client/components/Checkout.tsx", "export function Checkout() { return 'checkout' }\n")
		writeTestFile(t, repo, "src/client/components/CheckoutSummary.tsx", "export function CheckoutSummary() { return 'summary' }\n")
		writeTestFile(t, repo, "src/client/hooks/useCheckout.ts", "export function useCheckout() { return 'checkout' }\n")
		writeTestFile(t, repo, "src/client/api/checkout.ts", "export function loadCheckout() { return 'checkout' }\n")
		writeTestFile(t, repo, "src/auth/Login.tsx", "export function Login() { return 'login' }\n")
		writeTestFile(t, repo, "README.md", "# project\n")
		writeTestFile(t, repo, ".env", "LIVE_SECRET=must-not-be-sent\n")

		service := liveService(repo, model, stream)
		plan, err := service.Plan(context.Background())
		if err != nil {
			t.Fatalf("live Plan: %v\n%s", err, AbortText(err))
		}
		t.Logf("plan: %s", PlanSummary(plan))
		if len(plan.Groups) != 3 {
			t.Fatalf("groups = %s, want checkout, authentication, and documentation",
				FormatGroupSummary(plan.Groups))
		}
		if len(plan.Protected) != 1 {
			t.Errorf("protected = %s, want .env", renderChanges(plan.Protected))
		}
		checkoutFiles := 0
		for _, group := range plan.Groups {
			if group.Scope == "checkout" {
				checkoutFiles = len(group.Files)
			}
		}
		if checkoutFiles != 4 {
			t.Errorf("checkout files = %d, want the four cross-directory files", checkoutFiles)
		}
	})

	t.Run("automatic plan group proposals", func(t *testing.T) {
		repo := newTestRepo(t)
		initialCommit(t, repo, "internal/alpha/a.go", "package alpha\n")
		initialCommit(t, repo, "internal/beta/b.go", "package beta\n")
		writeTestFile(t, repo, "internal/alpha/a.go", "package alpha\n\nfunc Alpha() {}\n")
		writeTestFile(t, repo, "internal/beta/b.go", "package beta\n\nfunc Beta() {}\n")

		service := liveService(repo, model, stream)
		var diagnostics ProposalDiagnostics
		service.OnDiagnostics = func(d ProposalDiagnostics) { diagnostics = d }
		created := 0
		for {
			plan, err := service.Plan(context.Background())
			if errors.Is(err, ErrNoChanges) {
				break
			}
			if err != nil {
				t.Fatalf("live Plan: %v\n%s", err, AbortText(err))
			}
			if len(plan.Groups) == 0 {
				break
			}
			group := plan.Groups[0]
			request, err := service.ProposeGroup(context.Background(), plan, group.Key)
			if err != nil {
				t.Fatalf("live ProposeGroup(%s): %v\n%s", group.Key, err, AbortText(err))
			}
			if request.Progress == nil || request.Progress.Position != 1 {
				t.Errorf("progress = %+v, want the remaining plan position", request.Progress)
			}
			if !sameStringSet(request.Snapshot.StagePaths(), groupPaths(group)) {
				t.Errorf("scope = %s, want only %s",
					renderChanges(request.Snapshot.Scope), renderChanges(group.Files))
			}
			assertProposalValid(t, request)
			t.Logf("proposal %d/%d: %s (attempts=%d output_tokens=%d reasoning_tokens=%d)",
				created+1, created+request.Progress.Total, request.Proposal.Message(),
				diagnostics.Attempts, diagnostics.OutputTokens, diagnostics.ReasoningTokens)
			if _, err := service.Execute(context.Background(), request); err != nil {
				t.Fatalf("live Execute: %v\n%s", err, AbortText(err))
			}
			created++
			if created > 4 {
				t.Fatal("live automatic plan did not terminate")
			}
		}
		if created != 2 {
			t.Errorf("created = %d commits, want one per logical change", created)
		}
		if got := commitCount(t, repo); got != 4 {
			t.Errorf("commits = %d, want two initial commits plus two logical commits", got)
		}
	})

}

// livePropose runs one bounded proposal and returns the request plus its
// diagnostics. A failed proposal is a test failure with the classified text.
func liveService(repo string, model ai.Model, stream ai.StreamFn) *Service {
	return &Service{
		Root:    repo,
		Stream:  stream,
		Model:   model,
		Effort:  "off",
		Timeout: 2 * time.Minute,
	}
}

func livePropose(t *testing.T, repo string, model ai.Model, stream ai.StreamFn) (*Request, ProposalDiagnostics) {
	t.Helper()
	var diagnostics ProposalDiagnostics
	service := liveService(repo, model, stream)
	service.OnDiagnostics = func(d ProposalDiagnostics) {
		diagnostics = d
	}
	request, err := service.Propose(context.Background())
	if err != nil {
		t.Fatalf("live Propose: %v\n%s", err, AbortText(err))
	}
	t.Logf("proposal: %s", request.Proposal.Message())
	t.Logf("diagnostics: phase=%s attempts=%d regenerations=%d output_tokens=%d reasoning_tokens=%d elapsed=%s groups=%d selected=%d excluded=%d protected=%d",
		diagnostics.Phase, diagnostics.Attempts, diagnostics.Regenerations,
		diagnostics.OutputTokens, diagnostics.ReasoningTokens,
		diagnostics.Elapsed.Round(time.Millisecond), diagnostics.Groups,
		diagnostics.SelectedFiles, diagnostics.ExcludedFiles, diagnostics.ProtectedFiles)
	return request, diagnostics
}

// assertScope proves the selected paths are exactly the expected ones.
func assertScope(t *testing.T, request *Request, want ...string) {
	t.Helper()
	got := request.Snapshot.StagePaths()
	if !sameStrings(got, want) {
		t.Errorf("scope = %v, want %v", got, want)
	}
}

// assertProposalValid re-applies both deterministic passes to the live output.
func assertProposalValid(t *testing.T, request *Request) {
	t.Helper()
	if err := request.Proposal.Validate(request.Snapshot.Refs); err != nil {
		t.Errorf("live proposal failed syntax/length validation: %v", err)
	}
	if err := request.Snapshot.ValidateProposal(request.Proposal); err != nil {
		t.Errorf("live proposal failed semantic validation: %v", err)
	}
	if strings.TrimSpace(request.Proposal.Subject()) == "" {
		t.Error("live proposal has no subject")
	}
}

// liveCommitModel resolves the live provider from the environment. The default
// is a small, fast Groq model; override with OHGO_COMMIT_MODEL/API_KEY_ENV.
func liveCommitModel(t *testing.T) (ai.Model, ai.StreamFn) {
	t.Helper()
	ref := strings.TrimSpace(os.Getenv("OHGO_COMMIT_MODEL"))
	if ref == "" {
		ref = "groq/openai/gpt-oss-20b"
	}
	model, err := catalog.Resolve(ref)
	if err != nil {
		// Dynamic providers (OpenRouter, for example) are not in the bundled
		// catalog. An explicit endpoint keeps the live check usable without
		// making /commit depend on a network catalog lookup.
		provider, id, ok := strings.Cut(ref, "/")
		baseURL := strings.TrimSpace(os.Getenv("OHGO_COMMIT_BASE_URL"))
		apiName := strings.TrimSpace(os.Getenv("OHGO_COMMIT_API"))
		if apiName == "" {
			apiName = string(ai.APIOpenAIChatCompletions)
		}
		if baseURL == "" {
			switch provider {
			case "groq":
				baseURL = "https://api.groq.com/openai/v1"
			case "openrouter":
				baseURL = "https://openrouter.ai/api/v1"
			}
		}
		if !ok || provider == "" || id == "" || baseURL == "" {
			t.Fatalf("resolve live COMMIT model %q: %v", ref, err)
		}
		model = ai.Model{
			Provider:        provider,
			ID:              id,
			Name:            id,
			API:             ai.API(apiName),
			BaseURL:         baseURL,
			ContextWindow:   128000,
			MaxOutputTokens: 4096,
		}
	}
	keyEnv := strings.TrimSpace(os.Getenv("OHGO_COMMIT_KEY_ENV"))
	if keyEnv == "" {
		keyEnv = strings.ToUpper(model.Provider) + "_API_KEY"
	}
	key := strings.TrimSpace(os.Getenv(keyEnv))
	if key == "" {
		t.Skipf("%s is not set; cannot call the live provider", keyEnv)
	}
	registry := ai.NewRegistry()
	registry.SetAPIKey(model.Provider, key)
	return model, registry.StreamFn()
}
