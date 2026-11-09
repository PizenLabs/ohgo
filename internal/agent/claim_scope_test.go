package agent

import (
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/control/contract"
)

// claimContract is the informational contract shape used by the practical
// repository-analysis case. It carries grounding so AssessGrounding runs.
func claimContract() contract.IntentContract {
	return contract.IntentContract{
		Objective: "check this project and analyze what this project is about?",
		Kind:      contract.ObjectiveInformational,
		Grounding: &contract.IntentGrounding{
			Objective:  "check this project and analyze what this project is about?",
			Action:     contract.GroundingRead,
			Actions:    []contract.GroundingAction{contract.GroundingRead},
			Confidence: contract.GroundingConfidenceHigh,
		},
	}
}

func claimRead(path, text string, totalLines int) ToolOutcome {
	details := map[string]any{"path": path}
	if totalLines > 0 {
		details["totalLines"] = totalLines
		details["offset"] = 1
		details["limit"] = 2000
	}
	return ToolOutcome{
		ToolName: "read",
		Args:     map[string]any{"path": path},
		Result:   Result{Text: text, Details: details},
	}
}

func claimListing(path, text string) ToolOutcome {
	return ToolOutcome{
		ToolName: "ls",
		Args:     map[string]any{"path": path},
		Result:   Result{Text: text, Details: map[string]any{"path": path}},
	}
}

func claimGlob(pattern, text string) ToolOutcome {
	return ToolOutcome{
		ToolName: "glob",
		Args:     map[string]any{"pattern": pattern},
		Result:   Result{Text: text},
	}
}

func claimVerdict(turns []Turn, answer string) Completion {
	return EvaluateTaskCompletion(CompletionInput{
		Turns:    append(turns, answerTurnNoTools(answer)),
		Contract: claimContract(),
	})
}

// The practical failure: a bounded report about what the probes surfaced is an
// observation about this run, not a repository-wide absence conclusion. It must
// complete; the same sentence with a global scope must not.
func TestBoundedProbeReportIsNotAnAbsenceClaim(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		claimListing(".", "README.md\npackage.json\nbackend\nsrc\nsupabase\n"),
		claimRead("README.md", "# E-commerce Microservices Platform", 333),
		claimRead("package.json", `{"dependencies":{"react":"18"}}`, 35),
		claimRead("backend/go.mod", "module ecommerce", 52),
		claimRead("backend/docker-compose.yml", "services:\n  postgres:\n    image: postgres:15-alpine\n", 266),
	}}}
	verdict := claimVerdict(turns,
		"No test files or CI configuration were surfaced in the probes — worth verifying if you want to assess maturity.")
	if !verdict.Complete() {
		t.Fatalf("a bounded probe report was reviewed: %s", verdict.Reason)
	}
}

// A global absence conclusion from a root listing is still a review boundary.
func TestBoundedProbePhraseDoesNotExcuseGlobalAbsence(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		claimListing(".", "README.md\npackage.json\nbackend\nsrc\nsupabase\n"),
	}}}
	verdict := claimVerdict(turns,
		"No test files were found in the repository by the probes.")
	if verdict.Complete() {
		t.Fatal("a repository-wide absence conclusion was accepted from a bounded probe phrase")
	}
	if !strings.Contains(verdict.Reason, "intent grounding") {
		t.Fatalf("reason = %q, want a grounding boundary", verdict.Reason)
	}
}

// A complete recursive scan of every Go file that contains no _test.go file
// proves the language-qualified test-file claim. The same scan with a test
// file, a pruned scan, or a bare "no tests" claim does not.
func TestCompleteGoScanProvesNoGoTestFiles(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		claimGlob("**/*.go", "backend/cmd/main.go\nbackend/internal/order/order.go\n"),
	}}}
	if verdict := claimVerdict(turns, "No Go test files are present."); !verdict.Complete() {
		t.Fatalf("a complete Go scan did not prove the narrowed absence: %s", verdict.Reason)
	}

	withTest := []Turn{{Results: []ToolOutcome{
		claimGlob("**/*.go", "backend/cmd/main.go\nbackend/internal/order/order_test.go\n"),
	}}}
	if verdict := claimVerdict(withTest, "No Go test files are present."); verdict.Complete() {
		t.Fatal("a scan containing a Go test file was accepted as proof of absence")
	}

	pruned := []Turn{{Results: []ToolOutcome{
		claimGlob("**/*.go", "backend/cmd/main.go\n\n[4 paths were excluded by .gitignore]"),
	}}}
	if verdict := claimVerdict(pruned, "No Go test files are present."); verdict.Complete() {
		t.Fatal("a pruned Go scan was accepted as proof of absence")
	}
}

