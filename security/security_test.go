package security

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// fakeAuthority is a test authority with a fixed capability set.
type fakeAuthority struct {
	name string
	caps map[Capability]bool
}

func (a fakeAuthority) Name() string { return a.name }
func (a fakeAuthority) Permits(cap Capability) bool {
	return a.caps[cap]
}

// buildAuthority permits read/write/exec/network, standing in for BUILD.
func buildAuthority() fakeAuthority {
	return fakeAuthority{name: "build", caps: map[Capability]bool{
		CapabilityRead: true, CapabilityWrite: true, CapabilityExec: true, CapabilityNetwork: true,
		CapabilityExternalSearch: true, CapabilityExternalMCPRead: true,
		CapabilityExternalMCPMutate: true, CapabilityExternalBrowserRead: true,
		CapabilityExternalBrowserMutate: true,
	}}
}

func TestCapabilityKnown(t *testing.T) {
	if !CapabilityRead.Known() || !CapabilityExec.Known() {
		t.Fatal("a defined capability was reported unknown")
	}
	if Capability("future").Known() {
		t.Fatal("an undefined capability was reported known")
	}
}

func TestNormalizeScopeClassifiesTargets(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A symlink inside the workspace that points out must not be usable to
	// reach the outside target.
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name  string
		raw   string
		class ScopeClass
	}{
		{"relative", "src/main.go", ScopeInside},
		{"absolute inside", filepath.Join(root, "main.go"), ScopeInside},
		{"dot", ".", ScopeInside},
		{"parent that stays inside", "src/../main.go", ScopeInside},
		{"parent that escapes", "../secrets", ScopeOutside},
		{"absolute outside", filepath.Join(outside, "f"), ScopeOutside},
		{"symlink escape", "escape/f", ScopeOutside},
		{"empty is unspecified", "", ScopeUnspecified},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := NormalizeScope(root, tc.raw)
			if got.Class != tc.class {
				t.Errorf("class = %s, want %s (path %q)", got.Class, tc.class, got.Path)
			}
		})
	}
}

func TestNormalizeScopeIsCanonical(t *testing.T) {
	// Two spellings of one path must share a canonical form, so a grant for one
	// covers the other and neither covers an escape.
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	one := NormalizeScope(root, "a.go")
	two := NormalizeScope(root, filepath.Join(root, "a.go"))
	if !one.Same(two) {
		t.Errorf("equivalent spellings differ: %+v vs %+v", one, two)
	}
	escape := NormalizeScope(root, "../a.go")
	if one.Same(escape) {
		t.Error("an escape compared equal to an in-workspace path")
	}
}

func TestClassifyRiskIsDeterministic(t *testing.T) {
	inside := Scope{Class: ScopeInside, Path: "/w/f"}
	outside := Scope{Class: ScopeOutside, Path: "/other/f"}

	cases := []struct {
		name        string
		cap         Capability
		scope       Scope
		mutates     bool
		destructive bool
		want        Risk
	}{
		{"read inside", CapabilityRead, inside, false, false, RiskLow},
		{"read outside", CapabilityRead, outside, false, false, RiskHigh},
		{"write inside", CapabilityWrite, inside, true, false, RiskMedium},
		{"write outside", CapabilityWrite, outside, true, false, RiskHigh},
		{"exec ordinary", CapabilityExec, inside, false, false, RiskMedium},
		{"exec destructive", CapabilityExec, inside, true, true, RiskHigh},
		{"network", CapabilityNetwork, Scope{Class: ScopeExternal}, false, false, RiskMedium},
		{"external search", CapabilityExternalSearch, Scope{Class: ScopeExternal}, false, false, RiskMedium},
		{"mcp read", CapabilityExternalMCPRead, Scope{Class: ScopeExternal}, false, false, RiskMedium},
		{"browser read", CapabilityExternalBrowserRead, Scope{Class: ScopeExternal}, false, false, RiskMedium},
		{"mcp mutate", CapabilityExternalMCPMutate, Scope{Class: ScopeExternal}, true, false, RiskHigh},
		{"browser mutate", CapabilityExternalBrowserMutate, Scope{Class: ScopeExternal}, true, false, RiskHigh},
		{"unknown capability", Capability("future"), inside, false, false, RiskHigh},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			first := ClassifyRisk(tc.cap, tc.scope, tc.mutates, tc.destructive)
			if first != tc.want {
				t.Fatalf("risk = %s, want %s", first, tc.want)
			}
			for i := 0; i < 5; i++ {
				if again := ClassifyRisk(tc.cap, tc.scope, tc.mutates, tc.destructive); again != first {
					t.Fatalf("risk changed between evaluations: %s then %s", first, again)
				}
			}
		})
	}
}

