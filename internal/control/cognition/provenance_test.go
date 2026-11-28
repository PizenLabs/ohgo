package cognition

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/external/knowledge"
)

func broadShape(facts RepositoryFacts) TaskShape {
	return BuildShape(contract.IntentContract{
		Objective: "analyze what this project is about",
		Kind:      contract.ObjectiveInformational,
	}, ShapeParams{Authority: "ask", Facts: facts})
}

func observeReads(state *EvidenceState, shape TaskShape, reads ...agent.ToolOutcome) {
	turn := agent.Turn{Results: reads}
	state.ObserveTurns(shape, []agent.Turn{turn})
}

// A code project's broad account needs an implementation observation; two
// documentation artifacts describe what the project says about itself.
func TestSurveyRequiresImplementationObservationInCodeRepository(t *testing.T) {
	shape := broadShape(RepositoryFacts{Go: true})
	docs := NewEvidenceState()
	observeReads(docs, shape,
		readEvidenceForTest("read-1", "README.md", "The platform uses PostgreSQL."),
		readEvidenceForTest("read-2", "docs/architecture.md", "The architecture is service oriented."),
	)
	suff := EvaluateSufficiency(shape, docs, ExecutionState{}, false)
	if suff.State == Blocked || suff.Stage == StageAnswer {
		t.Fatalf("documentation-only evidence satisfied a code repository survey: %+v", suff)
	}

	withSource := NewEvidenceState()
	observeReads(withSource, shape,
		readEvidenceForTest("read-1", "README.md", "The platform uses PostgreSQL."),
		readEvidenceForTest("read-2", "main.go", "package main"),
	)
	suff = EvaluateSufficiency(shape, withSource, ExecutionState{}, false)
	if suff.State != Sufficient || suff.Stage != StageAnswer {
		t.Fatalf("a documentation plus implementation survey = %s/%s, want SUFFICIENT/answer", suff.State, suff.Stage)
	}
}

// A Compose file, Makefile, and manifest declare topology and dependencies.
// They are not implementation content, even when the runtime reads them.
func TestConfigurationObservedButNotImplementation(t *testing.T) {
	shape := broadShape(RepositoryFacts{Go: true})
	state := NewEvidenceState()
	observeReads(state, shape,
		readEvidenceForTest("read-1", "README.md", "The platform uses PostgreSQL."),
		readEvidenceForTest("read-2", "backend/docker-compose.yml", "services:\n  product-service:\n    ports:\n      - \"8081:8080\"\n"),
		readEvidenceForTest("read-3", "backend/Makefile", "build:\n\tgo build ./...\n"),
		readEvidenceForTest("read-4", "backend/go.mod", "module ecommerce"),
	)
	state.ObserveOutcome(agent.ToolOutcome{
		CallID:   "structure-overview",
		ToolName: "structure",
		Args:     map[string]any{"view": "overview"},
		Result: agent.Result{
			Text: "view: overview\ntests: 0 (test_files: 0)\n",
			Details: map[string]any{
				"view":                "overview",
				"graph_state":         "CURRENT",
				"complete":            false,
				"partial":             true,
				"truncated":           true,
				"scope_complete":      false,
				"structural_complete": true,
				"test_files":          0,
				"total":               0,
			},
		},
	})
	summary := state.Provenance(shape)
	if summary.HasImplementation {
		t.Fatalf("configuration/manifest evidence was counted as implementation: %+v", summary)
	}
	if !summary.HasConfiguration || !summary.HasDocumentation {
		t.Fatalf("declared evidence was not classified: %+v", summary)
	}
	if summary.HasRuntimeObservation {
		t.Fatalf("a metadata overview was counted as a runtime observation: %+v", summary)
	}
	if summary.StructuralScope != "PARTIAL" {
		t.Fatalf("structural scope = %q, want PARTIAL", summary.StructuralScope)
	}
	if state.Satisfied("repository_survey", ExecutionState{}) {
		t.Fatal("configuration and inventory satisfied a code repository survey")
	}
	suff := EvaluateSufficiency(shape, state, ExecutionState{}, false)
	if suff.State != Insufficient || suff.Stage != StageExplore {
		t.Fatalf("sufficiency = %s/%s, want INSUFFICIENT/explore", suff.State, suff.Stage)
	}
	if !containsString(suff.Missing, "repository_survey") {
		t.Fatalf("missing = %v, want repository_survey", suff.Missing)
	}
	if !strings.Contains(strings.Join(suff.Next, " "), "read the entry point") {
		t.Fatalf("next evidence = %v, want a source-read instruction", suff.Next)
	}
}

// A source read is the content observation that can support a behavior claim.
func TestSourceReadSatisfiesImplementationClaim(t *testing.T) {
	shape := broadShape(RepositoryFacts{Go: true})
	state := NewEvidenceState()
	observeReads(state, shape,
		readEvidenceForTest("read-1", "README.md", "The platform uses PostgreSQL."),
		readEvidenceForTest("read-2", "backend/cmd/product-service/main.go",
			"func main() { http.ListenAndServe(\":8081\", nil) }"),
	)
	summary := state.Provenance(shape)
	if !summary.HasImplementation || len(summary.ImplementationPaths) == 0 {
		t.Fatalf("a source read was not counted as implementation: %+v", summary)
	}
	if !state.Satisfied("repository_survey", ExecutionState{}) {
		t.Fatal("a documentation plus source survey did not satisfy repository_survey")
	}
}