// A language name that merely contains "go" is not a Go test claim.
func TestUnrelatedLanguageNameDoesNotBecomeGoTestProof(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		claimGlob("**/*.go", "backend/cmd/main.go\n"),
	}}}
	if verdict := claimVerdict(turns, "No Django test files are present."); verdict.Complete() {
		t.Fatal("a Django test claim was accepted from a Go scan")
	}
}

// A bare "no tests" claim is still a concept absence: a complete Go scan does
// not cover frontend tests.
func TestBareTestAbsenceStillNeedsCoverage(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		claimGlob("**/*.go", "backend/cmd/main.go\n"),
	}}}
	if verdict := claimVerdict(turns, "No test files are present."); verdict.Complete() {
		t.Fatal("a bare test-file absence was accepted from a Go-only scan")
	}
}

// Frontend test naming is convention-based, so a complete .tsx scan does not
// prove that TypeScript tests are absent. Go's mandatory _test.go suffix is
// the narrow exception above.
func TestFrontendTestAbsenceStaysReview(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		claimGlob("**/*.ts", "src/main.ts\n"),
		claimGlob("**/*.tsx", "src/App.tsx\n"),
	}}}
	if verdict := claimVerdict(turns, "No TypeScript test files are present."); verdict.Complete() {
		t.Fatal("convention-based frontend test absence was accepted from file scans")
	}
}

// Compose declares service topology. It does not prove that cart validates
// against product, that order consumes cart, or that delivery fulfills.
func TestConfigurationTopologyDoesNotProveRuntimeBehavior(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		claimListing(".", "backend\nsrc\n"),
		claimRead("backend/docker-compose.yml",
			"services:\n  cart-service:\n    depends_on:\n      - product-service\n  order-service:\n    depends_on:\n      - cart-service\n", 266),
	}}}
	verdict := claimVerdict(turns,
		"This points to a synchronous gRPC design: cart validates against product, order consumes cart and publishes events, delivery fulfills.")
	if verdict.Complete() {
		t.Fatal("configuration topology was accepted as proof of runtime behavior")
	}
	if !strings.Contains(verdict.Reason, "validates") && !strings.Contains(verdict.Reason, "runtime behavior") {
		t.Fatalf("reason = %q, want the unsupported behavior claim", verdict.Reason)
	}
}

// A source observation is the evidence class that can support a behavior
// claim. The check must not reject behavior categorically.
func TestSourceObservationCanSupportBehaviorClaim(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		claimRead("backend/internal/cart/service/service.go",
			"func (s *Service) ValidateProduct(id string) error { return nil }", 40),
	}}}
	verdict := claimVerdict(turns, "cart validates against product.")
	if !verdict.Complete() {
		t.Fatalf("a source-supported behavior claim was reviewed: %s", verdict.Reason)
	}
}

// Attributing a behavior claim to the documentation is a documentation claim,
// not an observed implementation fact.
func TestDocumentationAttributionIsNotAnObservedBehaviorClaim(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		claimRead("README.md", "cart validates against product", 333),
	}}}
	verdict := claimVerdict(turns, "According to the README, cart validates against product.")
	if !verdict.Complete() {
		t.Fatalf("an attributed documentation claim was reviewed: %s", verdict.Reason)
	}
}

// An explicitly unverified behavior claim uses the lower-assertion option and
// is not presented as an observation.
func TestExplicitlyUnverifiedBehaviorIsNotAnObservation(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		claimRead("backend/docker-compose.yml", "services:\n  cart-service:\n    depends_on:\n      - product-service\n", 266),
	}}}
	verdict := claimVerdict(turns,
		"Compose declares the topology; cart validates against product is not verified.")
	if !verdict.Complete() {
		t.Fatalf("an explicitly unverified behavior claim was reviewed: %s", verdict.Reason)
	}
}

// Separate Compose services are not independent deployment boundaries.
func TestComposeTopologyDoesNotProveIndependentDeployment(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		claimRead("backend/docker-compose.yml",
			"services:\n  product-service:\n    build:\n      dockerfile: docker/product-service/Dockerfile\n  cart-service:\n    build:\n      dockerfile: docker/cart-service/Dockerfile\n", 266),
	}}}
	verdict := claimVerdict(turns,
		"This is a backend composed of multiple independently-deployed microservices.")
	if verdict.Complete() {
		t.Fatal("Compose service declarations were accepted as independent deployment")
	}
	if !strings.Contains(verdict.Reason, "deployment") {
		t.Fatalf("reason = %q, want the deployment boundary", verdict.Reason)
	}
}