func lowReadRequest() Request {
	return Request{
		Capability: CapabilityRead,
		Scope:      Scope{Class: ScopeInside, Path: "/w/a.go"},
		Risk:       RiskLow,
		ToolName:   "read",
	}
}

func TestPolicyAutoForLowRiskInsideWorkspace(t *testing.T) {
	d := (Policy{}).Evaluate(lowReadRequest(), buildAuthority(), nil)
	if d.Outcome != OutcomeAuto {
		t.Fatalf("outcome = %s, want AUTO (%s)", d.Outcome, d.Reason)
	}
	if d.Rule != RuleRiskLow {
		t.Errorf("rule = %s, want %s", d.Rule, RuleRiskLow)
	}
	if d.Capability != CapabilityRead || d.Risk != RiskLow {
		t.Errorf("decision did not echo the request: %+v", d)
	}
}

func TestPolicyApprovesMediumRisk(t *testing.T) {
	req := Request{
		Capability: CapabilityWrite,
		Scope:      Scope{Class: ScopeInside, Path: "/w/a.go"},
		Risk:       RiskMedium,
		Mutates:    true,
	}
	d := (Policy{}).Evaluate(req, buildAuthority(), nil)
	if d.Outcome != OutcomeApprove {
		t.Fatalf("outcome = %s, want APPROVE (%s)", d.Outcome, d.Reason)
	}
}

func TestPolicyDeniesOutsideWorkspace(t *testing.T) {
	req := Request{
		Capability: CapabilityRead,
		Scope:      Scope{Class: ScopeOutside, Path: "/etc/passwd"},
		Risk:       RiskHigh,
	}
	d := (Policy{}).Evaluate(req, buildAuthority(), nil)
	if d.Outcome != OutcomeDeny {
		t.Fatalf("outcome = %s, want DENY", d.Outcome)
	}
	if d.Rule != RuleOutsideWorkspace {
		t.Errorf("rule = %s, want %s", d.Rule, RuleOutsideWorkspace)
	}
}

func TestPolicyDeniesUnknownCapability(t *testing.T) {
	req := Request{Capability: Capability("future"), Risk: RiskHigh}
	d := (Policy{}).Evaluate(req, buildAuthority(), nil)
	if d.Outcome != OutcomeDeny || d.Rule != RuleUnknownCapability {
		t.Fatalf("decision = %+v, want DENY/%s", d, RuleUnknownCapability)
	}
}

func TestPolicyDeniesUnpermittedCapability(t *testing.T) {
	ask := fakeAuthority{name: "ask", caps: map[Capability]bool{CapabilityRead: true}}
	req := Request{
		Capability: CapabilityWrite,
		Scope:      Scope{Class: ScopeInside, Path: "/w/a.go"},
		Risk:       RiskMedium,
		Mutates:    true,
	}
	d := (Policy{}).Evaluate(req, ask, nil)
	if d.Outcome != OutcomeDeny || d.Rule != RuleCapabilityNotPermitted {
		t.Fatalf("decision = %+v, want DENY/%s", d, RuleCapabilityNotPermitted)
	}
}

