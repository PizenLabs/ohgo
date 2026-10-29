# Model catalog

`catalog.json` is generated from the upstream TypeScript source, not
hand-written. Hand-editing it is how the old structs drifted (sonnet-4-5 at
200k context instead of 1M, opus-4-1 at $15/$75 instead of $5/$25).

## Source

Upstream: `packages/catalog/src/models.json`
(itself assembled from provider listings). The Go file keeps a subset with
real facts: id, provider, API, route, base URL, context window, max output,
reasoning flag, input modalities, per-million-token prices, thinking ladder.

Current content: **379 models across 13 providers** (see `ohgo models`).

Regen: `python3 scripts/extract-upstream.py [upstream-models.json]
[catalog.json]`. It converts only rows this build can speak
(openai-completions → chat route, anthropic-messages native, ollama-chat
native, openrouter native ids with the `~` stripped), drops
Responses-only and google-generative-ai rows with counts (never mis-routed),
limits OpenRouter seeds to native ids already shipped elsewhere, and leaves
existing entries byte-identical (no `sort_keys`: only the new provider maps
are added).

## Dynamic discovery

Bundled rows answer offline and fast; ids the bundle never saw resolve over
the network (`catalog.DiscoverModels`): OpenRouter and the OpenCode gateways
via OpenAI-compatible `/models` (the official `GET {baseURL}/models`
documented response: `id`, `name`, `context_length`,
`top_provider.max_completion_tokens`, per-token `pricing`, `architecture`
modalities, `supported_parameters`, reasoning ladders), Ollama via
`/api/tags` plus `/api/show` caps. Rosters cache to
`~/.ohgo/cache/<provider>-models.json` for 24h, with a stale fallback when
the network is down. Public metadata browses unauthenticated (no key is
sent when none resolves); inference still authenticates. Discovered rows
face the same validation as bundled ones; rows without a context window are
skipped, tool-less OpenRouter rows are skipped (the harness always offers
tools), and minimal gateway ids merge with bundled facts or not at all.
Pricing presence is recorded (`priced`): a zero price with metadata is
verified free, a missing pricing object is unknown — never rendered as
free. Local Ollama ships no bundled rows at all: its models exist only on
the user's machine, so discovery is the only honest source.

## Picker snapshot and refresh

`catalog.SnapshotAll` merges bundled plus cached rows without network:
live-cached values win per (provider, id), bundled fills the rest, new live
ids appear as their own rows, and providers never merge by display name.
Each row carries origin (`live`/`cached`/`bundled`) and pricing flags for
the picker's free filter. `catalog.DiscoverModelsFresh` bypasses a fresh
cache for one provider and falls back to stale rows on error. The TUI
issues refreshes as Bubble Tea commands (never blocking the event loop),
pairs results by generation (older responses never overwrite newer ones),
and keeps cached rows with a reason when a refresh fails.

## Regen

1. Refresh the upstream checkout.
2. Run `python3 scripts/extract-upstream.py`; it prints kept/dropped counts
   per provider with reasons. Review the dropped lines: a new drop reason is
   a signal, not noise.
3. Keep only models this build can speak (the script enforces this):
   - `route: openai-chat-completions` for OpenAI chat models (upstream uses
     Responses; those models also accept Chat Completions).
   - Native `ollama-chat` for Ollama local and Cloud.
   - Exclude Responses-only lines (codex, internal codenames, xai), Gemini
     rows, Bedrock rows, and anything without real windows and prices.
4. Validate: `go test ./internal/provider/catalog/` — `TestCatalogIsWellFormed` rejects entries
   with no id/provider, non-positive windows, output exceeding context, or no
   input modalities. `TestResolveReturnsRealFacts` pins the values that were
   wrong before the catalog existed.

## Rules the data enforces

- Unknown references are errors, never fallbacks (`catalog.ErrUnknownModel`).
- Base URLs in the catalog are defaults; `Registry.SetBaseURL` overrides them
  wholesale, `/v1` prefix included.
- Multi-slash ids (`groq/meta-llama/...`) resolve on the first separator.
