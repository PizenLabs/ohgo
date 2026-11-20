# Edge freshness

Date: 2026-11-20
Status: **closed for representation; edge verification remains a capability limit**

## 1. The three levels

A structural graph makes three different currency claims. They cannot be
collapsed into one freshness label.

| Level | Question | Established by |
|---|---|---|
| Node | Does the declaration still exist at the reported coordinate? | Current source read at the file/line range |
| Edge | Does the relationship still exist? | A current structural observation of the call/import/dependency, or a source read that binds both endpoints |
| Index | Does the provider's snapshot match the workspace? | Provider generation plus a reconciled source fingerprint |

A `CURRENT` node is not a `CURRENT` edge. The source can still contain `Place`
and `NewOrder` after the call between them was deleted.

## 2. The defect

GRY `trace` and `search` payloads do not carry a generation or an edge
freshness field. The previous adapter verified endpoints and then applied one
observation-level freshness to every record. When all endpoints were current,
the edge records were labelled `CURRENT` even though no call site had been
checked.

## 3. The representation

`evidence.Freshness` now has `UNVERIFIED`:

| State | Meaning |
|---|---|
| `CURRENT` | The coordinate or fact was verified against current source, or the provider supplied a current generation and the source did not contradict it. |
| `STALE` | The source moved, disappeared, no longer contains the symbol, or the provider said stale. |
| `UNAVAILABLE` | The source needed to establish currency could not be read. |
| `UNVERIFIED` | The endpoints are current, but the relationship itself was not checked against current source or a current structural snapshot. |
| `UNKNOWN` | No freshness information and no endpoint could be verified. |

The GRY builder keeps two freshness values:

- `freshness` for nodes, files, packages, and tests;
- `edgeFreshness` for trace and context relationships.

`recordEdge` and `edgeFact` use `edgeFreshness`. A current endpoint set maps to
`UNVERIFIED` edges, not `CURRENT`. A stale or unavailable observation keeps the
weaker state for both.

Every edge fact carries its own freshness into the investigation output:

```text
- [structural/gry UNVERIFIED] order.NewOrder — CALLS outbound order/service.go:2 hop 1 via order.Place
```

Each trace or context observation also states the boundary once:

```text
issues:
- gry trace edges are UNVERIFIED: the named endpoints still exist in current
  source, but each relationship was not re-checked; treat the edge as an index
  candidate, not a current fact
```

## 4. Why not force edge verification now

Verifying every edge would turn a bounded structural query into a source scan.
The adapter's contract is a candidate set with explicit provenance, not a proof
of the whole graph. The closure criterion was narrower and is met: an
unverified edge is never presented as a current fact.

## 5. Evidence

Tests:

- `TestSourceVerificationCurrentWhenSymbolStillExists`
- `TestSourceVerificationMissingFileIsStale`
- `TestSourceVerificationMovedSymbolIsStale`
- `TestSourceVerificationOutsideRootIsUnavailable`
- `TestSourceVerificationSymlinkOutsideRootIsUnavailable`
- `TestSourceVerificationWithoutRootIsUnknown`
- `TestTraceWithoutGenerationVerifiesEndpointsButNotEdges`
- `TestContextKeepsNodeAndEdgeFreshnessSeparate`
- `TestTraceStaleRowDowngradesObservation`
- `TestStaleContextIsMarkedAndBounded`
- `TestRouterNeverPresentsStaleExternalEvidenceAsCurrent`

Run:

```sh
go test ./internal/intelligence/gry ./internal/intelligence/investigate
```

## 6. Remaining limit

EDGE verification is not implemented for every relationship. A future provider
that supplies a per-edge generation or a current structural snapshot can
upgrade an edge to `CURRENT`; until then, `UNVERIFIED` is the honest ceiling.
