package agent

import (
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/control/contract"
)

// The vocabulary checks know a finite set of predicates. This answer uses a
// verb they do not, and names a code entity no observation contains. The
// general association check must still catch it.
func TestEntityClaimRejectsInventedIdentifierWithNovelPredicate(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		readOutcome("internal/order/service.go", "package order\n\nfunc NewOrder() {}\n"),
	}}}
	answer := "The `PaymentReconciler` harmonizes ledgers before settlement."
	issues := claimScopeIssues(answer, turns)
	if len(issues) == 0 || issues[0].Code != "unsupported_code_entity" {
		t.Fatalf("issues = %+v, want an unsupported_code_entity", issues)
	}
	if !strings.Contains(issues[0].Detail, "PaymentReconciler") {
		t.Fatalf("detail = %q, want the invented identifier", issues[0].Detail)
	}
}

// A name that the run actually observed is supported even when the sentence
// uses the same novel predicate.
func TestEntityClaimAcceptsObservedIdentifier(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		readOutcome("internal/order/service.go",
			"package order\n\ntype PaymentReconciler struct{}\n\nfunc (PaymentReconciler) Harmonize() {}\n"),
	}}}
	answer := "The `PaymentReconciler` harmonizes ledgers before settlement."
	if issues := claimScopeIssues(answer, turns); len(issues) != 0 {
		t.Fatalf("an observed identifier was reviewed: %+v", issues)
	}
}

// Boundary matching prevents a short name from being supported by a longer
// observed identifier, and vice versa.
func TestEntityClaimDoesNotMatchInsideAnotherIdentifier(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		readOutcome("internal/order/service.go", "package order\n\nfunc NewOrderView() {}\n"),
	}}}
	answer := "The `Order` type controls checkout."
	issues := claimScopeIssues(answer, turns)
	if len(issues) == 0 || issues[0].Code != "unsupported_code_entity" {
		t.Fatalf("issues = %+v, want a boundary miss", issues)
	}
}

// Without an observation there is no baseline, so a resumed answer is not
// refuted by this run.
func TestEntityClaimSkipsWithoutObservations(t *testing.T) {
	answer := "The `PaymentReconciler` harmonizes ledgers before settlement."
	if issues := claimScopeIssues(answer, nil); len(issues) != 0 {
		t.Fatalf("a claim was reviewed without a baseline: %+v", issues)
	}
}

// Advice about an external API is not a project fact. The general check must
// not turn a recommendation into a completion review.
func TestEntityClaimSkipsImperativeAdvice(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		readOutcome("internal/order/service.go", "package order\n\nfunc NewOrder() {}\n"),
	}}}
	for _, answer := range []string{
		"Use `fmt.Errorf` with `%w` so cancellation survives wrapping.",
		"Prefer `errors.Is` over string matching.",
		"The `PaymentReconciler` was not found in the inspected source.",
	} {
		if issues := claimScopeIssues(answer, turns); len(issues) != 0 {
			t.Fatalf("non-factual answer was reviewed: %q -> %+v", answer, issues)
		}
	}
}

// The general association check reaches the completion boundary through the
// same grounding assessment as the vocabulary checks.
func TestEntityClaimReachesGroundingAssessment(t *testing.T) {
	intent := contract.IntentContract{
		Kind:      contract.ObjectiveInformational,
		Grounding: &contract.IntentGrounding{},
	}
	turns := []Turn{{Results: []ToolOutcome{
		readOutcome("internal/order/service.go", "package order\n\nfunc NewOrder() {}\n"),
	}}}
	answer := "The `PaymentReconciler` harmonizes ledgers before settlement."
	assessment := AssessGrounding(intent, turns, answer)
	if assessment.Pass() || !strings.Contains(assessment.Reason(), "PaymentReconciler") {
		t.Fatalf("grounding = %+v, want an entity review", assessment)
	}
}

