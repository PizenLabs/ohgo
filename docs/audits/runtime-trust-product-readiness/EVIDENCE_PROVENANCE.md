# Evidence provenance and freshness

## 1. Native evidence

The native Structure Core remains authoritative for its supported surface.

- Go uses the native `go/ast` backend.
- Multi-language builds add pinned Tree-sitter syntax adapters. They are
  syntax-level: declarations, imports, references, calls, JSX references, type
  references, and explicit inheritance/implements clauses. They do not claim
  type resolution, overload resolution, or dispatch.
- A snapshot carries `generation`, `snapshot_id`, `source_fingerprint`,
  `scope_fingerprint`, `graph_state`, and independent `scope_complete`,
  `inventory_complete`, and `structural_complete` fields.
- A `CURRENT` snapshot means the snapshot matches the stat at the last scan. It
  does not mean every semantic relationship is complete.
- Unsupported, excluded, unparsed, failed, ambiguous, and truncated states are
  explicit. `.gohtml`, `.tmpl`, and `.sql` remain unsupported source in the
  ecommerce repository and are reported as such.

## 2. GRY evidence

GRY is an optional external structural provider. It is never required for
correctness and never authorizes an action.

- Project identity is resolved by canonical root path, not by directory name.
  Zero matches is `NOT_INDEXED`; multiple matches is `AMBIGUOUS`; neither is
  evidence that the target is absent.
- The router is native-first. A sufficient native observation skips the
  external call. An external call is spent only for a specific native gap.
- Native and external facts retain per-fact source attribution. A merge is
  additive, not a claim that either provider alone completed the step.
- Stale external evidence downgrades the combined observation; it is never
  presented as current source truth.

### 2.1 Freshness states

| State | Meaning |
|---|---|
| `CURRENT` | The relevant coordinate was verified against current source, or the provider supplied a current generation and the source did not contradict it. |
| `STALE` | The source moved, disappeared, or no longer contains the symbol; the provider said stale; or a bounded result admits incompleteness. |
| `UNKNOWN` | No freshness information and no endpoint could be verified. |
| `UNAVAILABLE` | The source needed to establish freshness could not be read (missing root, outside-root path, unreadable or oversized file). |
| `UNVERIFIED` | The endpoint coordinates are current, but the asserted relationship itself was not checked against current source or a current structural snapshot. A current node is not proof of a current edge. |

`trace` and `search` payloads do not expose a generation today. The adapter
therefore verifies their file/line/symbol coordinates against the current
workspace:

- all checked endpoints still contain the symbol → `CURRENT`;
- a file is missing or the symbol is gone → `STALE`;
- the path is outside the root or unreadable → `UNAVAILABLE`;
- nothing could be checked → `UNKNOWN`.

This verifies endpoints, not every edge. The record keeps the provider's own
observation flag and snapshot identity, and the summary says what was verified.

Node and edge freshness are separate. Trace and context relationship records
use `UNVERIFIED` when only their endpoints are current:

- a node record (`declared_at`, `appears_in_context`) can be `CURRENT`;
- an edge record (`CALLS`, `IMPORTS`, `depends_on`, …) is `UNVERIFIED` unless a
  current structural observation or source read binds both endpoints;
- an edge fact carries the state in the investigation output
  (`[structural/gry UNVERIFIED]`), not only in a summary line;
- a stale or unavailable observation downgrades both nodes and edges.

This closes the representation defect: an unverified edge is never presented as
a current fact. It does not implement per-edge verification, which remains a
capability limit.

`context` payloads do carry generation/freshness; their nodes and tests are
also checked against current source. Their `Relationships` rows use the same
node/edge split: nodes can be `CURRENT`, relationships are `UNVERIFIED` unless
the provider supplies a current per-edge proof.

## 3. Source verification and completion

- The completion evaluator requires a source observation for code-identifier
  claims. A listing proves a path exists, not what it implements.
- A negative conclusion must be earned by coverage that can establish absence.
  A complete file inventory can prove a file absence; it cannot prove a concept
  absence.
- A zero test-file inventory is a file-level observation. It does not prove the
  project has no tests, and a missing test script is a manifest fact, not a
  test inventory.
- A truncated or excluded inventory is reported as a limitation, not silently
  treated as complete.

## 4. What was not changed

- GRY remains optional and is not a completion authority.
- The native Structure Core is not replaced by GRY.
- No generation metadata is invented when GRY does not expose it.
- No target repository was indexed into a durable store or modified.
