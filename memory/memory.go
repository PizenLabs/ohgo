// Package memory defines OHGO's project-memory trust boundary.
//
// It is a seam, not a store. OHGO keeps no project-memory database: the only
// durable project knowledge it has today is the human-authored AGENTS.md, read
// per turn by prompt.ProjectContext. Knowledge that a *session* produces is
// session memory, not project memory, and this package does not invent an
// automatic migration between the two. It exists so that if session knowledge
// is ever promoted into a project store, exactly one place decides whether that
// promotion is allowed and what provenance the record carries.
//
// The boundary is provenance (ARCHITECTURE_CONTRACT_V1 §32: model output is
// input, never authority). A statement is OBSERVED (reproducible from project,
// source, or tool state), CONFIRMED (a human established it), or INFERRED (a
// model's hypothesis). Promote refuses to turn an INFERRED statement into
// durable project knowledge unless a human explicitly confirms it, so an LLM
// hypothesis can never become authoritative project memory by being written
// down.
//
// The physical store, when one is justified, belongs at <project>/.ohgo and is
// not implemented: no consumer exists yet, and a database with no reader is a
// speculative structure. This file establishes the seam and defers storage.
package memory

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Provenance is how OHGO came to believe a statement.
//
// It records origin, not current status: a statement inferred by a model and
// later approved by a human stays INFERRED with Confirmed set, so the trust
// trail survives the promotion instead of being overwritten by it.
type Provenance string

// The three provenance tiers.
const (
	// Observed is a deterministic fact derived from project, source, or tool
	// state. It is authoritative without human approval because it is
	// reproducible; a wrong observation is a bug in the observer.
	Observed Provenance = "observed"
	// Confirmed is a statement a human explicitly established. It is
	// authoritative by construction.
	Confirmed Provenance = "confirmed"
	// Inferred is a model-derived hypothesis or interpretation. It is never
	// authoritative on its own.
	Inferred Provenance = "inferred"
)

// Valid reports whether p is one of the three tiers.
func (p Provenance) Valid() bool {
	switch p {
	case Observed, Confirmed, Inferred:
		return true
	}
	return false
}

// SourceKind names what a statement was derived from.
//
// It is separate from provenance so a record can answer both "why does OHGO
// believe this" and "where did it see it": a model can reason about a file
// (Inferred / file) or a tool can report it (Observed / tool).
type SourceKind string

// Source kinds.
const (
	// FromTool is a deterministic result reported by a tool.
	FromTool SourceKind = "tool"
	// FromFile is content read from a project file.
	FromFile SourceKind = "file"
	// FromHuman is a statement a person made.
	FromHuman SourceKind = "human"
	// FromModel is a statement a language model produced.
	FromModel SourceKind = "model"
)

// Valid reports whether k is a known source kind.
func (k SourceKind) Valid() bool {
	switch k {
	case FromTool, FromFile, FromHuman, FromModel:
		return true
	}
	return false
}

// Source records where a statement originated.
type Source struct {
	// Kind is what produced the statement.
	Kind SourceKind
	// Ref names the specific origin, e.g. a path, tool name, or session id.
	// Empty is allowed: the kind alone may be all that is known.
	Ref string
}

// Candidate is a statement a session offers for durable project memory.
//
// It is the input side of the seam: a caller (a session, a tool, or a human)
// proposes knowledge, and Promote decides whether it may become durable.
type Candidate struct {
	// Statement is the fact being proposed.
	Statement string
	// Provenance is how the statement originated.
	Provenance Provenance
	// Source is where it came from.
	Source Source
	// Confirmed marks explicit human confirmation. It is required to promote
	// an Inferred candidate and is what lets a reader tell a hypothesis that
	// was accepted from one that merely exists.
	Confirmed bool
}

// Fact is one durable project-memory record.
//
// It carries enough to answer the questions a reader must be able to ask of
// durable knowledge: what is claimed, why OHGO believes it, where it came
// from, when it was established, and whether a human confirmed it.
type Fact struct {
	// Statement is the fact.
	Statement string
	// Provenance is how it originated (Observed, Confirmed, or Inferred).
	Provenance Provenance
	// Source is where it came from.
	Source Source
	// Confirmed reports whether a human explicitly approved it.
	Confirmed bool
	// Established is when it became durable.
	Established time.Time
	// Valid reports whether the fact is still believed. A store may clear it
	// without deleting the record, so an invalidated fact keeps its trail.
	Valid bool
}

// Errors, matched with errors.Is.
var (
	// ErrUnconfirmed means an inferred statement was offered for durable
	// memory without the human confirmation the trust boundary requires.
	ErrUnconfirmed = errors.New("memory: inferred statement requires explicit confirmation")
	// ErrInvalidCandidate means a candidate cannot become a fact.
	ErrInvalidCandidate = errors.New("memory: invalid candidate")
)

// Promote turns a session candidate into a durable project fact, enforcing the
// trust boundary.
//
// The rule is the whole point of the package: OBSERVED and CONFIRMED
// statements may be promoted directly, because they are already authoritative,
// but an INFERRED statement is refused unless a human confirmed it. Promotion
// preserves provenance rather than rewriting it, so a confirmed hypothesis
// remains labelled as a hypothesis that a human accepted.
//
// now is a parameter rather than a call to time.Now so the establishment time
// is deterministic in tests and owned by the caller's clock, matching the rest
// of the runtime.
func Promote(c Candidate, now time.Time) (Fact, error) {
	if strings.TrimSpace(c.Statement) == "" {
		return Fact{}, fmt.Errorf("%w: empty statement", ErrInvalidCandidate)
	}
	if !c.Provenance.Valid() {
		return Fact{}, fmt.Errorf("%w: unknown provenance %q", ErrInvalidCandidate, c.Provenance)
	}
	if !c.Source.Kind.Valid() {
		return Fact{}, fmt.Errorf("%w: unknown source %q", ErrInvalidCandidate, c.Source.Kind)
	}
	if c.Provenance == Inferred && !c.Confirmed {
		return Fact{}, fmt.Errorf("%w: %q", ErrUnconfirmed, c.Statement)
	}

	return Fact{
		Statement:   strings.TrimSpace(c.Statement),
		Provenance:  c.Provenance,
		Source:      c.Source,
		Confirmed:   c.Confirmed || c.Provenance == Confirmed,
		Established: now.UTC(),
		Valid:       true,
	}, nil
}