// A broad analysis cannot reach its answer boundary from one content read and
// a directory listing: names without roles are not a survey of the project.
// The survey's structural leg is a structural query that returned coordinates
// or edges; before that rule, "analyze what this project is about" was
// satisfied by reading one file and listing the root.
func TestSurveyIsNotSatisfiedByNamesAndOneRead(t *testing.T) {
	shape := broadShape(RepositoryFacts{Go: true})
	state := NewEvidenceState()
	observeReads(state, shape,
		readEvidenceForTest("read-1", "main.go", "package main\n\nfunc main() {}\n"),
	)
	state.ObserveTurns(shape, []agent.Turn{{Results: []agent.ToolOutcome{{
		CallID: "ls-1", ToolName: "ls", Args: map[string]any{"path": "."},
		Result: agent.Result{
			Text:    "main.go\nREADME.md",
			Details: map[string]any{"path": ".", "entries": 2},
		},
	}}}})
	if state.Satisfied("repository_survey", ExecutionState{}) {
		t.Fatal("one content read and a directory listing satisfied a broad survey")
	}
	suff := EvaluateSufficiency(shape, state, ExecutionState{}, false)
	if !containsString(suff.Missing, "repository_survey") {
		t.Fatalf("missing = %v, want repository_survey", suff.Missing)
	}

	// A structural query that returned the repository's own facts is the
	// structural leg of the survey.
	state.ObserveTurns(shape, []agent.Turn{{Results: []agent.ToolOutcome{{
		CallID: "structure-1", ToolName: "structure",
		Args: map[string]any{"path": "."},
		Result: agent.Result{
			Text: "internal/agent: 12 packages, 153 symbols",
			Details: map[string]any{
				"path": ".", "view": "packages", "packages": 12, "symbols": 153,
			},
		},
	}}}})
	if !state.Satisfied("repository_survey", ExecutionState{}) {
		t.Fatal("a structural query with repository facts did not complete the survey")
	}
}

// A complete test inventory is the only structural observation that supports a
// test-file absence claim. A partial inventory leaves it unproven.
func TestClaimSummaryTracksTestInventoryCompleteness(t *testing.T) {
	complete := NewEvidenceState()
	complete.ObserveOutcome(agent.ToolOutcome{
		CallID:   "structure-tests",
		ToolName: "structure",
		Args:     map[string]any{"view": "tests"},
		Result: agent.Result{
			Text: "view: tests\nreturned: 0\ntotal: 0\n",
			Details: map[string]any{
				"view":                "tests",
				"graph_state":         "CURRENT",
				"complete":            true,
				"partial":             false,
				"truncated":           false,
				"scope_complete":      true,
				"inventory_complete":  true,
				"structural_complete": true,
				"completeness":        "COMPLETE",
				"test_files":          0,
				"total":               0,
			},
		},
	})
	summary := complete.ClaimSummary()
	if summary.TestInventory != "COMPLETE" || !summary.TestFilesKnown || summary.TestFiles != 0 ||
		!summary.TestTotalKnown || summary.TestTotal != 0 {
		t.Fatalf("complete inventory summary = %+v", summary)
	}

	partial := NewEvidenceState()
	partial.ObserveOutcome(agent.ToolOutcome{
		CallID:   "structure-tests",
		ToolName: "structure",
		Args:     map[string]any{"view": "tests"},
		Result: agent.Result{
			Text: "view: tests\nscope: PARTIAL\nreturned: 0\ntotal: 0\n",
			Details: map[string]any{
				"view":                "tests",
				"graph_state":         "CURRENT",
				"complete":            false,
				"partial":             true,
				"truncated":           false,
				"scope_complete":      false,
				"inventory_complete":  true,
				"structural_complete": true,
				"completeness":        "COMPLETE",
				"test_files":          0,
				"total":               0,
			},
		},
	})
	if got := partial.ClaimSummary().TestInventory; got != "PARTIAL" {
		t.Fatalf("partial inventory = %q, want PARTIAL", got)
	}
}

// A checkpoint clone must not alias the bounded counts. Mutating one state
// must not rewrite the other's claim support.
func TestEvidenceCloneDoesNotAliasStructuralCounts(t *testing.T) {
	state := NewEvidenceState()
	state.ObserveOutcome(agent.ToolOutcome{
		CallID:   "structure-tests",
		ToolName: "structure",
		Args:     map[string]any{"view": "tests"},
		Result: agent.Result{
			Text: "view: tests\ntotal: 0\n",
			Details: map[string]any{
				"view": "tests", "graph_state": "CURRENT", "complete": true,
				"scope_complete": true, "structural_complete": true,
				"completeness": "COMPLETE", "test_files": 0, "total": 0,
			},
		},
	})
	item, ok := state.Find("structure:tests")
	if !ok {
		t.Fatal("the structural count observation was not recorded")
	}
	clone := state.Clone()
	cloneItem, ok := clone.Find("structure:tests")
	if !ok {
		t.Fatal("the clone lost the structural count observation")
	}
	cloneItem.Counts["test_files"] = 9
	if item.Counts["test_files"] != 0 {
		t.Fatal("the clone aliased the live count map")
	}
}

