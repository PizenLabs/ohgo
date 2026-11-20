package workspace

import (
	"reflect"
	"sort"
	"testing"

	"github.com/PizenLabs/ohgo/internal/control/contract"
)

func TestRequiredFacetsExtractsExplicitDeliverables(t *testing.T) {
	cases := []struct {
		request string
		want    []Facet
	}{
		{
			request: "Report the callers of NewOrder, the tests, and affected packages.",
			want:    []Facet{FacetCallers, FacetImpact, FacetTests},
		},
		{
			request: "Explain the architecture and list the entry points.",
			want:    []Facet{FacetEntrypoints},
		},
		{
			request: "Which tests cover TerminalBlog, and who imports it?",
			want:    []Facet{FacetImporters, FacetTests},
		},
		{
			request: "Investigate this project and analyze what this project is about.",
			want:    nil,
		},
		{
			request: "Investigate why the tests fail and explain the cause.",
			want:    nil,
		},
		{
			request: "Why does this function use a mutex?",
			want:    nil,
		},
	}
	for _, test := range cases {
		intent := BuildIntentContract(test.request)
		var got []Facet
		for _, facet := range intent.RequiredFacets {
			got = append(got, facet.Facet)
		}
		sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
		want := append([]Facet(nil), test.want...)
		sort.Slice(want, func(i, j int) bool { return want[i] < want[j] })
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%q facets = %v, want %v", test.request, got, test.want)
		}
	}
}

// A mutation objective is judged by targets and verification. Its prose may
// name tests as context; that must not become an answer-field obligation.
func TestRequiredFacetsDoNotApplyToMutationObjectives(t *testing.T) {
	intent := BuildIntentContract("Fix the callers of NewOrder and run the tests.")
	if intent.Kind != contract.ObjectiveMutation {
		t.Fatalf("kind = %s, want mutation", intent.Kind)
	}
	if len(intent.RequiredFacets) != 0 {
		t.Fatalf("mutation facets = %+v, want none", intent.RequiredFacets)
	}
}
