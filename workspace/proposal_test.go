package workspace

import (
	"strings"
	"testing"
)

// emptyWorkspace is the no-convention evidence.
func emptyWorkspace() ProposalFacts { return ProposalFacts{} }

// establishedWorkspace is the convention evidence.
func establishedWorkspace() ProposalFacts {
	return ProposalFacts{HasConvention: true, VisibleEntries: 3}
}

// TestProposalQuestionQuotesTheActualRequest is the core contract: the ask is
// built from the request's own object, not from a fixed project-type
// template. A change that restores "How should this be structured?" fails
// here because the subject is absent.
func TestProposalQuestionQuotesTheActualRequest(t *testing.T) {
	const request = "Create a small portfolio site for John Smith."
	proposal, ok := AssessProposal(Build, request, emptyWorkspace())
	if !ok {
		t.Fatal("a target-less greenfield creation produced no proposal")
	}
	if proposal.Dimension != ProposalStructure {
		t.Errorf("dimension = %q, want structure", proposal.Dimension)
	}
	if proposal.Subject != "small portfolio site for John Smith" {
		t.Errorf("subject = %q, want the request's object", proposal.Subject)
	}
	for _, want := range []string{
		"small portfolio site for John Smith",
		"How should I organize",
	} {
		if !strings.Contains(proposal.Question, want) {
			t.Errorf("question %q does not carry %q", proposal.Question, want)
		}
	}
	if !strings.Contains(proposal.Reason, "small portfolio site for John Smith") {
		t.Errorf("reason %q does not carry the request's object", proposal.Reason)
	}
	if proposal.Question == "How should this be structured?" {
		t.Error("the proposal fell back to the fixed template")
	}
	if proposal.Request != request {
		t.Errorf("request = %q, want the exact user text", proposal.Request)
	}
}

// TestProposalQuestionsVaryWithTheRequest proves the dimension is computed
// per request: six materially different greenfield/problems produce six
// different questions, each naming its own object. A canned question cannot
// satisfy this.
func TestProposalQuestionsVaryWithTheRequest(t *testing.T) {
	requests := []string{
		"Create a small portfolio site for John Smith.",
		"Create a backend service that accepts webhooks and retries failed deliveries.",
		"Create a CLI that converts CSV to JSON.",
		"Add support for this new configuration format.",
		"Add support for this new data source.",
		"Add validation for this input format.",
	}
	seen := map[string]string{}
	for _, request := range requests {
		proposal, ok := AssessProposal(Build, request, emptyWorkspace())
		if !ok {
			t.Errorf("%q produced no proposal", request)
			continue
		}
		if proposal.Subject == "" || !strings.Contains(proposal.Question, proposal.Subject) {
			t.Errorf("%q question %q does not quote its subject %q", request, proposal.Question, proposal.Subject)
		}
		if previous, dup := seen[proposal.Question]; dup {
			t.Errorf("%q and %q share the question %q", previous, request, proposal.Question)
		}
		seen[proposal.Question] = request
	}
	if len(seen) != len(requests) {
		t.Fatalf("distinct questions = %d, want %d", len(seen), len(requests))
	}
}

// TestProposalIsLanguageAgnostic is the negative test for project-type
// triggers: requests naming Go, Python, and Rust work produce the same fixed
// wording, differing only in the user's own subject. No proposal copy names a
// language the request did not name.
func TestProposalIsLanguageAgnostic(t *testing.T) {
	structures := []string{
		"Add support for this new configuration format.",
		"Add support for this new data source.",
		"Add validation for this input format.",
	}
	var normalized []string
	var optionSets []string
	for _, request := range structures {
		proposal, ok := AssessProposal(Build, request, emptyWorkspace())
		if !ok {
			t.Fatalf("%q produced no proposal", request)
		}
		var labels []string
		for _, option := range proposal.Options {
			if option.ID != ProposalOptionOneFile &&
				option.ID != ProposalOptionMultipleFiles &&
				option.ID != ProposalOptionModelDecides {
				t.Errorf("%q has a non-structural option %q", request, option.ID)
			}
			labels = append(labels, option.ID+"="+option.Label)
		}
		optionSets = append(optionSets, strings.Join(labels, "|"))
		normalized = append(normalized, strings.Replace(proposal.Question, proposal.Subject, "<subject>", 1))
		for _, word := range []string{"go", "golang", "python", "rust", "html", "css", "javascript", "react"} {
			if containsWord(proposal.Question+" "+proposal.Reason, word) {
				t.Errorf("%q proposal names project vocabulary %q", request, word)
			}
		}
	}
	for i := 1; i < len(optionSets); i++ {
		if optionSets[i] != optionSets[0] {
			t.Errorf("structure options differ by request: %q vs %q", optionSets[0], optionSets[i])
		}
	}
	for i := 1; i < len(normalized); i++ {
		if normalized[i] != normalized[0] {
			t.Errorf("fixed question wording differs by request: %q vs %q", normalized[0], normalized[i])
		}
	}
}