// A documentation-only project has no implementation to observe; two
// documentation artifacts are a complete survey.
func TestSurveyAcceptsDocumentationOnlyProject(t *testing.T) {
	shape := broadShape(RepositoryFacts{})
	state := NewEvidenceState()
	observeReads(state, shape,
		readEvidenceForTest("read-1", "README.md", "A documentation project."),
		readEvidenceForTest("read-2", "docs/guide.md", "How to use it."),
	)
	suff := EvaluateSufficiency(shape, state, ExecutionState{}, false)
	if suff.State != Sufficient || suff.Stage != StageAnswer {
		t.Fatalf("a documentation-only survey = %s/%s, want SUFFICIENT/answer", suff.State, suff.Stage)
	}
}

// Vendored and generated content is evidence, but it is not the project's own
// implementation.
func TestGeneratedAndVendoredContentDoNotCountAsImplementation(t *testing.T) {
	shape := broadShape(RepositoryFacts{Go: true})
	state := NewEvidenceState()
	observeReads(state, shape,
		readEvidenceForTest("read-1", "README.md", "The platform uses PostgreSQL."),
		readEvidenceForTest("read-2", "vendor/example/client.go", "package client"),
		readEvidenceForTest("read-3", "api/service.pb.go", "// Code generated by protoc."),
	)
	suff := EvaluateSufficiency(shape, state, ExecutionState{}, false)
	if suff.Stage == StageAnswer {
		t.Fatalf("vendored/generated content satisfied an implementation survey: %+v", suff)
	}
}

// The provenance summary subtracts terms observed in the implementation, so a
// source-observed term is not treated as a documentation-only claim.
func TestProvenanceSummarySubtractsObservedImplementationTerms(t *testing.T) {
	shape := broadShape(RepositoryFacts{Go: true})
	state := NewEvidenceState()
	observeReads(state, shape,
		readEvidenceForTest("read-1", "README.md", "The platform uses PostgreSQL and Redis for storage."),
		readEvidenceForTest("read-2", "main.go", "package main\n// PostgreSQL is the primary store."),
	)
	summary := state.Provenance(shape)
	if !summary.RequireAttribution || !summary.HasDocumentation || !summary.HasImplementation {
		t.Fatalf("provenance summary = %+v, want a broad documented+implementation account", summary)
	}
	for _, term := range summary.DocumentationTerms {
		if term == "postgresql" {
			t.Fatalf("a source-observed term was reported as documentation-only: %+v", summary.DocumentationTerms)
		}
	}
	foundRedis := false
	for _, term := range summary.DocumentationTerms {
		if term == "redis" {
			foundRedis = true
		}
	}
	if !foundRedis {
		t.Fatalf("a documentation-only term was lost: %+v", summary.DocumentationTerms)
	}
}

// A documentation-only warning is current state. Once the implementation
// corroborates the same term, the warning must clear and the same answer must
// pass provenance; keeping it would punish the run for evidence it later
// acquired.
func TestAnswerProvenanceClearsAfterImplementationCorroboration(t *testing.T) {
	shape := broadShape(RepositoryFacts{Go: true})
	state := NewEvidenceState()
	observeReads(state, shape,
		readEvidenceForTest("read-1", "README.md", "The platform uses Redis for storage."),
	)
	answer := "The project uses Redis for storage."
	before := state.Provenance(shape)
	if assessment := agent.AssessAnswerProvenance(contract.IntentContract{
		Objective: shape.Objective,
		Kind:      contract.ObjectiveInformational,
	}, answer, before); assessment.Pass() {
		t.Fatalf("a documentation-only claim passed before implementation evidence: %+v", before)
	}

	observeReads(state, shape,
		readEvidenceForTest("read-2", "main.go", "package main\n// redis client setup"),
	)
	after := state.Provenance(shape)
	for _, term := range after.DocumentationTerms {
		if term == "redis" {
			t.Fatalf("a corroborated term remained documentation-only: %+v", after.DocumentationTerms)
		}
	}
	if assessment := agent.AssessAnswerProvenance(contract.IntentContract{
		Objective: shape.Objective,
		Kind:      contract.ObjectiveInformational,
	}, answer, after); !assessment.Pass() {
		t.Fatalf("a corroborated answer was still rejected: %s", assessment.Reason())
	}
}

// The project's own title is identity, and generic nouns are not claims; the
// documentation vocabulary keeps only specific proper names and metrics.
func TestDocumentationTitleAndGenericNounsAreNotClaims(t *testing.T) {
	shape := broadShape(RepositoryFacts{Go: true})
	state := NewEvidenceState()
	observeReads(state, shape,
		readEvidenceForTest("read-1", "README.md",
			"# Acme Cloud\n\nAcme Cloud is a Go microservices platform with PostgreSQL and Redis. It handles 100+ requests per second."),
		readEvidenceForTest("read-2", "main.go", "package main"),
	)
	summary := state.Provenance(shape)
	for _, term := range summary.DocumentationTerms {
		switch term {
		case "acme", "cloud", "microservices", "platform", "go":
			t.Fatalf("non-claim term %q entered the documentation vocabulary: %+v", term, summary.DocumentationTerms)
		}
	}
	// Specific claims survive.
	found := false
	for _, term := range summary.DocumentationTerms {
		if term == "postgresql" {
			found = true
		}
	}
	if !found {
		t.Fatalf("a specific documentation claim was lost: %+v", summary.DocumentationTerms)
	}
	foundMetric := false
	for _, term := range summary.DocumentationTerms {
		if term == "100" || term == "100+" || term == "99.9" || term == "99.9%" {
			foundMetric = true
		}
	}
	if !foundMetric {
		t.Fatalf("a documentation metric was lost: %+v", summary.DocumentationTerms)
	}
}

