package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/provider/catalog"
)

func behavioralMutationContract() contract.IntentContract {
	return contract.IntentContract{
		Kind:                contract.ObjectiveMutation,
		Objective:           "Make the project section scroll in one row without lag",
		BehavioralObjective: true,
		Verification:        []contract.VerificationRequirement{{Kind: contract.VerifyAny}},
	}
}

func classedVerification(class VerificationClass, check contract.VerificationCheck) Turn {
	return Turn{Results: []ToolOutcome{{
		ToolName: "verify",
		Result: Result{
			Text:         "verification: PASS",
			Executes:     true,
			Finished:     true,
			Verifies:     true,
			Verification: &VerificationEvidence{Command: "verify", Check: check, Class: class},
		},
	}}}
}

// A mutation plus a static check is a real change with a real check, but it is
// not evidence that the page behaves. The completion boundary must not certify
// an observed-behavior claim from that evidence.
func TestMutationWithStaticVerificationDoesNotCompleteBehavioralClaim(t *testing.T) {
	turns := []Turn{
		mutationTurn(),
		classedVerification(VerificationStatic, contract.CheckAny),
		answerTurn("The project section now scrolls seamlessly with no lag."),
	}
	verdict := EvaluateTaskCompletion(CompletionInput{
		Turns:    turns,
		Contract: behavioralMutationContract(),
	})
	if verdict.Complete() {
		t.Fatalf("static verification certified a behavioral claim: %+v", verdict)
	}
	if !strings.Contains(strings.ToLower(verdict.Reason), "motion") {
		t.Fatalf("review reason = %q, want the unmeasured-motion boundary", verdict.Reason)
	}
}

// The same evidence must leave a behavioral answer claim unverified at the
// answer gate, not silently publish it and reject the task later.
func TestBehavioralClaimRemainsUnverifiedWithStaticEvidence(t *testing.T) {
	turns := []Turn{
		mutationTurn(),
		classedVerification(VerificationStatic, contract.CheckAny),
	}
	answer := "The page renders the project cards."
	finalized := FinalizeAnswer(behavioralMutationContract(), turns, answer, EvidenceProvenance{}, "")
	if finalized.Audit.Approved() {
		t.Fatal("the answer gate approved a behavioral claim with only static evidence")
	}
	if finalized.Audit.UnsupportedClaims == 0 {
		t.Fatalf("audit = %+v, want a quantified unsupported claim", finalized.Audit)
	}
	issues := finalized.Audit.Issues
	if len(issues) == 0 || issues[0].Code != "unsupported_behavioral_claim" {
		t.Fatalf("issues = %+v, want unsupported_behavioral_claim", issues)
	}
	if issues[0].Requirement != "behavioral verification" {
		t.Fatalf("requirement = %q, want behavioral verification", issues[0].Requirement)
	}
}