func TestPolicyDeniesMutationWithoutWriteAuthority(t *testing.T) {
	// FUZZ may execute but not change the repository: a mutating shell command
	// is exec by capability and must still be refused.
	fuzz := fakeAuthority{name: "fuzz", caps: map[Capability]bool{CapabilityRead: true, CapabilityExec: true}}
	req := Request{
		Capability: CapabilityExec,
		Scope:      Scope{Class: ScopeInside, Path: "/w"},
		Risk:       RiskMedium,
		Mutates:    true,
	}
	d := (Policy{}).Evaluate(req, fuzz, nil)
	if d.Outcome != OutcomeDeny || d.Rule != RuleMutationNotPermitted {
		t.Fatalf("decision = %+v, want DENY/%s", d, RuleMutationNotPermitted)
	}
}

// The read-only bit follows the authority, not the command text. Even an
// execution the classifier believes is harmless is marked read-only under an
// authority that cannot write, because the classifier cannot prove the absence
// of a mutation.
func TestPolicyMarksExecReadOnlyWithoutWriteAuthority(t *testing.T) {
	fuzz := fakeAuthority{name: "fuzz", caps: map[Capability]bool{CapabilityRead: true, CapabilityExec: true}}
	req := Request{
		Capability: CapabilityExec,
		Scope:      Scope{Class: ScopeInside, Path: "/w"},
		Risk:       RiskMedium,
		Mutates:    false,
	}
	d := (Policy{}).Evaluate(req, fuzz, nil)
	if !d.ReadOnly {
		t.Fatalf("FUZZ exec decision was not marked read-only: %+v", d)
	}
	if d.Outcome != OutcomeApprove {
		t.Fatalf("outcome = %s, want APPROVE", d.Outcome)
	}

	build := buildAuthority()
	d = (Policy{}).Evaluate(req, build, nil)
	if d.ReadOnly {
		t.Fatalf("BUILD exec decision was marked read-only: %+v", d)
	}
}

func TestPolicyDeniesWithNilAuthority(t *testing.T) {
	d := (Policy{}).Evaluate(lowReadRequest(), nil, nil)
	if d.Outcome != OutcomeDeny {
		t.Fatalf("outcome = %s, want DENY", d.Outcome)
	}
}

func TestGrantCoversExactlyTheApprovedOperation(t *testing.T) {
	req := Request{
		Capability: CapabilityWrite,
		Scope:      Scope{Class: ScopeInside, Path: "/w/a.go"},
		Risk:       RiskMedium,
		Mutates:    true,
		TaskID:     "task-1",
		SessionID:  "sess-1",
	}
	grant := Grant{
		Capability: CapabilityWrite,
		Scope:      req.Scope,
		TaskID:     "task-1",
		SessionID:  "sess-1",
		Provenance: "human approval",
	}
	now := time.Now()
	if !grant.Covers(req, now) {
		t.Fatal("a matching grant did not cover its request")
	}

	widened := req
	widened.Scope = Scope{Class: ScopeOutside, Path: "/etc/passwd"}
	if grant.Covers(widened, now) {
		t.Error("a grant covered a widened scope")
	}

	other := req
	other.TaskID = "task-2"
	if grant.Covers(other, now) {
		t.Error("a grant covered a different task")
	}

	changed := req
	changed.Capability = CapabilityExec
	if grant.Covers(changed, now) {
		t.Error("a grant covered a different capability")
	}
}

func TestGrantDoesNotLeakAcrossOperations(t *testing.T) {
	// Two identical operations in one batch are two approvals. A grant bound to
	// one operation id must not authorize the other.
	base := Request{
		Capability:  CapabilityWrite,
		Scope:       Scope{Class: ScopeInside, Path: "/w/a.go"},
		OperationID: "call-a",
	}
	grant := Grant{Capability: CapabilityWrite, Scope: base.Scope, OperationID: "call-a"}
	if !grant.Covers(base, time.Now()) {
		t.Fatal("a grant did not cover its own operation")
	}
	other := base
	other.OperationID = "call-b"
	if grant.Covers(other, time.Now()) {
		t.Error("a grant leaked to a different operation id")
	}
}