// A README heading is often the repository slug while the project name appears
// only in the opening definition. That name is identity — documentation is the
// evidence class for a project-identity claim — so naming it must not veto an
// otherwise grounded survey. The exemption must not widen to the document's
// technical vocabulary.
func TestRepositorySlugHeadingKeepsProjectIdentityOutOfClaims(t *testing.T) {
	shape := broadShape(RepositoryFacts{Go: true})
	state := NewEvidenceState()
	observeReads(state, shape,
		readEvidenceForTest("read-1", "README.md",
			"# go-ecommerce\n\nGoCommerce is an e-commerce application written in Go and HTMX. The backend uses PostgreSQL."),
		readEvidenceForTest("read-2", "main.go", "package main"),
	)
	summary := state.Provenance(shape)
	for _, term := range summary.DocumentationTerms {
		if term == "gocommerce" {
			t.Fatalf("the project's own name was audited as an implementation claim: %+v", summary.DocumentationTerms)
		}
	}
	foundTech := false
	for _, term := range summary.DocumentationTerms {
		if term == "postgresql" {
			foundTech = true
		}
	}
	if !foundTech {
		t.Fatalf("the identity exemption swallowed a documented implementation claim: %+v", summary.DocumentationTerms)
	}

	intent := contract.IntentContract{
		Kind:      contract.ObjectiveInformational,
		Objective: "check this project and analyze what this project is about?",
	}
	if assessment := agent.AssessAnswerProvenance(intent, "This project is GoCommerce.", summary); !assessment.Pass() {
		t.Fatalf("naming the documented project identity was rejected: %s", assessment.Reason())
	}
	if assessment := agent.AssessAnswerProvenance(intent, "The backend uses PostgreSQL.", summary); assessment.Pass() {
		t.Fatal("an unattributed documented implementation claim passed")
	}
}

// The production observation value is the read tool's line-numbered display
// text. The identity scan must see the README's opening definition through
// that gutter, or the project's own name is audited as an unobserved
// implementation claim and a truthful survey is vetoed.
func TestLineNumberedReadKeepsDocumentIdentityOutOfClaims(t *testing.T) {
	shape := broadShape(RepositoryFacts{Go: true})
	state := NewEvidenceState()
	document := []string{
		"# go-ecommerce",
		"",
		"GoCommerce is an e-commerce application written in Go and HTMX.",
	}
	for len(document) < 120 {
		document = append(document, "Filler line.")
	}
	document = append(document, "The backend uses PostgreSQL.")
	observeReads(state, shape,
		readEvidenceForTest("read-1", "README.md", numberedRead(document...)),
		readEvidenceForTest("read-2", "main.go", numberedRead("package main")),
	)
	summary := state.Provenance(shape)
	for _, term := range summary.DocumentationTerms {
		if term == "gocommerce" {
			t.Fatalf("the project's own name was audited as an implementation claim: %+v", summary.DocumentationTerms)
		}
		if strings.Trim(term, "0123456789") == "" {
			t.Fatalf("a read-gutter line number was audited as a documentation metric: %+v", summary.DocumentationTerms)
		}
	}
	foundTech := false
	for _, term := range summary.DocumentationTerms {
		if term == "postgresql" {
			foundTech = true
		}
	}
	if !foundTech {
		t.Fatalf("the documented implementation claim was lost: %+v", summary.DocumentationTerms)
	}

	intent := contract.IntentContract{
		Kind:      contract.ObjectiveInformational,
		Objective: "check this project and analyze what this project is about?",
	}
	if assessment := agent.AssessAnswerProvenance(intent, "This project is GoCommerce.", summary); !assessment.Pass() {
		t.Fatalf("naming the documented project identity was rejected: %s", assessment.Reason())
	}
	if assessment := agent.AssessAnswerProvenance(intent, "The backend uses PostgreSQL.", summary); assessment.Pass() {
		t.Fatal("an unattributed documented implementation claim passed")
	}
}

// numberedRead renders lines in the read tool's production format: a 1-based
// line number, a tab, then the line.
func numberedRead(lines ...string) string {
	var b strings.Builder
	for i, line := range lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "%d\t%s", i+1, line)
	}
	return b.String()
}