// A negative existential claim about a code entity is contradicted by a
// positive observation of that entity, regardless of the wording used.
func TestEntityClaimRejectsAbsenceContradictedByObservation(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		readOutcome("internal/order/service.go", "package order\n\ntype PaymentReconciler struct{}\n"),
	}}}
	for _, answer := range []string{
		"The `PaymentReconciler` does not exist in this codebase.",
		"There is no `PaymentReconciler` in the project.",
	} {
		issues := claimScopeIssues(answer, turns)
		if len(issues) == 0 || issues[0].Code != "unsupported_entity_absence" {
			t.Fatalf("answer %q -> issues %+v, want an entity-absence contradiction", answer, issues)
		}
	}
}

// A correction must quote the identifier it withdraws. That quotation is an
// epistemic statement about the earlier answer, not a claim that the wrong
// qualified name is a real symbol; without this, a model cannot recover from a
// naming slip because the retraction itself still contains the name.
func TestEntityClaimSkipsRetractedIdentifier(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		readOutcome("backend/reviews/domain/review.go", "package domain\n\nfunc NewReview() {}\n"),
	}}}
	for _, answer := range []string{
		"Correction: I should not have called it `reviews.NewReview`. The observed declaration is `NewReview` in `backend/reviews/domain/review.go`, in package `domain`.",
		"The correct name is `NewReview` instead of `reviews.NewReview`.",
	} {
		if issues := claimScopeIssues(answer, turns); len(issues) != 0 {
			t.Fatalf("a retracted identifier was reviewed as a claim: %q -> %+v", answer, issues)
		}
	}
	intent := contract.IntentContract{
		Kind:      contract.ObjectiveInformational,
		Grounding: &contract.IntentGrounding{},
	}
	correction := "Correction: I should not have called it `reviews.NewReview`. The observed declaration is `NewReview` in `backend/reviews/domain/review.go`, in package `domain`."
	if assessment := AssessGrounding(intent, turns, correction); !assessment.Pass() {
		t.Fatalf("grounding rejected the correction it asked for: %s", assessment.Reason())
	}
	// A plain assertion of the same wrong qualifier is still a claim.
	assertion := "The `reviews.NewReview` constructor trims the body before storing it."
	issues := claimScopeIssues(assertion, turns)
	if len(issues) == 0 || issues[0].Code != "unsupported_code_entity" {
		t.Fatalf("issues = %+v, want an unsupported_code_entity", issues)
	}
}

// A package-qualified reference names the declaration by its package. The run
// read backend/checkout/bounded_context.go, which declares `func New`, and the
// answer wrote the import-path spelling `checkout.New`. Requiring the literal
// dotted token reviewed a source-verified constructor as invented.
func TestEntityClaimAcceptsPackageQualifiedDeclaration(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		readOutcome("backend/checkout/bounded_context.go",
			"1\tpackage checkout\n2\t\n3\tfunc New(db *sql.DB) {}\n"),
	}}}
	answer := "The `checkout.New` constructor takes locally-declared interfaces."
	if issues := claimScopeIssues(answer, turns); len(issues) != 0 {
		t.Fatalf("a package-qualified observed declaration was reviewed: %+v", issues)
	}

	// A member the package does not declare is still invented.
	invented := "The `checkout.Reconcile` constructor takes locally-declared interfaces."
	if issues := claimScopeIssues(invented, turns); len(issues) == 0 {
		t.Fatal("an undeclared package member was accepted")
	}
}

// An answer names a method by its type (`StockReserver.Reserve`,
// `httpHandler.Checkout`). The declaration spells it as an interface method or
// a receiver method, so the literal dotted token never appears. A member the
// type does not declare is still invented.
func TestEntityClaimAcceptsTypeQualifiedMembers(t *testing.T) {
	source := "type StockReserver interface {\n\tReserve(ctx context.Context, quantities map[string]int) error\n}\n\n" +
		"type OrderStorage interface {\n\tSave(ctx context.Context, order *Order) error\n}\n\n" +
		"func (handler httpHandler) Checkout(w http.ResponseWriter, r *http.Request) {}\n"
	turns := []Turn{{Results: []ToolOutcome{
		readOutcome("backend/checkout/app/checkout.go", source),
	}}}
	answer := "`StockReserver.Reserve`, `OrderStorage.Save`, and `httpHandler.Checkout` are the seams."
	if issues := claimScopeIssues(answer, turns); len(issues) != 0 {
		t.Fatalf("type-qualified observed members were reviewed: %+v", issues)
	}
	invented := "`StockReserver.Save` and `httpHandler.Place` are the seams."
	if issues := claimScopeIssues(invented, turns); len(issues) == 0 {
		t.Fatal("members the types do not declare were accepted")
	}
}

