package agent

import (
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

func provenanceContract(objective string) contract.IntentContract {
	return contract.IntentContract{
		Kind:      contract.ObjectiveInformational,
		Objective: objective,
	}
}

// A documentation-only term stated as an observed fact is a review boundary.
func TestDocumentationClaimRequiresAttribution(t *testing.T) {
	provenance := EvidenceProvenance{
		RequireAttribution: true,
		HasDocumentation:   true,
		DocumentationTerms: []string{"postgresql", "redis", "kubernetes"},
	}
	answer := "The platform uses PostgreSQL, Redis, and Kubernetes."
	assessment := AssessAnswerProvenance(provenanceContract("analyze this project"), answer, provenance)
	if assessment.Pass() {
		t.Fatal("an unattributed documentation claim passed as observed fact")
	}
	if !strings.Contains(assessment.Reason(), "documentation-only term") {
		t.Fatalf("reason = %q, want a documentation-provenance reason", assessment.Reason())
	}

	// A lowercase spelling is the same claim.
	answer = "The project uses postgresql."
	if assessment := AssessAnswerProvenance(provenanceContract("analyze this project"), answer, provenance); assessment.Pass() {
		t.Fatal("a lower-case documentation claim passed as observed fact")
	}

	// A concrete metric is a claim even though it is not a proper name.
	provenance.DocumentationTerms = []string{"99.9", "100"}
	answer = "It handles 100+ requests per second with 99.9% uptime."
	if assessment := AssessAnswerProvenance(provenanceContract("analyze this project"), answer, provenance); assessment.Pass() {
		t.Fatal("an unattributed documentation metric passed as observed fact")
	}
}

// A heading that attributes a section covers the claims under it, and the next
// unattributed heading ends that provenance. This is how a truthful analysis
// separates a README summary from observed implementation facts.
func TestAttributedSectionCoversItsClaims(t *testing.T) {
	provenance := EvidenceProvenance{
		RequireAttribution: true,
		HasDocumentation:   true,
		DocumentationTerms: []string{"postgresql", "redis", "react"},
	}
	answer := `## What the README Claims

- Backend: Go
- Frontend: React
- Infrastructure: PostgreSQL, Redis

## What Actually Exists

There is no React frontend and no PostgreSQL configuration; the source is a single HTTP server.`
	if assessment := AssessAnswerProvenance(provenanceContract("analyze this project"), answer, provenance); !assessment.Pass() {
		t.Fatalf("a section-attributed analysis was rejected: %s", assessment.Reason())
	}
}

// Asserting that a documented claim is absent is not promoting it to fact.
func TestAbsenceAssertionIsNotADocumentationClaim(t *testing.T) {
	provenance := EvidenceProvenance{
		RequireAttribution: true,
		HasDocumentation:   true,
		DocumentationTerms: []string{"postgresql", "redis"},
	}
	answer := "PostgreSQL is not present in the repository, and Redis is not configured."
	if assessment := AssessAnswerProvenance(provenanceContract("analyze this project"), answer, provenance); !assessment.Pass() {
		t.Fatalf("an absence assertion was rejected: %s", assessment.Reason())
	}
}

// Naming the documentation source is enough: the claim remains a claim about
// what the project says, not a fabricated implementation observation.
func TestAttributedDocumentationClaimPasses(t *testing.T) {
	provenance := EvidenceProvenance{
		RequireAttribution: true,
		HasDocumentation:   true,
		DocumentationTerms: []string{"postgresql", "redis", "kubernetes"},
	}
	answer := "According to the README, the platform uses PostgreSQL, Redis, and Kubernetes."
	if assessment := AssessAnswerProvenance(provenanceContract("analyze this project"), answer, provenance); !assessment.Pass() {
		t.Fatalf("an attributed documentation claim was rejected: %s", assessment.Reason())
	}
	// Attribution in the preceding sentence also covers a following claim.
	answer = "The README describes the platform's stack. It uses PostgreSQL, Redis, and Kubernetes."
	if assessment := AssessAnswerProvenance(provenanceContract("analyze this project"), answer, provenance); !assessment.Pass() {
		t.Fatalf("a claim covered by the preceding attribution was rejected: %s", assessment.Reason())
	}
}

// A term observed in the implementation is not documentation-only; the answer
// may state it without attribution.
func TestObservedImplementationTermPasses(t *testing.T) {
	provenance := EvidenceProvenance{
		RequireAttribution: true,
		HasDocumentation:   true,
		HasImplementation:  true,
		DocumentationTerms: []string{}, // the term was observed in source
	}
	answer := "The service uses PostgreSQL."
	if assessment := AssessAnswerProvenance(provenanceContract("analyze this project"), answer, provenance); !assessment.Pass() {
		t.Fatalf("an observed implementation term was rejected: %s", assessment.Reason())
	}
}

// A generic answer with no specific term is not a documentation claim.
func TestGenericAnswerHasNoProvenanceClaim(t *testing.T) {
	provenance := EvidenceProvenance{
		RequireAttribution: true,
		HasDocumentation:   true,
		DocumentationTerms: []string{"postgresql", "kubernetes"},
	}
	answer := "The answer is grounded in the observed files."
	if assessment := AssessAnswerProvenance(provenanceContract("analyze this project"), answer, provenance); !assessment.Pass() {
		t.Fatalf("a generic answer was rejected: %s", assessment.Reason())
	}
	// Generic project nouns and their plurals are not specific claims even if
	// a documentation heading capitalised them.
	provenance.DocumentationTerms = []string{"environment", "apis", "frameworks"}
	answer = "Environment variable management and APIs are supported by the frameworks."
	if assessment := AssessAnswerProvenance(provenanceContract("analyze this project"), answer, provenance); !assessment.Pass() {
		t.Fatalf("a generic-noun answer was rejected: %s", assessment.Reason())
	}
}

// The provenance check applies only to a broad repository account. A targeted
// question or a non-repository answer is not forced to attribute.
func TestProvenanceOnlyAppliesToBroadRepositoryAccounts(t *testing.T) {
	provenance := EvidenceProvenance{
		RequireAttribution: false,
		HasDocumentation:   true,
		DocumentationTerms: []string{"postgresql"},
	}
	answer := "The project uses PostgreSQL."
	if assessment := AssessAnswerProvenance(provenanceContract("what is this project's license"), answer, provenance); !assessment.Pass() {
		t.Fatalf("a targeted answer was forced to attribute: %s", assessment.Reason())
	}
}

// A required external capability that never produced an observation is a
// review boundary even when the answer is otherwise well-formed.
func TestRequiredExternalObservationIsPartOfCompletion(t *testing.T) {
	turns := []Turn{{Assistant: ai.AssistantMessage{
		Content: []ai.ContentBlock{ai.TextContent{Text: "The local README describes a Go project."}},
	}}}
	intent := provenanceContract("check the project using the configured MCP server")
	got := EvaluateTaskCompletion(CompletionInput{
		Turns:    turns,
		Contract: intent,
		Provenance: EvidenceProvenance{
			ExternalRequired: true,
		},
	})
	if got.Complete() {
		t.Fatal("a required external capability was never observed but completion passed")
	}
	if !strings.Contains(got.Reason, "external knowledge") {
		t.Fatalf("reason = %q, want the external-observation reason", got.Reason)
	}

	got = EvaluateTaskCompletion(CompletionInput{
		Turns:    turns,
		Contract: intent,
		Provenance: EvidenceProvenance{
			ExternalRequired: true,
			ExternalObserved: true,
		},
	})
	if !got.Complete() {
		t.Fatalf("an observed external requirement was rejected: %s", got.Reason)
	}
}