// Manifest dependency tokens are observed facts even when they are embedded in
// a path-style module line.
func TestManifestDependencyTermsAreObserved(t *testing.T) {
	shape := broadShape(RepositoryFacts{Go: true})
	state := NewEvidenceState()
	observeReads(state, shape,
		readEvidenceForTest("read-1", "README.md", "# App\n\nA Gin framework template with SQLite storage."),
		readEvidenceForTest("read-2", "go.mod", "module app\n\nrequire github.com/gin-gonic/gin v1.12.0\ngithub.com/mattn/go-sqlite3 v1.14.48"),
		readEvidenceForTest("read-3", "main.go", "package main"),
	)
	summary := state.Provenance(shape)
	for _, term := range summary.DocumentationTerms {
		if term == "gin" || term == "gonic" || term == "sqlite" {
			t.Fatalf("a manifest dependency was reported as documentation-only: %+v", summary.DocumentationTerms)
		}
	}
}

// MCP discovery names capabilities but observes no knowledge; it must not
// satisfy an explicit external-knowledge requirement.
func TestMCPDiscoveryIsNotExternalObservation(t *testing.T) {
	shape := BuildShape(contract.IntentContract{
		Objective: "check this project and use gry mcp",
		Kind:      contract.ObjectiveInformational,
	}, ShapeParams{Authority: "ask", Facts: RepositoryFacts{Go: true}})
	state := NewEvidenceState()
	state.ObserveOutcome(agent.ToolOutcome{
		CallID: "list-1", ToolName: "mcp_list",
		Result: agent.Result{
			Text: "server: gry\ndisclosed: 20",
			Details: map[string]any{
				"capability":     string(knowledge.CapabilityMCPDiscovery),
				"discovery_only": true,
			},
		},
	})
	if summary := state.Provenance(shape); summary.ExternalObserved {
		t.Fatalf("discovery satisfied the external requirement: %+v", summary)
	}
	state.ObserveOutcome(agent.ToolOutcome{
		CallID: "read-1", ToolName: "mcp_read",
		Result: agent.Result{
			Text: "MCP tool result: source",
			Details: map[string]any{
				"status": "ok",
				"evidence": []knowledge.Evidence{{
					Kind: knowledge.EvidenceMCPToolResult,
					Provenance: knowledge.Provenance{
						Capability: knowledge.CapabilityMCPReadTool,
						Source:     "mcp:gry",
						Provider:   "gry",
					},
				}},
			},
		},
	})
	if summary := state.Provenance(shape); !summary.ExternalObserved {
		t.Fatalf("a successful MCP read did not satisfy the external requirement: %+v", summary)
	}
}

// A nested module manifest read by the agent establishes the implementation
// language even when the root probe saw only the frontend package.json. Without
// it, "Go 1.23" read from backend/go.mod is reclassified as a documentation-only
// claim and completion is vetoed.
func TestNestedManifestEstablishesImplementationLanguage(t *testing.T) {
	shape := broadShape(RepositoryFacts{JavaScript: true})
	state := NewEvidenceState()
	observeReads(state, shape,
		readEvidenceForTest("read-1", "README.md",
			"# go-ecommerce\n\nA full-stack platform with a Golang microservices backend.\nThe backend uses Go 1.23.0."),
		readEvidenceForTest("read-2", "backend/go.mod", "module ecommerce\n\ngo 1.23.0\n"),
		readEvidenceForTest("read-3", "backend/cmd/product/main.go", "package main"),
	)
	summary := state.Provenance(shape)
	for _, term := range summary.DocumentationTerms {
		if term == "go" || term == "golang" {
			t.Fatalf("a manifest-observed language was reported as documentation-only: %+v", summary.DocumentationTerms)
		}
	}
	answer := "Backend (`backend/`, Go 1.23.0, module `ecommerce`): six service entrypoints."
	if assessment := agent.AssessAnswerProvenance(contract.IntentContract{
		Objective: "analyze what this project is about",
		Kind:      contract.ObjectiveInformational,
	}, answer, summary); !assessment.Pass() {
		t.Fatalf("an answer grounded in the read manifest was rejected: %s", assessment.Reason())
	}
}

// Path classification is deterministic and keeps README, manifests, source,
// and generated content distinct.
func TestPathProvenanceClassification(t *testing.T) {
	cases := map[string]Provenance{
		"README.md":                   ProvenanceDocumentation,
		"docs/architecture.md":        ProvenanceDocumentation,
		"LICENSE":                     ProvenanceDocumentation,
		"go.mod":                      ProvenanceManifest,
		"package.json":                ProvenanceManifest,
		"main.go":                     ProvenanceObservation,
		"internal/agent/loop.go":      ProvenanceObservation,
		"vendor/lib/client.go":        ProvenanceGenerated,
		"api/service.pb.go":           ProvenanceGenerated,
		"web/app.min.js":              ProvenanceGenerated,
		"config/settings.yaml":        ProvenanceConfiguration,
		"docker-compose.yml":          ProvenanceConfiguration,
		"Makefile":                    ProvenanceConfiguration,
		"docs/requirements.txt":       ProvenanceManifest,
		"docs/guide.markdown":         ProvenanceDocumentation,
		"internal/agent/loop_test.go": ProvenanceObservation,
	}
	for path, want := range cases {
		if got := classifyPathProvenance(path); got != want {
			t.Errorf("classifyPathProvenance(%q) = %s, want %s", path, got, want)
		}
	}
}

