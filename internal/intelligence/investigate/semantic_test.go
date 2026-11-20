package investigate

import "testing"

// workspace/symbol is not a natural-language search. A targeted question names
// its target in Focus, and that is the text the language server must resolve.
func TestSemanticQueryPrefersFocus(t *testing.T) {
	if got := semanticQuery(Request{Question: "who calls NewOrder?", Focus: "NewOrder"}); got != "NewOrder" {
		t.Fatalf("semantic query = %q, want the focus symbol", got)
	}
	question := "explain the checkout architecture"
	if got := semanticQuery(Request{Question: question}); got != question {
		t.Fatalf("open-ended semantic query = %q, want the question", got)
	}
}