// TestProposalAlternativeDimensionUsesTheRequestsOwnOptions is Case J: a
// genuinely different decision dimension is represented through the same
// proposal type, with the user's own alternatives rather than file-count
// vocabulary.
func TestProposalAlternativeDimensionUsesTheRequestsOwnOptions(t *testing.T) {
	const request = "Implement config loading. Extend the existing loader vs introduce a new loader path."
	proposal, ok := AssessProposal(Build, request, emptyWorkspace())
	if !ok {
		t.Fatal("an explicit choice between two approaches produced no proposal")
	}
	if proposal.Dimension != ProposalAlternative {
		t.Errorf("dimension = %q, want alternative", proposal.Dimension)
	}
	if proposal.Subject != "config loading" {
		t.Errorf("subject = %q, want config loading", proposal.Subject)
	}
	if !strings.Contains(proposal.Question, "config loading") {
		t.Errorf("question %q does not name the request's object", proposal.Question)
	}
	want := []string{"Extend the existing loader", "Introduce a new loader path", "Let the model decide"}
	if len(proposal.Options) != len(want) {
		t.Fatalf("options = %+v, want %d", proposal.Options, len(want))
	}
	for i, label := range want {
		if proposal.Options[i].Label != label {
			t.Errorf("option %d label = %q, want %q", i, proposal.Options[i].Label, label)
		}
	}
	for _, option := range proposal.Options[:2] {
		if !strings.Contains(option.Directive, option.Label) {
			t.Errorf("option %q lost its request-derived directive", option.Label)
		}
	}
	for _, option := range proposal.Options {
		if strings.Contains(option.Label, "Single file") || strings.Contains(option.Label, "Multiple files") {
			t.Errorf("an alternative proposal reused structural options: %+v", proposal.Options)
		}
	}
}

// TestProposalAlternativeUsesEitherForm proves the second explicit choice
// syntax, so the mechanism is not tied to one connector.
func TestProposalAlternativeUsesEitherForm(t *testing.T) {
	const request = "Implement config loading, either by extending the existing loader or by introducing a new loader path."
	proposal, ok := AssessProposal(Build, request, emptyWorkspace())
	if !ok {
		t.Fatal("an either/or choice produced no proposal")
	}
	if proposal.Dimension != ProposalAlternative {
		t.Errorf("dimension = %q, want alternative", proposal.Dimension)
	}
	if proposal.Subject != "config loading" {
		t.Errorf("subject = %q, want the request's object", proposal.Subject)
	}
	if !strings.Contains(strings.ToLower(proposal.Options[0].Label), "extending the existing loader") ||
		!strings.Contains(strings.ToLower(proposal.Options[1].Label), "introducing a new loader path") {
		t.Errorf("options lost the request's alternatives: %+v", proposal.Options)
	}
}

// TestProposalSuppressesExplicitStructure is Case G: a request that names its
// targets or declares its own layout never opens the boundary.
func TestProposalSuppressesExplicitStructure(t *testing.T) {
	for _, request := range []string{
		"Put this in internal/config/loader.go and add tests in internal/config/loader_test.go.",
		"create a single-file HTML portfolio",
		"create an HTML/CSS/JS portfolio",
		"create a portfolio as a single file",
		"build the tool as a library",
		"write the script across multiple files",
	} {
		if proposal, ok := AssessProposal(Build, request, emptyWorkspace()); ok {
			t.Errorf("%q proposed despite declaring its own structure: %+v", request, proposal)
		}
	}
}

// TestProposalSuppressesExistingConvention is Case H: the same greenfield
// request inside an established project inherits the convention.
func TestProposalSuppressesExistingConvention(t *testing.T) {
	if proposal, ok := AssessProposal(Build,
		"Create a small portfolio site for John Smith.", establishedWorkspace()); ok {
		t.Errorf("a request inside an established project proposed: %+v", proposal)
	}
}

// TestProposalSuppressesOrdinaryImplementationDetail is Case I: sufficiently
// specified change work and ordinary code units do not interrupt.
func TestProposalSuppressesOrdinaryImplementationDetail(t *testing.T) {
	for _, request := range []string{
		"Fix the off-by-one error in the paginator.",
		"Refactor the loader.",
		"Rename the parse function to parseInput.",
		"Implement a helper function.",
		"Add a test for the parser.",
		"Create a README.",
		"Create something.",
		"Make it work.",
		"run the tests",
		"explain how this works",
		"why does this function return nil here?",
		"reproduce the failure",
	} {
		if proposal, ok := AssessProposal(Build, request, emptyWorkspace()); ok {
			t.Errorf("%q proposed despite ordinary or anchored work: %+v", request, proposal)
		}
	}
	// The same ordinary change inside a real project is anchored by the
	// repository even though its object is concrete.
	if proposal, ok := AssessProposal(Build,
		"Add a timeout of 30 seconds to the HTTP client.", establishedWorkspace()); ok {
		t.Errorf("ordinary change work inside a convention proposed: %+v", proposal)
	}
}

