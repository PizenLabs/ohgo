# GRY evaluation

## Scope

GRY is optional and must never replace OHGO's native structure ownership. The
evaluation compares native evidence with the GRY adapter on the Go project and
records the blog's explicit `NOT_INDEXED` state. Every GRY row used for a claim
was checked against current source.

## Environment

- GRY 1.0.4.
- Ecommerce project indexed at `2026-11-18T08:46:27Z`, 424 files, 5,302 nodes,
  24,837 edges. The measured repository commit is
  `00173f5f...` from 2026-11-19, so the index predates the commit and was not
  treated as current.
- No GRY project exists for `andev0x-blog`.

## Native baseline before fixes

The nested-module defect left the native index with no module identity:

```text
NewOrder: found=true, references=0, callers=0
```

GRY's direct trace with `--include-tests` returned:

```text
NewOrder inbound depth 2: 15 rows, 13 at hop 1, 2 at hop 2
```

All 13 hop-1 rows match the 13 direct call sites in current source; the two
hop-2 rows (`TestAdminOrderStatusAndTracking_EndToEnd`, `Cancel`) are reached
through `seedOrder` and `AdminCancel`, both present in source. This is a real
marginal gain **in the pre-fix state**.

## Native baseline after fixes

After OHGO-STR-001 the native index reports:

```text
NewOrder: references=13, callers=13, callees=0, importers=7, tests=4
```

The native observation is `Sufficient` for the impact question. The router
runs native first and does **not** spend an external request:

```text
external_requests: 0
providers: structural/native (PARTIAL), semantic (SUCCESS)
```

The conclusion is not that GRY is useless. It is that the native fix removed
the particular gap that had made GRY necessary for this repository. Keeping
GRY on would add a second source without new correct relationships.

## Locate behaviour

Before the adapter fix, `gry search ecommerce NewOrder` ranked:

```text
NewOrderView, newOrderID, NewOrderSummary
```

The exact declaration was absent from the top three. The adapter now asks
`gry search --name-pattern '^NewOrder$'` first and receives exactly one
candidate at `backend/checkout/domain/order.go:99`.

## Freshness and provenance

- `trace` and `search` payloads used by the adapter do not carry a generation
  or `indexed_at`; the adapter therefore marks their records with unknown
  freshness. It does not present them as current.
- When an external observation is marked `STALE`, the router downgrades the
  combined outcome to `STALE` and adds an explicit issue. It no longer returns
  the stale answer alone.
- The ecommerce index predates the measured commit. The rows used here were
  source-confirmed at the exact file and line, not accepted because the GRY
  answer looked plausible.
- The blog has no index. The adapter returns `NOT_INDEXED`; the router keeps
  the native answer and does not qualify it as a failure.

## Marginal evidence gain

| State | Native correct relationships | GRY new correct relationships | Marginal gain | Added cost |
|---|---:|---:|---:|---|
| Before fixes, NewOrder impact | 0 | 13 direct + 2 indirect | 15 | 1 external command, one trace query |
| After fixes, NewOrder impact | 13 direct + 7 importers + 4 tests | 0 | 0 | 0 external requests (native sufficient) |

The additional GRY rows in the pre-fix state were correct and source-confirmed.
No GRY candidate was accepted without a source check. No blog relationship was
claimed from GRY because no project was indexed.

## Verdict

The current integration is justified only as an escalation for a native gap.
The audit fixed the router so it actually behaves that way: native first,
external only when insufficient, native evidence preserved when both run, and
stale external evidence never reported as current. On the tested repositories,
the fixed native Structure Core is sufficient for the exercised questions,
so GRY's measured marginal value after the fixes is zero.
