#!/usr/bin/env python3
"""Extract provider records from the upstream models.json into catalog.json.

Usage:
    python3 scripts/extract-upstream.py [upstream-models.json] [catalog.json]

Rules (see catalog/models.md):
- Only rows this build can speak: openai-completions (routed to
  openai-chat-completions), anthropic-messages (native), ollama-chat
  (native). Responses-only and google-generative-ai rows are excluded, never
  mis-routed.
- OpenRouter seeds are stripped of the leading `~` and limited to native ids
  already shipped under another provider: seeds repeat facts we carry, never
  new claims.
- Thinking ladders keep mode+efforts only for modes the port understands;
  anything else keeps the reasoning flag with no ladder.
- Rows violating the catalog invariants (no window, output above context, no
  input modalities) are dropped and reported.
"""

import json
import sys

UPSTREAM = sys.argv[1] if len(sys.argv) > 1 else (
    "/home/zen/Documents/ohgo/packages/catalog/src/models.json"
)
CATALOG = sys.argv[2] if len(sys.argv) > 2 else "catalog/catalog.json"

KNOWN_THINKING_MODES = {"budget", "effort", "anthropic-adaptive"}

PROVIDERS = {
    "openrouter": {
        "name": "OpenRouter",
        "baseUrl": "https://openrouter.ai/api/v1",
    },
    "opencode-zen": {
        "name": "OpenCode Zen",
        "baseUrl": "https://opencode.ai/zen/v1",
    },
    "opencode-go": {
        "name": "OpenCode Go",
        "baseUrl": "https://opencode.ai/zen/go/v1",
    },
    "ollama": {
        "name": "Ollama (local)",
        "baseUrl": "http://127.0.0.1:11434/v1",
    },
    "ollama-cloud": {
        "name": "Ollama Cloud",
        "baseUrl": "https://ollama.com",
    },
}

ROUTE = {
    "openai-completions": "openai-chat-completions",
    "openrouter": "openai-chat-completions",
}


def native_id(raw):
    return raw[1:] if raw.startswith("~") else raw


def convert(provider, raw_id, row):
    api = row.get("api")
    if api not in ("openai-completions", "anthropic-messages", "ollama-chat", "openrouter"):
        return None, f"unsupported api {api}"
    ctx = row.get("contextWindow") or 0
    mx = row.get("maxTokens") or 0
    if ctx <= 0 or mx <= 0:
        return None, "no window"
    if mx > ctx:
        return None, "max above context"
    modalities = row.get("input") or []
    if not modalities:
        return None, "no input modalities"
    cost = row.get("cost") or {}
    record = {
        "id": native_id(raw_id),
        "name": row.get("name") or native_id(raw_id),
        "api": api,
        "provider": provider,
        "baseUrl": row.get("baseUrl") or PROVIDERS[provider]["baseUrl"],
        "contextWindow": ctx,
        "maxTokens": mx,
        "reasoning": bool(row.get("reasoning")),
        "input": modalities,
        "cost": {
            "input": cost.get("input") or 0,
            "output": cost.get("output") or 0,
            "cacheRead": cost.get("cacheRead") or 0,
            "cacheWrite": cost.get("cacheWrite") or 0,
        },
    }
    if api in ROUTE:
        record["route"] = ROUTE[api]
    thinking = row.get("thinking") or {}
    if thinking.get("mode") in KNOWN_THINKING_MODES and thinking.get("efforts"):
        record["thinking"] = {
            "mode": thinking["mode"],
            "efforts": thinking["efforts"],
        }
    return record, None


def main():
    upstream = json.load(open(UPSTREAM))
    catalog = json.load(open(CATALOG))

    port_native = set()
    for models in catalog["models"].values():
        port_native.update(models.keys())

    stats = {}
    for provider in PROVIDERS:
        rows = upstream.get(provider, {})
        kept, dropped = {}, {}
        for raw_id, row in rows.items():
            if not isinstance(row, dict):
                dropped[raw_id] = "not a record"
                continue
            nid = native_id(raw_id)
            if provider == "openrouter" and nid not in port_native:
                dropped[raw_id] = "unshipped id (dynamic discovery covers it)"
                continue
            if nid in kept:
                dropped[raw_id] = "duplicate native id"
                continue
            record, reason = convert(provider, raw_id, row)
            if record is None:
                dropped[raw_id] = reason
                continue
            kept[nid] = record
        # Sorted ids keep the diff reviewable; the rest of the file is left
        # byte-identical by dumping without sort_keys. A provider with no
        # static rows (local Ollama: its models only exist on the user's
        # machine) keeps its metadata but no models map: the loader rejects
        # empty maps, and discovery is the only honest source there.
        if kept:
            catalog["models"][provider] = {k: kept[k] for k in sorted(kept)}
        else:
            catalog["models"].pop(provider, None)
        catalog["providers"][provider] = PROVIDERS[provider]
        stats[provider] = (len(kept), dropped)

    json.dump(catalog, open(CATALOG, "w"), indent=1)
    for provider, (kept, dropped) in stats.items():
        print(f"{provider}: kept {kept}, dropped {len(dropped)}")
        by_reason = {}
        for reason in dropped.values():
            by_reason[reason] = by_reason.get(reason, 0) + 1
        for reason, count in sorted(by_reason.items()):
            print(f"    {count:4d}  {reason}")


if __name__ == "__main__":
    main()
