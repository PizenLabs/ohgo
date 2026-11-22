package cognition

import (
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

func conclusionTurn(callID, verdict, evidence, uncertainty string) agent.Turn {
	return conclusionTurnStatus(callID, verdict, "FINAL", evidence, uncertainty)
}

func conclusionTurnStatus(callID, verdict, status, evidence, uncertainty string) agent.Turn {
	return agent.Turn{Results: []agent.ToolOutcome{{
		CallID: callID, ToolName: "conclude",
		Args: map[string]any{"verdict": verdict, "status": status, "evidence": evidence, "uncertainty": uncertainty},
		Result: agent.Result{
			Text: "conclusion: " + verdict,
			Details: map[string]any{
				"verdict": verdict, "status": status, "evidence": evidence, "uncertainty": uncertainty,
			},
		},
	}}}
}

func conclusionShape(objective string) TaskShape {
	return BuildShape(contract.IntentContract{
		Kind:      contract.ObjectiveInformational,
		Objective: objective,
	}, ShapeParams{Authority: "fuzz", Intent: "investigate", Facts: RepositoryFacts{Go: true}})
}

// A conclusion that explicitly names an uninspected code coordinate is
// provisional. It must not satisfy the conclusion requirement or open the
// answer boundary; its target becomes the next bounded observation.
func TestNamedConclusionGapKeepsTheInvestigationOpen(t *testing.T) {
	shape := conclusionShape("Investigate the order flow and conclude SAFE, BUG, or UNRESOLVED.")
	evidence := NewEvidenceState()
	evidence.ObserveTurns(shape, []agent.Turn{
		readTurn("read-1", "backend/checkout/app/checkout.go",
			"package app\n\ntype StockReserver interface {\n\tReserve(quantities map[string]int) error\n}\n"),
	})
	evidence.ObserveTurns(shape, []agent.Turn{
		conclusionTurn("conclude-1", "UNRESOLVED",
			"checkout Place calls Reserve before Save",
			"the implementation of StockReserver.Reserve and whether it rejects deleted products is unknown"),
	})

	suff := EvaluateSufficiency(shape, evidence, ExecutionState{}, false)
	if suff.Stage != StageExplore {
		t.Fatalf("stage = %s, want explore while the named gap is unresolved", suff.Stage)
	}
	if !strings.Contains(strings.Join(suff.Missing, "\x00"), "investigation_target:StockReserver.Reserve") {
		t.Fatalf("missing = %v, want the named code target", suff.Missing)
	}
	if evidence.Satisfied("investigation_conclusion", ExecutionState{}) {
		t.Fatal("a provisional conclusion satisfied the conclusion requirement")
	}

	evidence.ObserveTurns(shape, []agent.Turn{
		readTurn("read-2", "backend/productcatalog/adapter/postgres.go",
			"package adapter\n\nfunc (db postgres) Reserve(ctx context.Context, quantities map[string]int) error {\n\treturn nil\n}\n"),
	})
	suff = EvaluateSufficiency(shape, evidence, ExecutionState{}, false)
	if strings.Contains(strings.Join(suff.Missing, "\x00"), "investigation_target:StockReserver.Reserve") {
		t.Fatalf("target remained missing after its source was observed: %v", suff.Missing)
	}
	if !strings.Contains(strings.Join(suff.Missing, "\x00"), "investigation_conclusion") {
		t.Fatalf("missing = %v, want the conclusion requirement after the target was observed", suff.Missing)
	}

	evidence.ObserveTurns(shape, []agent.Turn{
		conclusionTurn("conclude-2", "UNRESOLVED",
			"Reserve rejects unknown/deleted products before Save",
			""),
	})
	suff = EvaluateSufficiency(shape, evidence, ExecutionState{}, false)
	if suff.Stage != StageAnswer {
		t.Fatalf("stage = %s, want answer after the named gap was observed: %v", suff.Stage, suff.Reasons)
	}
}

// Omitting the unresolved coordinate from a later conclusion does not erase
// the obligation. Only an observation of that coordinate can clear it.
func TestConclusionCannotDropItsPendingObligation(t *testing.T) {
	shape := conclusionShape("Investigate the order flow and conclude SAFE, BUG, or UNRESOLVED.")
	evidence := NewEvidenceState()
	evidence.ObserveTurns(shape, []agent.Turn{
		readTurn("read-1", "checkout.go", "package checkout\n"),
		conclusionTurn("conclude-1", "UNRESOLVED", "checkout.Place", "StockReserver.Reserve remains unknown"),
	})
	evidence.ObserveTurns(shape, []agent.Turn{
		conclusionTurn("conclude-2", "UNRESOLVED", "checkout.Place", ""),
	})
	if evidence.Satisfied("investigation_conclusion", ExecutionState{}) {
		t.Fatal("a conclusion with an omitted pending target was accepted")
	}
	if !strings.Contains(strings.Join(evidence.PendingConclusionTargets(), "\x00"), "StockReserver.Reserve") {
		t.Fatalf("pending targets = %v, want the original obligation", evidence.PendingConclusionTargets())
	}
}

// A repeated UNRESOLVED conclusion without a new observation is not a hard
// semantic settlement; the projection must keep the investigation open.
func TestRepeatedUnresolvedWithoutObservationIsNotSettled(t *testing.T) {
	shape := conclusionShape("Investigate the order flow and conclude SAFE, BUG, or UNRESOLVED.")
	evidence := NewEvidenceState()
	evidence.ObserveTurns(shape, []agent.Turn{
		readTurn("read-1", "checkout.go", "package checkout\n"),
		conclusionTurn("conclude-1", "UNRESOLVED", "checkout.Place", "StockReserver.Reserve remains unknown"),
	})
	for i := 0; i < MaxConclusionAttempts; i++ {
		evidence.ObserveTurns(shape, []agent.Turn{
			conclusionTurn("conclude-repeat-"+itoa(i), "UNRESOLVED", "checkout.Place", "StockReserver.Reserve remains unknown"),
		})
	}
	if evidence.Satisfied("investigation_conclusion", ExecutionState{}) {
		t.Fatal("repeated conclusions without a new observation were accepted")
	}
}

// A bounded UNRESOLVED settlement is allowed only after an observation was
// actually made; the missing coordinate remains explicit in the record.
func TestBoundedUnresolvedSettlementRequiresAnObservation(t *testing.T) {
	shape := conclusionShape("Investigate the order flow and conclude SAFE, BUG, or UNRESOLVED.")
	evidence := NewEvidenceState()
	evidence.ObserveTurns(shape, []agent.Turn{
		readTurn("read-1", "checkout.go", "package checkout\n"),
		conclusionTurn("conclude-1", "UNRESOLVED", "checkout.Place", "StockReserver.Reserve remains unknown"),
	})
	for i := 0; i < MaxConclusionAttempts; i++ {
		evidence.ObserveTurns(shape, []agent.Turn{
			readTurn("read-extra-"+itoa(i), "other"+itoa(i)+".go", "package other\n"),
			conclusionTurn("conclude-"+itoa(i), "UNRESOLVED", "checkout.Place", "StockReserver.Reserve remains unknown"),
		})
	}
	if !evidence.Satisfied("investigation_conclusion", ExecutionState{}) {
		t.Fatal("the bounded UNRESOLVED settlement was not accepted after an observation")
	}
	latest, ok := evidence.latestConclusion()
	if !ok || latest.Complete != true || len(latest.Unresolved) == 0 {
		t.Fatalf("latest conclusion = %+v, want a complete record with explicit unresolved evidence", latest)
	}
}

func TestConclusionTargetExtractionIsNarrow(t *testing.T) {
	targets := unresolvedConclusionTargets("the StockReserver.Reserve path in backend/productcatalog/adapter/postgres.go is unknown, not enough evidence (e.g. docs)")
	joined := strings.Join(targets, "\x00")
	for _, want := range []string{"StockReserver.Reserve", "backend/productcatalog/adapter/postgres.go"} {
		if !strings.Contains(joined, want) {
			t.Errorf("targets = %v, want %q", targets, want)
		}
	}
	if strings.Contains(joined, "enough evidence") || strings.Contains(joined, "e.g.") {
		t.Errorf("prose became a target: %v", targets)
	}
}

// A later provisional conclusion supersedes an earlier complete one. History
// cannot mask a newly named unresolved coordinate.
func TestLatestConclusionIsAuthoritative(t *testing.T) {
	shape := conclusionShape("Investigate the order flow and conclude SAFE, BUG, or UNRESOLVED.")
	evidence := NewEvidenceState()
	evidence.ObserveTurns(shape, []agent.Turn{
		readTurn("read-1", "checkout.go", "package checkout\n"),
		conclusionTurn("conclude-1", "SAFE", "checkout.Place", ""),
	})
	evidence.ObserveTurns(shape, []agent.Turn{
		conclusionTurn("conclude-2", "UNRESOLVED", "checkout.Place", "ProductService.Reserve remains unknown"),
	})
	if evidence.Satisfied("investigation_conclusion", ExecutionState{}) {
		t.Fatal("an earlier complete conclusion masked the later provisional one")
	}
	if !strings.Contains(strings.Join(evidence.PendingConclusionTargets(), "\x00"), "ProductService.Reserve") {
		t.Fatalf("pending targets = %v, want the later conclusion's target", evidence.PendingConclusionTargets())
	}
}

// An explicit progress observation is not a conclusion. It must not satisfy
// investigation_conclusion or start the answer boundary even when the hidden
// verdict is UNRESOLVED.
func TestProgressConclusionDoesNotOpenTheAnswerBoundary(t *testing.T) {
	shape := conclusionShape("Investigate the order flow and conclude SAFE, BUG, or UNRESOLVED.")
	evidence := NewEvidenceState()
	evidence.ObserveTurns(shape, []agent.Turn{
		readTurn("read-1", "checkout.go", "package checkout\n"),
		conclusionTurnStatus("progress-1", "UNRESOLVED", "IN_PROGRESS",
			"Preliminary only; investigation still in progress.", "Place body not read"),
	})
	if evidence.Satisfied("investigation_conclusion", ExecutionState{}) {
		t.Fatal("a progress observation satisfied the conclusion requirement")
	}
	suff := EvaluateSufficiency(shape, evidence, ExecutionState{}, false)
	if suff.Stage == StageAnswer {
		t.Fatalf("progress observation opened the answer boundary: %+v", suff)
	}
	if _, ok := evidence.Find("conclusion-progress:" + digest("UNRESOLVED\x00Preliminary only; investigation still in progress.\x00Place body not read")); !ok {
		t.Fatal("progress observation was not recorded for audit")
	}
	if evidence.PendingConclusionTargets() != nil {
		t.Fatalf("progress observation created a terminal obligation: %v", evidence.PendingConclusionTargets())
	}
}

// A progress marker must not clear an existing named obligation.
func TestProgressConclusionDoesNotClearPendingTarget(t *testing.T) {
	shape := conclusionShape("Investigate the order flow and conclude SAFE, BUG, or UNRESOLVED.")
	evidence := NewEvidenceState()
	evidence.ObserveTurns(shape, []agent.Turn{
		readTurn("read-1", "checkout.go", "package checkout\n"),
		conclusionTurn("conclude-1", "UNRESOLVED", "checkout.Place", "StockReserver.Reserve remains unknown"),
		conclusionTurnStatus("progress-1", "UNRESOLVED", "IN_PROGRESS", "Preliminary only; investigation still in progress.", ""),
	})
	if evidence.Satisfied("investigation_conclusion", ExecutionState{}) {
		t.Fatal("a progress observation satisfied the conclusion requirement")
	}
	if !strings.Contains(strings.Join(evidence.PendingConclusionTargets(), "\x00"), "StockReserver.Reserve") {
		t.Fatalf("pending targets = %v, want the original obligation", evidence.PendingConclusionTargets())
	}
}

// A progress marker must not advance the terminal conclusion window.
func TestProgressObservationDoesNotConsumeConclusionWindow(t *testing.T) {
	shape := conclusionShape("Investigate the order flow and conclude SAFE, BUG, or UNRESOLVED.")
	projector := NewProjector(shape, NewEvidenceState(), RepositoryFacts{Go: true})
	tools := []agent.Tool{
		testTool{name: "read", tier: agent.TierRead},
		testTool{name: "conclude", tier: agent.TierRead},
	}
	request := func(turns []agent.Turn) {
		projector.Project(agent.ProjectionRequest{
			Messages: []agent.Message{agent.UserMessage{Text: shape.Objective}},
			Turns:    turns,
			Tools:    tools,
			System:   []ai.SystemPromptBlock{{Text: "base"}},
		})
	}
	var turns []agent.Turn
	request(turns)
	turns = append(turns, readTurn("read-1", "checkout.go", "package checkout\n"))
	turns = append(turns, conclusionTurnStatus("progress-1", "UNRESOLVED", "IN_PROGRESS", "still investigating", "Place body unread"))
	request(turns)
	if projector.conclusionTurns != 0 {
		t.Fatalf("progress observation advanced the conclusion window to %d", projector.conclusionTurns)
	}
	turns = append(turns, readTurn("read-2", "checkout2.go", "package checkout\n"))
	request(turns)
	if projector.conclusionTurns != 1 {
		t.Fatalf("ordinary observation did not advance the window: %d", projector.conclusionTurns)
	}
}

// Progress cannot extend an already-exhausted conclusion window indefinitely.
func TestProgressCannotExtendExhaustedConclusionWindow(t *testing.T) {
	shape := conclusionShape("Investigate the order flow and conclude SAFE, BUG, or UNRESOLVED.")
	projector := NewProjector(shape, NewEvidenceState(), RepositoryFacts{Go: true})
	projector.conclusionTurns = MaxConclusionObservationTurns
	turns := []agent.Turn{
		readTurn("read-1", "checkout.go", "package checkout\n"),
		conclusionTurnStatus("progress-1", "UNRESOLVED", "IN_PROGRESS", "still investigating", "Place body unread"),
	}
	projected := projector.Project(agent.ProjectionRequest{
		Messages: []agent.Message{agent.UserMessage{Text: shape.Objective}},
		Turns:    turns,
		Tools: []agent.Tool{
			testTool{name: "read", tier: agent.TierRead},
			testTool{name: "conclude", tier: agent.TierRead},
		},
		System: []ai.SystemPromptBlock{{Text: "base"}},
	})
	if !projected.Report.Terminal {
		t.Fatalf("progress extended an exhausted conclusion window: %+v", projected.Report)
	}
}

// A conclusion whose payload explicitly says it is a placeholder/incomplete
// must remain an observation even through the legacy FINAL path. This is the
// session-585 failure: concluding "placeholder, not yet sufficient" opened the
// answer boundary.
func TestPlaceholderConclusionDoesNotOpenTheAnswerBoundary(t *testing.T) {
	shape := conclusionShape("Investigate the order flow and conclude SAFE, BUG, or UNRESOLVED.")
	evidence := NewEvidenceState()
	evidence.ObserveTurns(shape, []agent.Turn{
		readTurn("read-1", "checkout.go", "package checkout\n"),
		conclusionTurnStatus("conclude-placeholder", "UNRESOLVED", "FINAL",
			"placeholder, not yet sufficient", ""),
	})
	if evidence.Satisfied("investigation_conclusion", ExecutionState{}) {
		t.Fatal("a placeholder conclusion satisfied the conclusion requirement")
	}
	suff := EvaluateSufficiency(shape, evidence, ExecutionState{}, false)
	if suff.Stage == StageAnswer {
		t.Fatalf("placeholder conclusion opened the answer boundary: %+v", suff)
	}
	if _, ok := evidence.latestConclusion(); ok {
		t.Fatal("placeholder conclusion was recorded as a terminal conclusion")
	}
	if evidence.PendingConclusionTargets() != nil {
		t.Fatalf("placeholder conclusion created a terminal obligation: %v", evidence.PendingConclusionTargets())
	}
	projector := NewProjector(shape, evidence, RepositoryFacts{Go: true})
	projected := projector.Project(agent.ProjectionRequest{
		Messages: []agent.Message{agent.UserMessage{Text: shape.Objective}},
		Turns: []agent.Turn{
			readTurn("read-1", "checkout.go", "package checkout\n"),
			conclusionTurnStatus("conclude-placeholder", "UNRESOLVED", "FINAL",
				"placeholder, not yet sufficient", ""),
		},
		Tools: []agent.Tool{
			testTool{name: "read", tier: agent.TierRead},
			testTool{name: "conclude", tier: agent.TierRead},
		},
		System: []ai.SystemPromptBlock{{Text: "base"}},
	})
	if projected.Report.Terminal {
		t.Fatalf("placeholder conclusion made the projection terminal: %+v", projected.Report)
	}
	if len(projected.Tools) == 0 {
		t.Fatal("placeholder conclusion withdrew the capability surface")
	}
}

// A non-admissible conclusion must not consume the bounded terminal attempt
// budget. Only an admitted FINAL conclusion starts that accounting.
func TestProvisionalConclusionDoesNotConsumeConclusionAttempts(t *testing.T) {
	shape := conclusionShape("Investigate the order flow and conclude SAFE, BUG, or UNRESOLVED.")
	evidence := NewEvidenceState()
	evidence.ObserveTurns(shape, []agent.Turn{
		readTurn("read-1", "checkout.go", "package checkout\n"),
	})
	for i := 0; i < MaxConclusionAttempts+1; i++ {
		evidence.ObserveTurns(shape, []agent.Turn{
			conclusionTurnStatus("progress-"+itoa(i), "UNRESOLVED", "FINAL",
				"placeholder, not yet sufficient", ""),
		})
	}
	if _, ok := evidence.latestConclusion(); ok {
		t.Fatal("provisional payloads consumed conclusion attempt records")
	}
	evidence.ObserveTurns(shape, []agent.Turn{
		conclusionTurn("conclude-final", "UNRESOLVED", "checkout.Place",
			"StockReserver.Reserve remains unknown"),
	})
	latest, ok := evidence.latestConclusion()
	if !ok {
		t.Fatal("the first admitted conclusion was not recorded")
	}
	if got := latest.Counts["attempts"]; got != 1 {
		t.Fatalf("attempts = %d, want 1 after provisional observations", got)
	}
}

// A persisted legacy conclusion that says it is a placeholder is not made
// admissible by the old record being marked complete.
func TestPersistedPlaceholderConclusionIsNotAdmissible(t *testing.T) {
	evidence := NewEvidenceState()
	evidence.Items = append(evidence.Items, EvidenceItem{
		ID: "conclusion:" + digest("UNRESOLVED\x00placeholder, not yet sufficient"), Status: StatusKnown,
		Retention: RetentionExact, Provenance: ProvenanceDiagnostic, Complete: true,
		Value: "verdict: UNRESOLVED\nevidence: placeholder, not yet sufficient",
		View:  "conclusion", Seq: 1,
	})
	if evidence.Satisfied("investigation_conclusion", ExecutionState{}) {
		t.Fatal("a persisted placeholder conclusion was admitted")
	}
}

// Older callers may omit status. A legacy placeholder payload must still be
// treated as progress by the control plane rather than admitted as FINAL.
func TestLegacyPlaceholderConclusionIsNotAdmissible(t *testing.T) {
	shape := conclusionShape("Investigate the order flow and conclude SAFE, BUG, or UNRESOLVED.")
	evidence := NewEvidenceState()
	evidence.ObserveTurns(shape, []agent.Turn{
		readTurn("read-1", "checkout.go", "package checkout\n"),
		conclusionTurnStatus("conclude-legacy", "UNRESOLVED", "",
			"placeholder, not yet sufficient", ""),
	})
	if evidence.Satisfied("investigation_conclusion", ExecutionState{}) {
		t.Fatal("legacy placeholder conclusion satisfied the conclusion requirement")
	}
	if _, ok := evidence.latestConclusion(); ok {
		t.Fatal("legacy placeholder conclusion was recorded as terminal")
	}
}

// A general project analysis answers a question; it is not a production-safety
// verdict. The control plane must reject SAFE even when the tool-side check was
// bypassed by a persisted or embedder-supplied payload.
func TestAnswerObjectiveRejectsDefectVerdict(t *testing.T) {
	shape := conclusionShape("Analyze this project and explain what it is about.")
	evidence := NewEvidenceState()
	evidence.ObserveTurns(shape, []agent.Turn{
		readTurn("read-1", "README.md", "# andev0x\nA terminal blog.\n"),
		conclusionTurn("conclude-safe", "SAFE", "the project is a Next.js blog", ""),
	})
	if _, ok := evidence.latestConclusion(); ok {
		t.Fatal("a general answer was recorded as a terminal safety verdict")
	}
	rejected := false
	for _, item := range evidence.Items {
		if item.View == "conclusion_rejected" {
			rejected = true
		}
	}
	if !rejected {
		t.Fatal("the out-of-mode verdict was not recorded for audit")
	}
}

// The same guard works in the other direction: a defect investigation cannot
// close as ANSWERED just because the model found the answer useful.
func TestDefectObjectiveRejectsAnswerVerdict(t *testing.T) {
	shape := conclusionShape("Investigate the order flow and conclude SAFE, BUG, or UNRESOLVED.")
	evidence := NewEvidenceState()
	evidence.ObserveTurns(shape, []agent.Turn{
		readTurn("read-1", "checkout.go", "package checkout\n"),
		conclusionTurn("conclude-answered", "ANSWERED", "the checkout path is now clear", ""),
	})
	if _, ok := evidence.latestConclusion(); ok {
		t.Fatal("a defect investigation was closed by an answer verdict")
	}
}

// The positive answer vocabulary is admissible and terminal for a general
// analysis, so the split does not turn every conclusion into a review.
func TestAnswerObjectiveAcceptsAnsweredOutcome(t *testing.T) {
	shape := conclusionShape("Analyze this project and explain what it is about.")
	evidence := NewEvidenceState()
	evidence.ObserveTurns(shape, []agent.Turn{
		readTurn("read-1", "README.md", "# andev0x\nA terminal blog.\n"),
		conclusionTurn("conclude-answered", "ANSWERED", "the entrypoint loads Markdown posts", ""),
	})
	latest, ok := evidence.latestConclusion()
	if !ok || !latest.Complete {
		t.Fatalf("latest conclusion = %+v, want a complete answer outcome", latest)
	}
}

// Plain project analysis is not a defect investigation. The shape must not
// manufacture a verdict requirement, or the model is pushed to pick SAFE for a
// question that only asked what the project is.
func TestPlainProjectAnalysisDoesNotRequireADefectVerdict(t *testing.T) {
	shape := BuildShape(contract.IntentContract{
		Kind:      contract.ObjectiveInformational,
		Objective: "Investigate this project and analyze what this project is about?",
	}, ShapeParams{Authority: "fuzz", Intent: "investigate", Facts: RepositoryFacts{Go: true}})
	for _, need := range shape.RequiredKnowledge {
		if need.Key == "investigation_conclusion" {
			t.Fatal("plain project analysis required a defect verdict")
		}
	}
}

// PARTIAL is a terminal answer with declared gaps, not a provisional
// conclusion. The gaps stay in the audit record but do not become pending
// observations that make the verdict unusable.
func TestPartialAnswerKeepsGapsWithoutReopeningTheConclusion(t *testing.T) {
	shape := conclusionShape("Analyze this project and explain what it is about.")
	evidence := NewEvidenceState()
	evidence.ObserveTurns(shape, []agent.Turn{
		readTurn("read-1", "README.md", "# andev0x\nA terminal blog.\n"),
		conclusionTurn("conclude-partial", "PARTIAL",
			"the entrypoint loads Markdown posts", "StockReserver.Reserve was not read"),
	})
	latest, ok := evidence.latestConclusion()
	if !ok || !latest.Complete {
		t.Fatalf("latest conclusion = %+v, want a complete PARTIAL outcome", latest)
	}
	if len(latest.Unresolved) == 0 {
		t.Fatal("the declared gap was not retained for audit")
	}
	if pending := evidence.PendingConclusionTargets(); len(pending) != 0 {
		t.Fatalf("pending targets = %v, want PARTIAL to keep the answer terminal", pending)
	}
}

// ANSWERED with a concrete unobserved coordinate is a contradiction: the model
// says the question is answered but names a file it did not read. That remains
// provisional and the coordinate becomes the next bounded observation.
func TestAnsweredWithNamedGapRemainsProvisional(t *testing.T) {
	shape := conclusionShape("Analyze this project and explain what it is about.")
	evidence := NewEvidenceState()
	evidence.ObserveTurns(shape, []agent.Turn{
		readTurn("read-1", "README.md", "# andev0x\nA terminal blog.\n"),
		conclusionTurn("conclude-answered-gap", "ANSWERED",
			"the entrypoint loads Markdown posts", "StockReserver.Reserve was not read"),
	})
	latest, ok := evidence.latestConclusion()
	if !ok || latest.Complete {
		t.Fatalf("latest conclusion = %+v, want a provisional ANSWERED result", latest)
	}
	if pending := evidence.PendingConclusionTargets(); len(pending) == 0 {
		t.Fatal("the named gap did not become the next observation")
	}
}

// readTurnWindow builds one read whose reported window covers only part of
// the file, so the observation is real but not complete.
func readTurnWindow(callID, path, body string, offset, limit, totalLines int) agent.Turn {
	return agent.Turn{Results: []agent.ToolOutcome{{
		CallID:   callID,
		ToolName: "read",
		Args:     map[string]any{"path": path},
		Result: agent.Result{Text: body, Details: map[string]any{
			"path": path, "totalLines": totalLines, "offset": offset, "limit": limit,
		}},
	}}}
}

// A short method name (StockReserver.Run) is a satisfiable obligation: the
// bare-name fallback matches on identifier boundaries, so a longer token such
// as "runtime" or "CheckoutRunner" cannot satisfy it and no length gate is
// needed. The old len>=4 gate made such targets unsatisfiable, and the
// runtime demanded the same inspection on every turn.
func TestShortMethodTargetIsSatisfiableOnIdentifierBoundaries(t *testing.T) {
	shape := conclusionShape("Investigate the order flow and conclude SAFE, BUG, or UNRESOLVED.")
	evidence := NewEvidenceState()
	evidence.ObserveTurns(shape, []agent.Turn{
		conclusionTurn("conclude-1", "UNRESOLVED",
			"checkout Place calls Run before Save",
			"the implementation of StockReserver.Run is unknown"),
	})
	target := "investigation_target:StockReserver.Run"
	if evidence.Satisfied(target, ExecutionState{}) {
		t.Fatal("the short method target was satisfied before any observation")
	}
	evidence.ObserveTurns(shape, []agent.Turn{
		readTurn("read-1", "other.go",
			"package other\n\n// runtime helper for the CheckoutRunner flow\n"),
	})
	if evidence.Satisfied(target, ExecutionState{}) {
		t.Fatal("a longer token containing the name satisfied the target: substring matching leaked")
	}
	evidence.ObserveTurns(shape, []agent.Turn{
		readTurn("read-2", "reserver.go",
			"package stock\n\nfunc (s StockReserver) Run(ctx context.Context) error {\n\treturn nil\n}\n"),
	})
	if !evidence.Satisfied(target, ExecutionState{}) {
		t.Fatal("the short method target stayed missing after its bare name was observed")
	}
}

// A target whose only mention lies outside the retained evidence excerpt is
// still observed. Matching happens at observe time against the whole tool
// text, so retention bounds (MaxEvidenceValueBytes) cannot make a real
// observation read as missing and force a reread that observes nothing new.
func TestTargetObservationSurvivesRetentionTruncation(t *testing.T) {
	shape := conclusionShape("Investigate the order flow and conclude SAFE, BUG, or UNRESOLVED.")
	evidence := NewEvidenceState()
	evidence.ObserveTurns(shape, []agent.Turn{
		conclusionTurn("conclude-1", "UNRESOLVED",
			"checkout Place calls Reserve before Save",
			"the implementation of StockReserver.Reserve is unknown"),
	})
	target := "investigation_target:StockReserver.Reserve"
	// The preamble alone exceeds the retained per-item bound; the named
	// symbol appears only after it.
	filler := strings.Repeat("func FillerRoutine() { return \"padding\" }\n", 600)
	body := filler + "func (db StockReserver) Reserve(ctx context.Context) error {\n\treturn nil\n}\n"
	if len(filler) <= MaxEvidenceValueBytes {
		t.Fatalf("fixture preamble = %d bytes, must exceed MaxEvidenceValueBytes", len(filler))
	}
	evidence.ObserveTurns(shape, []agent.Turn{
		readTurn("read-1", "adapter.go", body),
	})
	if !evidence.Satisfied(target, ExecutionState{}) {
		t.Fatal("the target stayed missing though its file was read past the retained excerpt")
	}
}

// A conclusion that names a file whose complete contents the run already
// holds does not reopen that file: the obligation would only force a reread
// of a window the runtime already observed (the reported main.go case).
func TestCompletePriorReadSatisfiesNamedPathTarget(t *testing.T) {
	shape := conclusionShape("Investigate the order flow and conclude SAFE, BUG, or UNRESOLVED.")
	evidence := NewEvidenceState()
	evidence.ObserveTurns(shape, []agent.Turn{
		readTurn("read-1", "main.go", "package main\n\nfunc main() {}\n"),
	})
	evidence.ObserveTurns(shape, []agent.Turn{
		conclusionTurn("conclude-1", "UNRESOLVED",
			"the entrypoint wires the CLI",
			"the role of main.go in the wiring is unknown"),
	})
	if !evidence.Satisfied("investigation_conclusion", ExecutionState{}) {
		t.Fatal("a conclusion naming an already-completely-read file stayed provisional")
	}
	if pending := evidence.PendingConclusionTargets(); len(pending) != 0 {
		t.Fatalf("pending = %v, want no reopened path obligation", pending)
	}

	// A partial window is not the file's contents; naming it stays a real
	// obligation because more of the file may still matter.
	partial := NewEvidenceState()
	partial.ObserveTurns(shape, []agent.Turn{
		readTurnWindow("read-1", "cmd/ohgo/main.go", "package main\n", 1, 2, 100),
	})
	partial.ObserveTurns(shape, []agent.Turn{
		conclusionTurn("conclude-1", "UNRESOLVED",
			"the entrypoint wires the CLI",
			"the role of cmd/ohgo/main.go in the wiring is unknown"),
	})
	if partial.Satisfied("investigation_conclusion", ExecutionState{}) {
		t.Fatal("a partial window read was treated as the file's complete contents")
	}
	if pending := partial.PendingConclusionTargets(); len(pending) == 0 {
		t.Fatal("the partial-window path obligation did not open")
	}
}

// A mutation after the read invalidates the prior-read credit: the
// conclusion may be naming the post-change state, so the file must be
// observed again.
func TestMutationAfterReadReopensNamedPathTarget(t *testing.T) {
	shape := conclusionShape("Investigate the order flow and conclude SAFE, BUG, or UNRESOLVED.")
	evidence := NewEvidenceState()
	evidence.ObserveTurns(shape, []agent.Turn{
		readTurn("read-1", "main.go", "package main\n\nfunc main() {}\n"),
	})
	evidence.ObserveTurns(shape, []agent.Turn{{Results: []agent.ToolOutcome{{
		CallID:   "write-1",
		ToolName: "write",
		Args:     map[string]any{"path": "main.go"},
		Result: agent.Result{Text: "wrote", Mutates: true,
			Details: map[string]any{"path": "main.go", "bytes": 5}},
	}}}})
	evidence.ObserveTurns(shape, []agent.Turn{
		conclusionTurn("conclude-1", "UNRESOLVED",
			"the entrypoint wires the CLI",
			"the role of main.go after the change is unknown"),
	})
	if evidence.Satisfied("investigation_conclusion", ExecutionState{}) {
		t.Fatal("a pre-mutation read was credited against a post-mutation obligation")
	}
	if pending := evidence.PendingConclusionTargets(); len(pending) == 0 {
		t.Fatal("the mutated path did not reopen as an observation obligation")
	}
}

// A conclusion that names a path the run already read completely is not a new
// obligation, regardless of language. The drop used to be Go-only, so a
// TypeScript or Python conclusion forced a reread of a window the runtime held.
func TestPreReadPathTargetIsDroppedForAnyLanguage(t *testing.T) {
	shape := conclusionShape("Investigate the order flow and conclude SAFE, BUG, or UNRESOLVED.")
	evidence := NewEvidenceState()
	evidence.ObserveTurns(shape, []agent.Turn{
		readTurnWindow("read-1", "src/parser.ts", "export function parse() { return [] }\n", 1, 2000, 1),
	})
	evidence.ObserveTurns(shape, []agent.Turn{
		conclusionTurn("conclude-1", "UNRESOLVED",
			"the entrypoint loads Markdown posts",
			"the behavior of src/parser.ts remains unknown"),
	})
	for _, target := range evidence.PendingConclusionTargets() {
		if target == "src/parser.ts" {
			t.Fatalf("a completely read path was reopened: %v", evidence.PendingConclusionTargets())
		}
	}
}