// A Dockerfile read is a deployment artifact observation.
func TestDeploymentArtifactSupportsIndependentDeploymentClaim(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		claimRead("backend/docker/product-service/Dockerfile", "FROM golang:1.23-alpine AS build", 20),
	}}}
	verdict := claimVerdict(turns, "The services are independently deployed.")
	if !verdict.Complete() {
		t.Fatalf("an observed deployment artifact was not accepted: %s", verdict.Reason)
	}
}

// The practical hallucination: a byte size printed by ls was promoted to a
// line count for a file the run never read.
func TestUnobservedLineCountIsReviewed(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		claimListing("src", "src\nadmin/\nclient/\ncomponents/\ncontexts/\n    1202  App.tsx\n"),
	}}}
	verdict := claimVerdict(turns,
		"Entry: src/main.tsx → src/App.tsx (~1200 LOC, likely containing most routes/pages).")
	if verdict.Complete() {
		t.Fatal("an unobserved line count was accepted")
	}
	if !strings.Contains(verdict.Reason, "App.tsx") || !strings.Contains(verdict.Reason, "line") {
		t.Fatalf("reason = %q, want the line-count boundary", verdict.Reason)
	}
}

// A read total that contradicts the claimed count is a truthfulness failure,
// not a rounding difference.
func TestContradictedLineCountIsReviewed(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		claimRead("src/App.tsx", "import React from 'react';\nexport default App;", 40),
	}}}
	verdict := claimVerdict(turns, "src/App.tsx is about 1200 lines.")
	if verdict.Complete() {
		t.Fatal("a line count contradicted by the observed read was accepted")
	}
	if !strings.Contains(verdict.Reason, "40") || !strings.Contains(verdict.Reason, "1200") {
		t.Fatalf("reason = %q, want the claimed and observed counts", verdict.Reason)
	}
}

// A line count measured by the read is allowed.
func TestObservedLineCountCompletes(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		claimRead("README.md", "# E-commerce Microservices Platform", 333),
	}}}
	verdict := claimVerdict(turns, "README.md is 333 lines.")
	if !verdict.Complete() {
		t.Fatalf("an observed line count was reviewed: %s", verdict.Reason)
	}
}

// Compose mounting the migrations directory into the PostgreSQL init hook is
// the configuration evidence that supports an auto-apply claim. A Compose file
// without that hook does not.
func TestAutoAppliedMigrationNeedsTheObservedMechanism(t *testing.T) {
	withHook := []Turn{{Results: []ToolOutcome{
		claimRead("backend/docker-compose.yml",
			"postgres:\n  volumes:\n    - ./migrations:/docker-entrypoint-initdb.d\n", 266),
	}}}
	answer := "backend/migrations/ contains SQL migrations auto-applied by Postgres init."
	if verdict := claimVerdict(withHook, answer); !verdict.Complete() {
		t.Fatalf("the observed init hook was not accepted: %s", verdict.Reason)
	}

	withoutHook := []Turn{{Results: []ToolOutcome{
		claimRead("backend/docker-compose.yml", "postgres:\n  image: postgres:15-alpine\n", 266),
	}}}
	if verdict := claimVerdict(withoutHook, answer); verdict.Complete() {
		t.Fatal("an auto-apply claim without the observed mechanism was accepted")
	}
}

// A path-scoped inference about content the run only listed is speculation,
// not an observed fact. An inference supported by the path's observed content
// is accepted.
func TestSpeculationAboutUninspectedPathIsReviewed(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		claimListing("supabase", "supabase\nmigrations/\n"),
	}}}
	verdict := claimVerdict(turns,
		"supabase/ — Supabase config (likely schema/edge functions or an alternative data layer).")
	if verdict.Complete() {
		t.Fatal("path-scoped speculation was accepted as an observed fact")
	}
	if !strings.Contains(verdict.Reason, "supabase") {
		t.Fatalf("reason = %q, want the speculated path", verdict.Reason)
	}

	supported := []Turn{{Results: []ToolOutcome{
		claimListing("supabase", "supabase\nmigrations/\n"),
	}}}
	if verdict := claimVerdict(supported, "supabase/ likely contains migrations."); !verdict.Complete() {
		t.Fatalf("a speculation supported by the path's listing was reviewed: %s", verdict.Reason)
	}
}

