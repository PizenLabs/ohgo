#!/usr/bin/env python3
"""Merge every Phase 2 agent-level run into one auditable final dataset.

Policy, applied in order:

1. Start from the 53 valid pre-anchored-ignore-fix cells (`phase2-valid-runs.json`).
2. A cell listed in AFFECTED (lexical or hybrid cells whose recorded lexical
   calls change output under the `.gitignore` fix, see `phase2-fix-impact.json`)
   is replaced ONLY by a valid post-fix rerun. If the rerun is blocked, the
   pre-fix cell is removed and the cell is reported as unresolved. Datasets are
   never mixed inside one cell.
3. A cell with no valid run is filled by the first valid rerun that produces it.
   Provider errors (HTTP 429/402) are never admitted.
4. Every source must share the task hash and repository revision, or the merge
   stops.

Usage:
    python3 docs/benchmarks/phase2_final_merge.py docs/benchmarks/results
"""

from __future__ import annotations

import json
import sys
from pathlib import Path

BASE = "phase2-valid-runs.json"
RERUNS = [
    "phase2-rerun-A2B.json",
    "phase2-rerun-blocked.json",
    "phase2-rerun-pending-1.json",
    "phase2-rerun-pending-2.json",
    "phase2-rerun-pending-3.json",
    "phase2-rerun-fixaffected-A.json",
    "phase2-rerun-fixaffected-C.json",
]
AFFECTED = {
    ("A1-exact-new-symbols-tool", "A-lexical"),
    ("A2-exact-index-lookup", "A-lexical"),
    ("B1-ambiguous-classify", "A-lexical"),
    ("D2-mod-tool-registration", "A-lexical"),
    ("D2-mod-tool-registration", "C-hybrid"),
    ("D3-mod-grep-single-file-cap", "A-lexical"),
    ("D4-mod-prompt-identity", "A-lexical"),
    ("E1-cross-grep-def-and-registration", "A-lexical"),
    ("E1-cross-grep-def-and-registration", "C-hybrid"),
}
PLANNED_TRACKS = ("A-lexical", "B-structural", "C-hybrid")


def load(results: Path, name: str) -> dict:
    return json.loads((results / name).read_text(encoding="utf-8"))


def main() -> int:
    if len(sys.argv) != 2:
        print(__doc__, file=sys.stderr)
        return 2
    results = Path(sys.argv[1])
    base = load(results, BASE)
    reference = base["metadata"]
    tasks = base["tasks"]
    task_ids = [task["id"] for task in tasks]

    merged: dict[tuple[str, str], dict] = {}
    for run in base["runs"]:
        merged[(run["task_id"], run["track"])] = dict(run, _source=BASE, _binary="pre-fix")

    sources = [BASE]
    unresolved: list[tuple[str, str]] = []
    for name in RERUNS:
        data = load(results, name)
        meta = data["metadata"]
        for field in ("task_hash", "repository_revision", "model", "provider", "temperature"):
            if meta.get(field) != reference.get(field):
                print(f"{name}: {field} differs from the baseline", file=sys.stderr)
                return 1
        sources.append(name)
        for run in data["runs"]:
            key = (run["task_id"], run["track"])
            valid = not run.get("error")
            if key in AFFECTED:
                if valid:
                    merged[key] = dict(run, _source=name, _binary="post-fix")
                elif key in merged and merged[key]["_binary"] == "pre-fix":
                    del merged[key]
                    unresolved.append(key)
                continue
            if valid and key not in merged:
                merged[key] = dict(run, _source=name, _binary="post-fix")

    runs = sorted(merged.values(), key=lambda r: (r["task_id"], r["track"]))
    for key in sorted(AFFECTED):
        if key not in merged and key not in unresolved:
            unresolved.append(key)

    have = {(r["task_id"], r["track"]) for r in runs}
    missing = [
        (task_id, track)
        for task_id in task_ids
        for track in PLANNED_TRACKS
        if (task_id, track) not in have
    ]
    pre_fix = sum(1 for r in runs if r["_binary"] == "pre-fix")

    output = {
        "metadata": {
            "description": "Final Phase 2 valid agent-level cells after reruns and the anchored .gitignore fix",
            "benchmark_revision": "phase2-final",
            "harness_version": reference["harness_version"],
            "sources": sources,
            "model": reference["model"],
            "provider": reference["provider"],
            "api": reference["api"],
            "base_url": reference["base_url"],
            "repository_revision": reference["repository_revision"],
            "task_hash": reference["task_hash"],
            "fixture_files": reference["fixture_files"],
            "fixture_go_files": reference["fixture_go_files"],
            "max_turns": reference["max_turns"],
            "max_tokens": reference["max_tokens"],
            "temperature": reference["temperature"],
            "timeout_seconds": reference["timeout_seconds"],
            "tracks": reference["tracks"],
            "excluded_paths": reference["excluded_paths"],
            "pre_fix_cells": pre_fix,
            "post_fix_cells": len(runs) - pre_fix,
            "unresolved_fix_affected_cells": [f"{t}|{k}" for t, k in unresolved],
            "permanently_missing_cells": [f"{t}|{k}" for t, k in missing],
        },
        "tasks": tasks,
        "runs": runs,
    }
    out = results / "phase2-final-valid-runs.json"
    out.write_text(json.dumps(output, indent=1, ensure_ascii=False) + "\n", encoding="utf-8")
    print(f"valid cells: {len(runs)} (pre-fix {pre_fix}, post-fix {len(runs) - pre_fix})")
    print(f"unresolved fix-affected: {len(unresolved)}; still missing: {len(missing)}")
    print(f"wrote {out}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