// Documentation claim terms must not include a bare small integer. A README
// line such as "React 18" contributes its named technology; the number alone
// would match unrelated counts in an answer and cause a false review.
func TestBareSmallIntegerIsNotADocumentationClaimTerm(t *testing.T) {
	terms := documentationClaimTerms("The app uses React 18 with 18 build scripts.")
	for _, term := range terms {
		if term == "18" {
			t.Fatalf("documentation terms = %v, want the bare count excluded", terms)
		}
	}
	for _, want := range []string{"react"} {
		found := false
		for _, term := range terms {
			if term == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("documentation terms = %v, want %q retained", terms, want)
		}
	}

	// Concrete metrics remain claims.
	terms = documentationClaimTerms("It handles 100+ requests per second at 99.9% uptime.")
	for _, want := range []string{"100+", "99.9%"} {
		found := false
		for _, term := range terms {
			if term == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("documentation terms = %v, want %q retained", terms, want)
		}
	}
}

// A capitalized domain noun in a heading or table header is ordinary project
// language, not a specific implementation claim. "Shopping Cart Service" must
// not make the word "shopping" a documentation term that any overview answer
// then has to attribute.
func TestDomainHeadingIsNotAProvenanceClaim(t *testing.T) {
	terms := documentationClaimTerms("### Shopping Cart Service\n\n| Shopping Cart | Redis |\n|---|---|\n")
	for _, term := range terms {
		if term == "shopping" || term == "cart" {
			t.Fatalf("documentation terms = %v, want the domain heading excluded", terms)
		}
	}
}

// The source uses the short spelling "Postgres" while the README uses the
// formal "PostgreSQL". They name one dependency; a source read of either must
// clear the documentation-only term.
func TestPostgresSpellingVariantIsObserved(t *testing.T) {
	shape := broadShape(RepositoryFacts{Go: true})
	state := NewEvidenceState()
	observeReads(state, shape,
		readEvidenceForTest("read-1", "README.md", "The platform uses PostgreSQL."),
		readEvidenceForTest("read-2", "backend/internal/product/repository/repository.go",
			"package repository\n\nimport (\n\t\"gorm.io/driver/postgres\"\n)\n"),
	)
	summary := state.Provenance(shape)
	if !summary.RequireAttribution {
		t.Fatalf("provenance = %+v, want a broad account requiring attribution", summary)
	}
	for _, term := range summary.DocumentationTerms {
		if term == "postgresql" {
			t.Fatalf("a source-observed spelling variant remained documentation-only: %v", summary.DocumentationTerms)
		}
	}
}

// Documentation stays documentation after the persisted evidence ledger is
// reloaded. The ledger is written to progress.json at every checkpoint, so a
// field missing from its wire form would let a resumed answer present a README
// claim as an observed implementation fact.
func TestProvenanceSurvivesThePersistedEvidenceLedger(t *testing.T) {
	shape := broadShape(RepositoryFacts{Go: true})
	state := NewEvidenceState()
	observeReads(state, shape,
		readEvidenceForTest("read-1", "README.md", "The platform uses PostgreSQL and HTMX."),
		readEvidenceForTest("read-2", "main.go", "package main"),
	)
	before := state.Provenance(shape)
	if !before.HasDocumentation || !before.HasImplementation || !before.RequireAttribution {
		t.Fatalf("setup provenance = %+v, want documentation + implementation in a broad account", before)
	}
	found := false
	for _, term := range before.DocumentationTerms {
		if term == "postgresql" {
			found = true
		}
	}
	if !found {
		t.Fatalf("documentation terms = %v, want postgresql documented-only", before.DocumentationTerms)
	}

	data, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal evidence state: %v", err)
	}
	var restored EvidenceState
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("unmarshal evidence state: %v", err)
	}
	after := restored.Provenance(shape)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("provenance changed across persistence:\nbefore %+v\nafter  %+v", before, after)
	}
}

func shellReadEvidenceForTest(id, command, text string) agent.ToolOutcome {
	return agent.ToolOutcome{
		CallID:   id,
		ToolName: "bash",
		Args:     map[string]any{"command": command},
		Result:   agent.Result{Text: text, Executes: true, Finished: true},
	}
}

// A source file paged or concatenated through the shell is still source
// content. The requirement ledger must not keep asking for a read the model
// already performed; it only needs a second content observation for a survey.
func TestShellSourceReadSatisfiesRepositorySurvey(t *testing.T) {
	shape := broadShape(RepositoryFacts{Go: true})
	state := NewEvidenceState()
	observeReads(state, shape,
		readEvidenceForTest("read-1", "README.md", "The platform uses PostgreSQL."),
		shellReadEvidenceForTest("bash-1", "cat backend/cmd/web/main.go",
			"package main\n\nfunc main() { http.ListenAndServe(\":8080\", nil) }\n"),
	)
	summary := state.Provenance(shape)
	if !summary.HasImplementation {
		t.Fatalf("a shell source read was not implementation evidence: %+v", summary)
	}
	if !state.Satisfied("repository_survey", ExecutionState{}) {
		t.Fatal("a shell source read did not satisfy repository_survey")
	}
}