// TestProposalOnlyRunsUnderMutationAuthority pins the authority guard.
func TestProposalOnlyRunsUnderMutationAuthority(t *testing.T) {
	const request = "Create a small portfolio site for John Smith."
	for _, active := range []Workspace{Ask, Fuzz, Workspace("")} {
		if proposal, ok := AssessProposal(active, request, emptyWorkspace()); ok {
			t.Errorf("active=%q proposed: %+v", active, proposal)
		}
	}
}

// TestProposalGenericityMatrix pins the adversarial cases the design must
// answer without a project-type detector. Each row states the request class,
// whether a decision is genuinely unresolved, and which dimension carries it.
func TestProposalGenericityMatrix(t *testing.T) {
	cases := []struct {
		name      string
		request   string
		facts     ProposalFacts
		want      bool
		dimension ProposalDimension
	}{
		{
			name:      "A static website",
			request:   "Create a small portfolio site for John Smith.",
			want:      true,
			dimension: ProposalStructure,
		},
		{
			name:      "B go configuration format",
			request:   "Add support for this new configuration format.",
			want:      true,
			dimension: ProposalStructure,
		},
		{
			name:      "C python data source",
			request:   "Add support for this new data source.",
			want:      true,
			dimension: ProposalStructure,
		},
		{
			name:      "D rust input validation",
			request:   "Add validation for this input format.",
			want:      true,
			dimension: ProposalStructure,
		},
		{
			name:      "E backend service",
			request:   "Create a backend service that accepts webhooks and retries failed deliveries.",
			want:      true,
			dimension: ProposalStructure,
		},
		{
			name:      "F cli tooling",
			request:   "Create a CLI that converts CSV to JSON.",
			want:      true,
			dimension: ProposalStructure,
		},
		{
			name:    "G explicit structure",
			request: "Put this in internal/config/loader.go and add tests in internal/config/loader_test.go.",
			want:    false,
		},
		{
			name:    "H existing convention",
			request: "Create a small portfolio site for John Smith.",
			facts:   establishedWorkspace(),
			want:    false,
		},
		{
			name:    "I ordinary implementation detail",
			request: "Implement a helper function.",
			want:    false,
		},
		{
			name:      "J different unresolved dimension",
			request:   "Implement config loading. Extend the existing loader vs introduce a new loader path.",
			want:      true,
			dimension: ProposalAlternative,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			proposal, ok := AssessProposal(Build, tc.request, tc.facts)
			if ok != tc.want {
				t.Fatalf("proposal = %v, want %v (%+v)", ok, tc.want, proposal)
			}
			if !ok {
				return
			}
			if proposal.Dimension != tc.dimension {
				t.Errorf("dimension = %q, want %q", proposal.Dimension, tc.dimension)
			}
			if proposal.Question == "" || proposal.Reason == "" {
				t.Error("the proposal dropped its user-facing copy")
			}
			if proposal.Dimension == ProposalStructure {
				if proposal.Subject == "" || !strings.Contains(proposal.Question, proposal.Subject) {
					t.Errorf("question %q is not grounded in subject %q", proposal.Question, proposal.Subject)
				}
			}
			if proposal.Dimension == ProposalAlternative {
				if len(proposal.Options) < 2 {
					t.Fatalf("alternative proposal has no alternatives: %+v", proposal.Options)
				}
				if !strings.Contains(proposal.Options[0].Label, "Extend the existing loader") {
					t.Errorf("first option = %q, want the request's own approach", proposal.Options[0].Label)
				}
			}
		})
	}
}

// TestProposalNeverMutatesTheChoiceSet pins copy safety across assessments.
func TestProposalNeverMutatesTheChoiceSet(t *testing.T) {
	const request = "Create a small portfolio site for John Smith."
	first, ok := AssessProposal(Build, request, emptyWorkspace())
	if !ok {
		t.Fatal("no proposal")
	}
	first.Options[0].Label = "rewritten"
	second, ok := AssessProposal(Build, request, emptyWorkspace())
	if !ok {
		t.Fatal("no second proposal")
	}
	if second.Options[0].Label == "rewritten" {
		t.Error("assessments share the option backing array")
	}
}

// containsWord reports a whole-word match, so "go" does not match "organize".
func containsWord(text, word string) bool {
	lower := strings.ToLower(text)
	for _, field := range strings.FieldsFunc(lower, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_'
	}) {
		if field == word {
			return true
		}
	}
	return false
}
