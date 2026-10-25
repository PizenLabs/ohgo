# `/model` Stale-While-Revalidate Pass

**Date:** 2026-10-25
**Scope:** `/model` entry path, cached catalog snapshot, background refresh,
reconciliation, cancellation, and tests.
**Result:** PASS

## Conclusion

`/model` now renders from the local cached snapshot immediately and revalidates
the catalog in the background.

```text
/model
    -> local catalog snapshot
    -> render picker / control plane
    -> issue background refresh
         -> provider discovery
         -> model catalog refresh
         -> existing cache update
         -> reconcile only when rows changed
```

Provider discovery and remote catalog requests no longer determine
time-to-first-picker.

## The Actual Bottleneck

The remote refresh was already returned as a Bubble Tea command, so it was not
executing synchronously on the `/model` entry path. The expensive synchronous
work was **catalog snapshot materialization**.

`/model` routes to `openModelControl`, which called `catalog.SnapshotAll` three
times:

1. `modelSnapshot()` for the model roster,
2. `ProviderPaneRows()` for provider counts,
3. `RoleAssignments()` for role capability facts.

`SnapshotAll` then re-read and re-decoded each provider cache **once per merged
row** through `findCachedRecord`. With the local 782-row roster and five cache
files (~185 KB total), that produced thousands of redundant file reads and JSON
decodes per open.

Measured on the real local cache on this machine:

| Operation | Before |
|---|---:|
| `SnapshotAll` | 419–448 ms |
| `catalogSnapshot()` | 445–462 ms |
| `ProviderPaneRows()` | 446–477 ms |
| `RoleAssignments()` | 447–456 ms |
| **Synchronous `/model` entry total** | **~1.34–1.40 s** |

The same quadratic path scales with roster and cache size, which accounts for
the reported ~3 s on a slower or larger local cache.

The refresh was also conditional on staleness/loading/bundled origin, so a fresh
cache paid the full local rebuild cost and never revalidated while running.

## What Changed

### Cached snapshot is the boot path

`openModelPicker` and `openModelControl` build the UI from `CatalogSnapshot`
before any refresh command runs. The picker and control plane are visible and
usable before provider discovery starts.

### Refresh always runs in the background

Both open paths now call `issueRefresh()` after the surface is constructed. The
refresh remains the existing `CatalogHooks.Refresh` command path and still uses
`DiscoverModelsFresh`, so provider discovery and cache writes are not duplicated.

### One in-flight refresh across opens

`modelRefreshing` is program-scoped rather than picker-scoped. Closing and
reopening `/model` reuses a refresh already in flight, marks the new surface with
the existing subtle refreshing indicator, and does not spawn a second fetch.

### Generation guard and reconciliation

`modelSeq` still identifies the latest refresh. Stale responses are dropped.

On completion:

- the in-flight marker is cleared even if the picker was closed,
- the current-model marker is recomputed from `currentModel`,
- changed rows replace the displayed roster,
- unchanged rows do not trigger `SetItems`/`SetModels`,
- selection is preserved by value/id,
- origin and refresh errors update without discarding cached rows.

### Lifecycle cancellation

The program now owns a catalog context.

- `NewProgram` creates it.
- `closeDone` cancels it when the UI exits.
- `refreshModelCatalog` derives its 90-second timeout from the caller context.

A refresh therefore has clear ownership and does not outlive the program.

### Cache miss

When the snapshot is empty but a refresh hook exists, the picker opens in its
existing loading state and hydrates asynchronously. Dismissing it does not wait
for the refresh. No new picker layout or loading surface was introduced.

## Snapshot Read Optimization

`SnapshotAll` now reads each dynamic provider cache once and indexes records for
the merge, instead of re-reading the cache for every merged row.

Behavior preserved:

- bundled rows remain the fallback,
- fresh cached rows still override bundled rows,
- stale cached rows still appear with `Origin: "cached"`,
- providers remain distinct by `(provider, id)`,
- pricing and reasoning provenance flags are unchanged,
- the stale flag still reflects every dynamic provider's freshness.

## Synchronous vs Asynchronous

### Synchronous

- bundled catalog access (already loaded once),
- one read of each dynamic provider cache,
- merge and normalization into `ModelOption` rows,
- local provider/role/credential status reads,
- UI state construction and first paint.