// A shell read of documentation is content, but it is not implementation
// evidence: reading README.md twice does not describe what the source does.
func TestShellDocumentationReadIsNotImplementation(t *testing.T) {
	shape := broadShape(RepositoryFacts{Go: true})
	state := NewEvidenceState()
	observeReads(state, shape,
		readEvidenceForTest("read-1", "README.md", "The platform uses PostgreSQL."),
		shellReadEvidenceForTest("bash-1", "cat docs/architecture.md", "# Architecture\n"),
	)
	if state.Provenance(shape).HasImplementation {
		t.Fatal("a shell documentation read was counted as implementation")
	}
}

// A large source file has more distinct tokens than the documentation
// vocabulary output cap. The comparison must scan the whole retained value, or
// a driver named after line 141 is misclassified as documentation-only and the
// completion audit vetoes a source-verified claim.
func TestObservedVocabularyIsNotCappedByTheDocumentationOutputBound(t *testing.T) {
	shape := broadShape(RepositoryFacts{Go: true})
	state := NewEvidenceState()
	var source strings.Builder
	for index := 0; index < 300; index++ {
		fmt.Fprintf(&source, "filler%03d ", index)
	}
	source.WriteString(`db, err := otelsql.Open("postgres", connString)`)
	observeReads(state, shape,
		readEvidenceForTest("read-doc", "README.md", "The application stores its data in Postgres."),
		readEvidenceForTest("read-src", "main.go", source.String()),
	)
	for _, term := range state.Provenance(shape).DocumentationTerms {
		if term == "postgres" {
			t.Fatal("a term observed past the documentation output bound was reported as documentation-only")
		}
	}
}

// A second page of a document must not erase the opening definition. The
// project's own name lives on line 3; when a later read overwrote the item, the
// name became "documentation-only" and the completion audit vetoed an answer
// that merely repeated it.
func TestPagedDocumentKeepsItsOpeningIdentity(t *testing.T) {
	shape := broadShape(RepositoryFacts{Go: true})
	state := NewEvidenceState()
	read := func(id string, offset, limit int, text string) agent.ToolOutcome {
		return agent.ToolOutcome{
			CallID: id, ToolName: "read", Args: map[string]any{"path": "README.md"},
			Result: agent.Result{Text: text, Details: map[string]any{
				"path": "README.md", "totalLines": 100, "offset": offset, "limit": limit,
			}},
		}
	}
	observeReads(state, shape,
		read("read-1", 1, 50, "# go-ecommerce\n\nGoCommerce is an e-commerce application written in Go.\n"),
		read("read-2", 51, 50, "## Later page\n\nThe rest of the documentation.\n"),
	)
	for _, term := range state.Provenance(shape).DocumentationTerms {
		if term == "gocommerce" {
			t.Fatal("the paged read lost the document's opening identity")
		}
	}
}

// A structural inventory is names and counts, not a model of the code. A
// broad source project needs the entry point plus one implementation file the
// entry leads to; the parsed structure does not substitute for the second read.
func TestSurveyRequiresASecondImplementationObservation(t *testing.T) {
	shape := broadShape(RepositoryFacts{TypeScript: true})
	state := NewEvidenceState()
	observeReads(state, shape,
		readEvidenceForTest("read-1", "app/page.tsx",
			"import { loadPosts } from \"@/lib/posts\"\nexport default function Page() { return loadPosts() }\n"),
	)
	state.ObserveTurns(shape, []agent.Turn{{Results: []agent.ToolOutcome{{
		CallID: "structure-1", ToolName: "structure",
		Args: map[string]any{"path": ".", "view": "packages"},
		Result: agent.Result{
			Text: "app: 2 packages, 12 symbols",
			Details: map[string]any{
				"path": ".", "view": "packages", "packages": 2, "symbols": 12,
				"source_files": 77,
			},
		},
	}}}})
	if state.Satisfied("repository_survey", ExecutionState{}) {
		t.Fatal("one implementation read plus parsed structure satisfied a broad survey")
	}
	suff := EvaluateSufficiency(shape, state, ExecutionState{}, false)
	if !containsString(suff.Missing, "repository_survey") {
		t.Fatalf("missing = %v, want repository_survey", suff.Missing)
	}

	// Following the import named by the entry is the second observation.
	observeReads(state, shape,
		readEvidenceForTest("read-2", "lib/posts.ts",
			"export async function loadPosts() { return [] }\n"),
	)
	if !state.Satisfied("repository_survey", ExecutionState{}) {
		t.Fatal("the entry point plus a followed dependency did not complete the survey")
	}
}

// A project with exactly one source file is fully observed by reading it; the
// second-observation rule must not invent a file that does not exist.
func TestSurveyExemptsASingleSourceFileProject(t *testing.T) {
	shape := broadShape(RepositoryFacts{Go: true})
	state := NewEvidenceState()
	observeReads(state, shape,
		readEvidenceForTest("read-1", "main.go", "package main\nfunc main() {}\n"),
	)
	state.ObserveTurns(shape, []agent.Turn{{Results: []agent.ToolOutcome{{
		CallID: "structure-1", ToolName: "structure",
		Args: map[string]any{"path": ".", "view": "files"},
		Result: agent.Result{
			Text:    "files: 1 (source: 1)",
			Details: map[string]any{"path": ".", "view": "files", "source_files": 1},
		},
	}}}})
	if !state.Satisfied("repository_survey", ExecutionState{}) {
		t.Fatal("a single-source-file project was required to read a second file")
	}
}

