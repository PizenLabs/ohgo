package memory

import (
	"errors"
	"testing"
	"time"
)

func fixedNow() time.Time {
	return time.Date(2026, 10, 12, 15, 4, 5, 0, time.FixedZone("offset", -7*3600))
}

func TestInferredRequiresConfirmation(t *testing.T) {
	// The trust boundary: a model hypothesis must not become durable project
	// knowledge merely because a caller proposed it. Without this, every
	// plausible-sounding model claim would silently become authoritative.
	_, err := Promote(Candidate{
		Statement:  "the auth layer probably uses JWT",
		Provenance: Inferred,
		Source:     Source{Kind: FromModel, Ref: "session-42"},
	}, fixedNow())
	if !errors.Is(err, ErrUnconfirmed) {
		t.Fatalf("Promote of an unconfirmed inference = %v, want ErrUnconfirmed", err)
	}
}

func TestConfirmedInferenceIsPromotedAndKeepsProvenance(t *testing.T) {
	// A human may accept a hypothesis, but the trail must not be rewritten:
	// the fact stays INFERRED *and* confirmed, so a reader can tell that a
	// person vouched for a model's claim rather than observing it directly.
	fact, err := Promote(Candidate{
		Statement:  "the auth layer uses JWT",
		Provenance: Inferred,
		Source:     Source{Kind: FromModel, Ref: "session-42"},
		Confirmed:  true,
	}, fixedNow())
	if err != nil {
		t.Fatalf("Promote of a confirmed inference: %v", err)
	}
	if fact.Provenance != Inferred {
		t.Errorf("provenance = %q, want %q (origin must survive confirmation)", fact.Provenance, Inferred)
	}
	if !fact.Confirmed {
		t.Error("Confirmed = false for a human-confirmed fact")
	}
	if !fact.Valid {
		t.Error("Valid = false for a freshly promoted fact")
	}
}

func TestObservedIsPromotedWithoutConfirmation(t *testing.T) {
	// A deterministic observation is authoritative on its own; gating it on a
	// human would make the runtime unable to record what a tool plainly
	// reported. It must stay distinguishable from a human-confirmed fact.
	fact, err := Promote(Candidate{
		Statement:  "internal/generated holds generated Go files",
		Provenance: Observed,
		Source:     Source{Kind: FromTool, Ref: "glob"},
	}, fixedNow())
	if err != nil {
		t.Fatalf("Promote of an observation: %v", err)
	}
	if fact.Provenance != Observed {
		t.Errorf("provenance = %q, want %q", fact.Provenance, Observed)
	}
	if fact.Confirmed {
		t.Error("an observation was reported as human-confirmed")
	}
}

func TestConfirmedProvenanceImpliesConfirmation(t *testing.T) {
	// A statement whose origin is a human IS human-confirmed; leaving the flag
	// false would let a reader mistake a direct instruction for an unapproved
	// claim.
	fact, err := Promote(Candidate{
		Statement:  "do not edit generated files",
		Provenance: Confirmed,
		Source:     Source{Kind: FromHuman},
	}, fixedNow())
	if err != nil {
		t.Fatalf("Promote of a confirmed statement: %v", err)
	}
	if !fact.Confirmed {
		t.Error("Confirmed = false for a Confirmed-provenance statement")
	}
}

func TestPromoteRecordsEstablishmentTimeInUTC(t *testing.T) {
	// The clock is injected; the fact must carry the caller's instant as a
	// comparable time, not a local-zone rendering that orders incorrectly.
	fact, err := Promote(Candidate{
		Statement:  "migrations live under db/migrations",
		Provenance: Observed,
		Source:     Source{Kind: FromFile, Ref: "AGENTS.md"},
	}, fixedNow())
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if !fact.Established.Equal(fixedNow()) {
		t.Errorf("Established = %v, want %v", fact.Established, fixedNow())
	}
	if _, offset := fact.Established.Zone(); offset != 0 {
		t.Errorf("Established zone offset = %d, want 0 (UTC)", offset)
	}
}

func TestPromoteRejectsMalformedCandidates(t *testing.T) {
	// Every rejection is a contract: a candidate missing a claim, a tier, or a
	// source cannot be evaluated for trust, so it must be refused at the seam
	// rather than stored as an unlabelled fact.
	cases := []struct {
		name string
		c    Candidate
	}{
		{"empty statement", Candidate{Provenance: Observed, Source: Source{Kind: FromTool}}},
		{"blank statement", Candidate{Statement: "  ", Provenance: Observed, Source: Source{Kind: FromTool}}},
		{"unknown provenance", Candidate{Statement: "x", Provenance: "vibes", Source: Source{Kind: FromTool}}},
		{"empty provenance", Candidate{Statement: "x", Source: Source{Kind: FromTool}}},
		{"unknown source", Candidate{Statement: "x", Provenance: Observed, Source: Source{Kind: "tarot"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Promote(tc.c, fixedNow()); !errors.Is(err, ErrInvalidCandidate) {
				t.Fatalf("Promote = %v, want ErrInvalidCandidate", err)
			}
		})
	}
}