// The runtime state block prints evidence statuses in backticks. Quoting
// `NOT_OBSERVED` is a status report, not an invented declaration.
func TestEntityClaimIgnoresRuntimeStatusTokens(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		readOutcome("main.go", "package main\n"),
	}}}
	answer := "Runtime was `NOT_OBSERVED`; the index is `PARTIAL` and `UNPARSED`."
	if issues := claimScopeIssues(answer, turns); len(issues) != 0 {
		t.Fatalf("runtime status tokens were reviewed as code entities: %+v", issues)
	}
}

// Session 963: the answer named `cart.domain.Product`, an import-path
// spelling of a type the read file uses as `domain.Product`. The file imports
// `.../cart/domain`; requiring the literal dotted token reviewed a
// source-observed declaration as invented and kept the correction loop alive.
func TestEntityClaimAcceptsImportPathQualifiedReference(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		readOutcome("backend/cart/bounded_context.go", `package cart

import (
	"github.com/bkielbasa/go-ecommerce/backend/cart/domain"
)

type transformProductCatalog struct{}

func (tpc transformProductCatalog) Find() (domain.Product, error) {
	return domain.Product{}, nil
}
`),
	}}}
	answer := "The ACL translates a product catalog variant into `cart.domain.Product`."
	if issues := claimScopeIssues(answer, turns); len(issues) != 0 {
		t.Fatalf("an import-path-qualified observed declaration was reviewed: %+v", issues)
	}
}

// The suffix match must not let an observed `domain.Product` support an
// invented package prefix. The observation names `cart/domain`, not
// `billing/domain`.
func TestEntityClaimRejectsUnobservedPackagePrefix(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		readOutcome("backend/cart/bounded_context.go", `package cart

import "github.com/bkielbasa/go-ecommerce/backend/cart/domain"

func Find() domain.Product { return domain.Product{} }
`),
	}}}
	answer := "The ACL translates a product catalog variant into `billing.domain.Product`."
	issues := claimScopeIssues(answer, turns)
	if len(issues) == 0 || issues[0].Code != "unsupported_code_entity" {
		t.Fatalf("issues = %+v, want an unsupported package prefix", issues)
	}
}

// A backticked filename names a path coordinate, not a declaration. The read
// observed package.json; reviewing the name as an invented identifier would
// send the model to re-read the file instead of repairing the absence claim.
func TestEntityClaimSkipsFileCoordinates(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		readOutcome("package.json", `{"scripts":{"dev":"next dev","build":"next build"}}`),
	}}}
	answer := "`package.json` has no test script, so the project has no tests."
	for _, issue := range claimScopeIssues(answer, turns) {
		if issue.Code == "unsupported_code_entity" {
			t.Fatalf("a filename was reviewed as a code entity: %+v", issue)
		}
	}
}

// A sentence that names an identifier only to disown it is a limitation, not
// an assertion. Session 973: the model removed the unsupported
// `cart.domain.Product` claim and added "No claim is made about ...", but the
// entity audit reviewed the disclaimer as the original claim and the loop
// could not terminate.
func TestEntityClaimSkipsDisclaimedIdentifier(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		readOutcome("backend/cart/bounded_context.go", "package cart\n"),
	}}}
	disclaimer := "No claim is made about a `cart.domain.Product` type or similar internal identifiers."
	if issues := claimScopeIssues(disclaimer, turns); len(issues) != 0 {
		t.Fatalf("a disclaimer was reviewed as an assertion: %+v", issues)
	}
	assertion := "The ACL translates a variant into `cart.domain.Product`."
	issues := claimScopeIssues(assertion, turns)
	if len(issues) == 0 || issues[0].Code != "unsupported_code_entity" {
		t.Fatalf("a plain assertion was accepted: %+v", issues)
	}
}