// A repository-wide grep is source evidence for the files it matched, not for
// the directory it searched. The old classification used the search scope, so
// an unscoped grep left every documented term in the "documentation-only"
// vocabulary and the answer review demanded another search that could never
// change the verdict.
func TestRepositoryGrepCorroboratesDocumentedTerms(t *testing.T) {
	shape := broadShape(RepositoryFacts{Go: true})
	state := NewEvidenceState()
	observeReads(state, shape,
		readEvidenceForTest("read-1", "README.md", "1\t# go-ecommerce\n2\tGoCommerce uses HTMX and RabbitMQ.\n"),
		readEvidenceForTest("read-2", "backend/cmd/web/main.go", "package main\n\nfunc main() {}\n"),
	)
	before := state.Provenance(shape)
	for _, term := range []string{"htmx", "rabbitmq"} {
		found := false
		for _, documented := range before.DocumentationTerms {
			if documented == term {
				found = true
			}
		}
		if !found {
			t.Fatalf("before the grep: %q was not documentation-only: %v", term, before.DocumentationTerms)
		}
	}
	state.ObserveOutcome(agent.ToolOutcome{
		CallID: "grep-1", ToolName: "grep",
		Args: map[string]any{"pattern": "HTMX|RabbitMQ"},
		Result: agent.Result{
			Text: "README.md:\n  2: GoCommerce uses HTMX and RabbitMQ.\n\n" +
				"backend/layout/http.go:\n  119: // htmx posts reach this handler.\n\n" +
				"2 file(s) matched\n",
			Details: map[string]any{"pattern": "HTMX|RabbitMQ", "files": 2},
		},
	})
	after := state.Provenance(shape)
	joined := strings.Join(after.DocumentationTerms, "\x00")
	if strings.Contains(joined, "htmx") {
		t.Fatalf("a source-matched term remained documentation-only: %v", after.DocumentationTerms)
	}
	if !strings.Contains(joined, "rabbitmq") {
		t.Fatalf("a documentation-only term was cleared by an unrelated source section: %v", after.DocumentationTerms)
	}
}

// A grep that matched only documentation is not implementation evidence. Its
// rendered file sections must not be treated as source content.
func TestDocumentationGrepIsNotImplementationEvidence(t *testing.T) {
	shape := broadShape(RepositoryFacts{Go: true})
	state := NewEvidenceState()
	observeReads(state, shape,
		readEvidenceForTest("read-1", "README.md", "1\t# go-ecommerce\n2\tGoCommerce uses HTMX.\n"),
		readEvidenceForTest("read-2", "backend/cmd/web/main.go", "package main\n\nfunc main() {}\n"),
	)
	before := state.Provenance(shape)
	state.ObserveOutcome(agent.ToolOutcome{
		CallID: "grep-1", ToolName: "grep",
		Args: map[string]any{"pattern": "HTMX"},
		Result: agent.Result{
			Text:    "README.md:\n  2: GoCommerce uses HTMX.\n\ndocs/stack.md:\n  9: HTMX renders the storefront.\n\n2 file(s) matched\n",
			Details: map[string]any{"pattern": "HTMX", "files": 2},
		},
	})
	after := state.Provenance(shape)
	if !reflect.DeepEqual(before.DocumentationTerms, after.DocumentationTerms) {
		t.Fatalf("a documentation-only grep changed the provenance vocabulary: %v -> %v",
			before.DocumentationTerms, after.DocumentationTerms)
	}
}

// A source identifier can name a dependency whose documented spelling is not
// a whole token in the identifier. "NewRedisClient" and
// "NewPostgresConnection" are implementation evidence for Redis and
// PostgreSQL; treating them as documentation-only forces the answer to
// downgrade an observed source fact.
func TestCamelCaseSourceIdentifierCorroboratesDocumentedDependency(t *testing.T) {
	shape := broadShape(RepositoryFacts{Go: true})
	state := NewEvidenceState()
	observeReads(state, shape,
		readEvidenceForTest("read-1", "README.md",
			"The platform uses PostgreSQL for storage and Redis for caching."),
		readEvidenceForTest("read-2", "backend/internal/product/app.go",
			"package product\n\nfunc main() {\n\tdb := database.NewPostgresConnection(cfg)\n\tcache := redis.NewRedisClient(cfg)\n\t_ = db\n\t_ = cache\n}\n"),
	)
	summary := state.Provenance(shape)
	if !summary.HasImplementation {
		t.Fatalf("source read was not implementation evidence: %+v", summary)
	}
	for _, term := range summary.DocumentationTerms {
		if term == "postgresql" || term == "redis" {
			t.Fatalf("a source-observed dependency remained documentation-only: %v", summary.DocumentationTerms)
		}
	}
	answer := "The product service opens a database connection and a Redis client in its startup path."
	if assessment := agent.AssessAnswerProvenance(contract.IntentContract{
		Objective: shape.Objective,
		Kind:      contract.ObjectiveInformational,
	}, answer, summary); !assessment.Pass() {
		t.Fatalf("an answer grounded in the observed source was rejected: %s", assessment.Reason())
	}
}