### Asynchronous

- provider discovery,
- remote model catalog requests,
- cache writes through the existing discovery path,
- fresh snapshot construction,
- UI reconciliation after completion.

No network request occurs before the picker becomes visible.

## Concurrency Rules

- One refresh in flight per program.
- Repeated `/model` invocations reuse it.
- After completion, the next `/model` may start a new revalidation.
- Older responses cannot overwrite newer rows.
- Refresh changes rows only; it never calls `setModel` or `Assign`.
- A user selection applied during refresh remains the runtime selection.
- A changed roster preserves the highlighted row by value/id.
- Shutdown cancels the refresh context.
- A refresh hook that returns no command clears the in-flight state instead of
  blocking every later refresh.

## Measurements

Method: real local `~/.ohgo/cache`, 782 merged rows, five dynamic provider
caches, measured through `catalogSnapshot` + `ProviderPaneRows` +
`RoleAssignments`.

### Before

| Path segment | Time |
|---|---:|
| Snapshot | 445–462 ms |
| Providers | 446–477 ms |
| Roles | 447–456 ms |
| **Synchronous entry total** | **~1.34–1.40 s** |

### After

| Path segment | Time |
|---|---:|
| Snapshot | 3.1–5.2 ms |
| Providers | 2.9–4.6 ms |
| Roles | 2.8–3.2 ms |
| **Synchronous entry total** | **~8.8–13.0 ms** |

Reduction: approximately **100x** in the synchronous part of
time-to-first-picker.

Refresh duration is unchanged; it no longer determines when the picker appears.

## Tests Added

`tui/modelrefresh_test.go`:

1. `TestModelControlShowsCachedSnapshotBeforeBlockingRefresh`
   - cached rows render immediately,
   - navigation works while refresh is blocked,
   - refresh command is issued only after the pane opens.
2. `TestModelControlOpenKeepsTheRuntimeSelection`
   - opening `/model` does not switch the model or alter the active row.
3. `TestModelControlRefreshReplacesStaleRowsAndKeepsSelection`
   - changed catalog replaces stale rows,
   - the user's highlighted model survives.
4. `TestModelControlUnchangedRefreshLeavesRowsUntouched`
   - a provenance-only change does not rebuild the list or move the cursor.
5. `TestModelControlReusesAnInFlightRefresh`
   - reopening `/model` reuses the running refresh,
   - a new refresh is issued only after completion.
6. `TestModelPickerCacheMissOpensLoadingWithoutBlocking`
   - an empty snapshot still opens a usable picker,
   - hydration is asynchronous,
   - the loading picker can be dismissed while blocked.
7. `TestModelRefreshIsCancelledAtShutdown`
   - program shutdown cancels the refresh context.

Existing coverage still passes:

- `TestPickerRefreshGuard`
- `TestPickerStaleRefreshDropped`
- `TestPickerFailedRefreshKeepsRows`
- `TestPickerPartialRefreshAppliesLiveRows`
- `TestPickerSetItemsPreservesSelection`
- provider/model precedence tests
- model-selection and control-plane tests

## Verification

```text
gofmt -l .          clean
go build ./...      PASS
go vet ./...        PASS
go test ./...       PASS
go test -race ./... PASS
```

Focused run:

```text
go test ./tui -run 'TestModel(Control|Picker|Refresh)' -count=1
PASS
```

## Preserved Semantics

No change to:

- provider precedence: CLI > environment > configuration > default,
- model-selection semantics,
- role semantics,
- picker layout or key bindings,
- model/provider naming,
- the existing catalog cache,
- the existing refresh hook or discovery implementation,
- the control-plane assignment transaction,
- runtime authority boundaries.

## Behavioral Verification Note

This environment is non-interactive, so the manual TTY walkthrough was
substituted with deterministic blocking-refresh tests. They exercise the same
sequence:

1. open `/model` with a populated cache,
2. hold the refresh on a channel to simulate a slow provider,
3. confirm the picker is visible and navigable,
4. release the refresh,
5. confirm new rows appear,
6. confirm the user's selection and runtime model remain intact.

## Final Answer

**Yes.** Entering `/model` is now dominated by local snapshot loading and UI
construction. The refresh still takes as long as the providers require, but it
runs after the picker is visible and cannot delay first paint.
