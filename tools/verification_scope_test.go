package tools

import "testing"

// A successful check is evidence only for the packages it named. Misreporting
// the scope would let "go test ./ast" stand in for a broken "tools" change.
func TestVerificationEvidenceRecordsCommandScope(t *testing.T) {
	cases := []struct {
		command   string
		required  string
		wantCover bool
	}{
		{"go test ./tools", "tools", true},
		{"go test ./tools/...", "tools/sub", true},
		{"go test ./tools", "tools/sub", false},
		{"go test ./...", "anything", true},
		{"go test .", ".", true},
		{"go test .", "tools", false},
		{"go vet ./ast", "tools", false},
		{"go build ./tools", "tools", true},
	}
	for _, tc := range cases {
		evidence := VerificationFor(tc.command)
		if evidence == nil {
			t.Errorf("VerificationFor(%q) = nil, want evidence", tc.command)
			continue
		}
		if got := evidence.Covers(tc.required); got != tc.wantCover {
			t.Errorf("VerificationFor(%q).Covers(%q) = %v, want %v (scopes %+v)",
				tc.command, tc.required, got, tc.wantCover, evidence.Scopes)
		}
	}
	if VerificationFor("go test ./tools || true") != nil {
		t.Fatal("a check masked by || true was accepted as verification")
	}
}

// A shell command may change directory before checking. Package arguments are
// relative to the directory the check actually ran in; a check outside the
// workspace is not evidence for it, and an inside directory must be rebased.
func TestVerificationEvidenceRebasesDirectoryChanges(t *testing.T) {
	root := t.TempDir()
	cases := []struct {
		name     string
		command  string
		required string
		wantOK   bool
	}{
		{"inside-recursive", "cd tools && go test ./...", "tools", true},
		{"inside-recursive-child", "cd tools && go test ./...", "tools/sub", true},
		{"inside-exact", "cd tools && go test .", "tools", true},
		{"inside-parent", "cd tools && go test ../ast", "ast", true},
		{"outside-absolute", "cd /tmp && go test ./tools", "tools", false},
		{"outside-parent", "cd .. && go test ./tools", "tools", false},
		{"outside-then-relative", "cd /tmp && cd tools && go test ./...", "tools", false},
		{"unknown-expansion", "cd $HOME && go test ./tools", "tools", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			evidence := VerificationForIn(root, tc.command)
			if evidence == nil {
				if tc.wantOK {
					t.Fatalf("VerificationForIn(%q) = nil", tc.command)
				}
				return
			}
			if got := evidence.Covers(tc.required); got != tc.wantOK {
				t.Fatalf("Covers(%q) = %v, want %v (scopes %+v)",
					tc.required, got, tc.wantOK, evidence.Scopes)
			}
		})
	}

	// Without a root the command is still a check, but there is no way to
	// rebase its directory. It must not claim coverage of the workspace; that
	// would be the false-completion shape.
	for _, command := range []string{
		"cd /tmp && go test ./tools",
		"cd tools && go test ./...",
	} {
		evidence := VerificationFor(command)
		if evidence == nil {
			t.Fatalf("VerificationFor(%q) = nil, want a recognised check", command)
		}
		if evidence.Covers("tools") {
			t.Fatalf("VerificationFor(%q) claimed workspace coverage without a root", command)
		}
	}
}

// The kind of check is evidence too: a passing build is not a passing test.
func TestVerificationEvidenceRecordsCheckKind(t *testing.T) {
	cases := map[string]string{
		"go test ./tools":   "test",
		"go build ./tools":  "build",
		"go vet ./tools":    "vet",
		"pytest -q":         "test",
		"tsc --noEmit":      "build",
		"staticcheck ./...": "vet",
	}
	for command, want := range cases {
		evidence := VerificationFor(command)
		if evidence == nil {
			t.Errorf("VerificationFor(%q) = nil, want evidence", command)
			continue
		}
		if got := string(evidence.EffectiveCheck()); got != want {
			t.Errorf("VerificationFor(%q).Check = %q, want %q", command, got, want)
		}
	}
}

// A recognised non-Go check is still evidence, but it carries no Go package
// scope. The evaluator must not treat that as coverage of a Go target.
func TestVerificationEvidenceDoesNotInventScope(t *testing.T) {
	evidence := VerificationFor("pytest -q")
	if evidence == nil {
		t.Fatal("pytest was not recognised")
	}
	if len(evidence.Scopes) != 0 {
		t.Fatalf("scopes = %+v, want none", evidence.Scopes)
	}
	if evidence.Covers("tools") {
		t.Fatal("an unscoped check claimed to cover tools")
	}
}
