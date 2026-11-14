package retrieval

import (
	"reflect"
	"strings"
	"testing"
)

func TestSplitIdentifier(t *testing.T) {
	cases := map[string][]string{
		"AuthMiddleware":  {"auth", "middleware"},
		"user_session":    {"user", "session"},
		"HTTPServer":      {"http", "server"},
		"utf8Reader":      {"utf8", "reader"},
		"payment.Process": {"payment", "process"},
		"parseURLPath":    {"parse", "url", "path"},
		"get_user-byID":   {"get", "user", "by", "id"},
	}
	for input, want := range cases {
		if got := SplitIdentifier(input); !reflect.DeepEqual(got, want) {
			t.Errorf("SplitIdentifier(%q) = %v, want %v", input, got, want)
		}
	}
}

func TestPathTokensCoverDirectoriesStemAndParts(t *testing.T) {
	got := PathTokens("backend/auth/middleware/sessionStore.go")
	want := []string{"backend", "auth", "middleware", "sessionstore", "session", "store"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("PathTokens = %v, want %v", got, want)
	}
}

func TestSourceTokensIgnoresStringLiteralsAndCapturesComments(t *testing.T) {
	source := `package auth

// Handles authentication for privileged requests.
func Checkout() {
	message := "authMiddlewareSecretValue"
	_ = strings.ToUpper(message)
	/* Billing retry helper */
}
`
	identifiers, comments := SourceTokens([]byte(source))
	if contains(identifiers, "authmiddlewaresecretvalue") {
		t.Fatal("string literal content leaked into identifier tokens")
	}
	for _, want := range []string{"checkout", "auth"} {
		if !contains(identifiers, want) {
			t.Errorf("identifiers missing %q: %v", want, identifiers)
		}
	}
	for _, want := range []string{"handles", "authentication", "privileged", "billing", "retry", "helper"} {
		if !contains(comments, want) {
			t.Errorf("comments missing %q: %v", want, comments)
		}
	}
}

func TestSourceTokensHandlesQualifiedNames(t *testing.T) {
	identifiers, _ := SourceTokens([]byte("payment.Process(ctx)\nobj.Method.Chain()\n"))
	for _, want := range []string{"payment", "process", "payment.process", "obj", "method", "chain", "obj.method.chain"} {
		if !contains(identifiers, want) {
			t.Errorf("identifiers missing %q: %v", want, identifiers)
		}
	}
}

func TestQueryTermsKeepExactWordsAndParts(t *testing.T) {
	weights, exact := queryTerms("HandleRequest authentication middleware", 32)
	for _, want := range []string{"handlerequest", "handle", "request", "authentication", "middleware"} {
		if weights[want] == 0 {
			t.Errorf("query term %q missing: %v", want, weights)
		}
	}
	if !contains(exact, "handlerequest") {
		t.Errorf("exact terms = %v, want the compound identifier", exact)
	}
}

func TestQueryTermsCapRepeatedTerms(t *testing.T) {
	weights, _ := queryTerms(strings.Repeat("checkout ", 10), 32)
	if weights["checkout"] != 3 {
		t.Fatalf("repeated term weight = %d, want the cap of 3", weights["checkout"])
	}
}

func TestDocumentTermFrequenciesAndBounds(t *testing.T) {
	opts := DefaultOptions()
	opts.MaxTermsPerDoc = 4
	doc := DocumentFromSource("a/b.go", "go", "go_source", false, 10, 1,
		[]byte("func Alpha() { alpha(); alpha(); beta() }\n"), opts)
	if doc.Terms["alpha"][FieldIdentifier] != 3 {
		t.Fatalf("alpha tf = %d, want 3", doc.Terms["alpha"][FieldIdentifier])
	}
	if len(doc.Terms) != 4 || !doc.Truncated {
		t.Fatalf("term bound not applied: %d terms truncated=%v", len(doc.Terms), doc.Truncated)
	}
	if doc.Length[FieldIdentifier] != 5 {
		t.Fatalf("document length = %d, want every one of the five identifier tokens counted before the term cut", doc.Length[FieldIdentifier])
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