func TestGrantExpiry(t *testing.T) {
	req := lowReadRequest()
	grant := Grant{Capability: CapabilityRead, Scope: req.Scope, ExpiresAt: time.Now().Add(-time.Second)}
	if grant.Covers(req, time.Now()) {
		t.Error("an expired grant covered a request")
	}
}

func TestPolicyHonoursCoveringGrant(t *testing.T) {
	req := Request{
		Capability: CapabilityWrite,
		Scope:      Scope{Class: ScopeInside, Path: "/w/a.go"},
		Risk:       RiskMedium,
		Mutates:    true,
	}
	grant := Grant{Capability: CapabilityWrite, Scope: req.Scope}
	d := (Policy{}).Evaluate(req, buildAuthority(), &grant)
	if d.Outcome != OutcomeAuto || d.Rule != RuleGrant {
		t.Fatalf("decision = %+v, want AUTO/%s", d, RuleGrant)
	}
}

func TestGrantStoreConsumeIsOneShotAndConcurrent(t *testing.T) {
	store := NewGrantStore()
	req := Request{
		Capability: CapabilityWrite,
		Scope:      Scope{Class: ScopeInside, Path: "/w/a.go"},
	}
	store.Add(Grant{Capability: CapabilityWrite, Scope: req.Scope})

	if _, ok := store.Consume(req, time.Now()); !ok {
		t.Fatal("the grant was not consumed")
	}
	if _, ok := store.Consume(req, time.Now()); ok {
		t.Fatal("a consumed grant authorized a second execution")
	}

	// Concurrent consumption must hand the grant to exactly one caller.
	store.Add(Grant{Capability: CapabilityWrite, Scope: req.Scope})
	var wg sync.WaitGroup
	var mu sync.Mutex
	granted := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, ok := store.Consume(req, time.Now()); ok {
				mu.Lock()
				granted++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if granted != 1 {
		t.Fatalf("grants handed out = %d, want 1", granted)
	}
}

// External mutation requires the strongest authority and an approval, but the
// decision must never claim workspace mutation: a remote change cannot demand
// local verification or become repository completion evidence.
func TestExternalMutationIsApprovedButNotWorkspaceMutation(t *testing.T) {
	req := Request{
		Capability: CapabilityExternalMCPMutate,
		Scope:      Scope{Class: ScopeExternal, Path: "mcp:alpha"},
		Risk:       RiskHigh,
		Mutates:    true,
	}
	d := (Policy{}).Evaluate(req, buildAuthority(), nil)
	if d.Outcome != OutcomeApprove {
		t.Fatalf("outcome = %s, want APPROVE (%s)", d.Outcome, d.Reason)
	}
	if d.Mutates {
		t.Fatal("external mutation was reported as workspace mutation")
	}
	if !d.ExternalMutation {
		t.Fatal("external mutation was not marked as external")
	}
	if !d.Capability.External() || !d.Capability.MutationCapable() {
		t.Fatalf("capability classification = %+v", d.Capability)
	}
}

func TestExternalMutationStillRequiresWriteAuthority(t *testing.T) {
	ask := fakeAuthority{name: "ask", caps: map[Capability]bool{
		CapabilityRead: true, CapabilityExternalMCPRead: true, CapabilityExternalMCPMutate: true,
	}}
	req := Request{
		Capability: CapabilityExternalMCPMutate,
		Scope:      Scope{Class: ScopeExternal, Path: "mcp:alpha"},
		Risk:       RiskHigh,
		Mutates:    true,
	}
	d := (Policy{}).Evaluate(req, ask, nil)
	if d.Outcome != OutcomeDeny {
		t.Fatalf("outcome = %s, want DENY", d.Outcome)
	}
	if d.Rule != RuleMutationNotPermitted {
		t.Fatalf("rule = %s, want %s", d.Rule, RuleMutationNotPermitted)
	}
}