// A general inference with no path reference is not path-scoped speculation;
// it is left to the evidence and provenance gates.
func TestUnscopedInferenceIsNotClaimScopeReview(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		claimRead("backend/go.mod", "module ecommerce\nrequire gorm.io/gorm v1.25.5", 52),
	}}}
	verdict := claimVerdict(turns, "The backend likely uses PostgreSQL for persistence.")
	if !verdict.Complete() {
		t.Fatalf("an unscoped inference was reviewed: %s", verdict.Reason)
	}
}

// The practical repository-analysis answer mixed observed manifests and
// Compose topology with an unobserved line count, an independent-deployment
// conclusion, runtime behavior, and path-scoped speculation. All of those are
// review boundaries; the bounded test/CI report is not.
func TestPracticalRepositoryAnalysisReviewsTheUnsupportedClaims(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		claimListing(".", ".\nbackend/\nnode_modules/\nsrc/\nsupabase/\n   19701  README.md\n     868  package.json\n"),
		claimListing("src", "src\nadmin/\nclient/\ncomponents/\ncontexts/\n    1202  App.tsx\n     234  main.tsx\n"),
		claimListing("backend", "backend\ncmd/\ndocker/\ninternal/\nk8s/\nmigrations/\npkg/\n"),
		claimRead("README.md", "# E-commerce Microservices Platform", 333),
		claimRead("package.json", `{"dependencies":{"react":"18"}}`, 35),
		claimListing("supabase", "supabase\nmigrations/\n"),
		claimListing("backend/internal", "backend/internal\norder/\nproduct/\n"),
		claimRead("backend/go.mod", "module ecommerce\nrequire github.com/gin-gonic/gin v1.9.1", 52),
		claimRead("backend/docker-compose.yml",
			"services:\n  product-service:\n    ports:\n      - \"8081:8080\"\n  cart-service:\n    depends_on:\n      - product-service\n  order-service:\n    depends_on:\n      - cart-service\n", 266),
		claimListing("backend/cmd", "backend/cmd\napi-gateway/\ncart-service/\ndelivery-service/\nnotification-service/\norder-service/\nproduct-service/\n"),
	}}}
	answer := strings.Join([]string{
		"This is an E-commerce Microservices Platform with a React frontend and a Go backend composed of multiple independently-deployed microservices.",
		"Entry: src/main.tsx → src/App.tsx (~1200 LOC, likely containing most routes/pages).",
		"This points to a synchronous gRPC + asynchronous RabbitMQ design: cart validates against product, order consumes cart and publishes events, delivery fulfills.",
		"supabase/ — Supabase config (likely schema/edge functions or an alternative data layer).",
		"No test files or CI configuration were surfaced in the probes — worth verifying if you want to assess maturity.",
	}, "\n\n")

	assessment := AssessGrounding(claimContract(), turns, answer)
	if assessment.Pass() {
		t.Fatal("the practical answer's unsupported claims were accepted")
	}
	reason := strings.ToLower(assessment.Reason())
	for _, want := range []string{"independently-deployed", "app.tsx", "validates"} {
		if !strings.Contains(reason, want) {
			t.Fatalf("reason = %q, want %q", assessment.Reason(), want)
		}
	}
	if strings.Contains(reason, "test files") || strings.Contains(reason, "ci configuration") {
		t.Fatalf("the bounded probe report was still treated as an absence claim: %q", assessment.Reason())
	}

	// The completion boundary uses the bounded reason. The key behavior and
	// deployment classes must survive that bound, not only the direct
	// assessment.
	verdict := claimVerdict(turns, answer)
	if verdict.Complete() {
		t.Fatal("the practical answer completed")
	}
	for _, want := range []string{"validates", "independently-deployed", "app.tsx"} {
		if !strings.Contains(strings.ToLower(verdict.Reason), want) {
			t.Fatalf("completion reason = %q, want %q", verdict.Reason, want)
		}
	}
	if strings.Contains(strings.ToLower(verdict.Reason), "test files") {
		t.Fatalf("completion reason retained the bounded probe report: %q", verdict.Reason)
	}
}

// A claim inside a quoted code block is not a prose assertion.
func TestClaimAuditIgnoresFencedCode(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		claimRead("README.md", "# E-commerce Microservices Platform", 333),
	}}}
	answer := "The observed output was:\n```\nsrc/App.tsx (~1200 LOC)\n```\nThe README describes the project."
	verdict := claimVerdict(turns, answer)
	if !verdict.Complete() {
		t.Fatalf("a quoted code block was treated as a claim: %s", verdict.Reason)
	}
}