// A behavioral check after the change is the evidence that makes the same
// claim supportable. The completion and the publication gate must agree.
func TestBehavioralVerificationSupportsBehavioralClaim(t *testing.T) {
	turns := []Turn{
		mutationTurn(),
		classedVerification(VerificationBehavioral, contract.CheckAny),
		answerTurn("The page renders the project cards."),
	}
	evidence := scanCompletionEvidence(turns, true, false)
	behavioral := AssessBehavior(behavioralMutationContract(), nil, evidence, nil)
	if behavioral.BehavioralVerifications != 1 {
		t.Fatalf("behavioral verifications = %d, want 1", behavioral.BehavioralVerifications)
	}
	if !behavioral.Satisfied {
		t.Fatalf("behavioral evidence = %+v, want satisfied", behavioral)
	}
	verdict := EvaluateTaskCompletion(CompletionInput{
		Turns:    turns,
		Contract: behavioralMutationContract(),
	})
	if !verdict.Complete() {
		t.Fatalf("behavioral verification did not complete: %+v", verdict)
	}
	if finalized := FinalizeAnswer(behavioralMutationContract(), turns, turns[len(turns)-1].Assistant.Text(),
		EvidenceProvenance{}, ""); !finalized.Audit.Approved() {
		t.Fatalf("the answer gate rejected supported behavior: %+v", finalized.Audit)
	}
	// The same behavioral check proves rendered content, not motion. A
	// stronger sentence must still be refused.
	motion := FinalizeAnswer(behavioralMutationContract(), turns,
		"The page scrolls seamlessly with no lag.", EvidenceProvenance{}, "")
	if motion.Audit.Approved() {
		t.Fatal("a rendered-text check was promoted into proof of motion")
	}
	if !strings.Contains(motion.Audit.Issues[0].Requirement, "motion") {
		t.Fatalf("requirement = %q, want a motion observation", motion.Audit.Issues[0].Requirement)
	}
	// An honest limitation is allowed: the model may report the rendered page
	// and say the motion was not measured.
	bounded := FinalizeAnswer(behavioralMutationContract(), turns,
		"The page renders the project cards; I could not measure the scroll motion or lag.", EvidenceProvenance{}, "")
	if !bounded.Audit.Approved() {
		t.Fatalf("an explicitly bounded motion limitation was rejected: %+v", bounded.Audit)
	}
}

// The publication gate must reject a behavioral claim even when the same
// claims bypass the completion audit, so a host cannot show a stronger answer
// than the durable verdict.
func TestAnswerGateRejectsBehavioralClaimBeyondStaticVerification(t *testing.T) {
	turns := []Turn{
		mutationTurn(),
		classedVerification(VerificationStatic, contract.CheckAny),
	}
	finalized := FinalizeAnswer(behavioralMutationContract(), turns,
		"The page renders the scrolling row.", EvidenceProvenance{}, "")
	if finalized.Audit.Approved() || finalized.Audit.UnsupportedClaims == 0 {
		t.Fatalf("audit = %+v, want a rejected publication", finalized.Audit)
	}
}

// A provider failure after a mutation is a resumable interruption. The
// completed change must remain in the turns and in the evidence derivation;
// only the missing verification may be reported.
func TestProviderFailureAfterMutationPreservesResumableEvidence(t *testing.T) {
	tool := newScriptedTool("write", "wrote")
	tool.result = Result{Text: "wrote", Mutates: true}
	var (
		mu      sync.Mutex
		attempt int
	)
	streamFn := func(context.Context, ai.Request) (*ai.Stream, error) {
		mu.Lock()
		position := attempt
		attempt++
		mu.Unlock()
		if position == 0 {
			return ai.NewCompletedStream(ai.AssistantMessage{
				Content: []ai.ContentBlock{ai.ToolCall{
					ID: "call-1", Name: "write", Arguments: map[string]any{"value": "x"},
				}},
				StopReason: ai.StopToolUse,
			}), nil
		}
		return nil, errors.New("stream ID 11; PROTOCOL_ERROR")
	}
	loop, err := New(Config{
		Model:    catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		Stream:   streamFn,
		Tools:    []Tool{tool},
		MaxTurns: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	turns, err := loop.Run(context.Background(), []Message{UserMessage{Text: "write"}})
	if !errors.Is(err, ErrProviderRequest) {
		t.Fatalf("err = %v, want ErrProviderRequest", err)
	}
	var mutation bool
	for _, turn := range turns {
		for _, outcome := range turn.Results {
			if outcome.Result.Mutates {
				mutation = true
			}
		}
	}
	if !mutation {
		t.Fatalf("turns = %+v, want the completed mutation retained", turns)
	}
	verdict, reason, _ := ClassifyRun(err, CompletionInput{Turns: turns}, false)
	if verdict != RunNeedsReview {
		t.Fatalf("verdict = %v (%s), want RunNeedsReview", verdict, reason)
	}
	mutated, verified := CompletionEvidence(turns, true)
	if !mutated || verified {
		t.Fatalf("evidence = mutated %t verified %t, want the change preserved without a verification", mutated, verified)
	}
}
