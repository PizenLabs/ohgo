#!/usr/bin/env python3
"""Compute the Phase 2 final metrics from the merged dataset.

Reports correctness (success, false positives, false negatives, wrong target,
not_found and ambiguity correctness), agent efficiency with medians and
quartiles, discovery precision, and a before/after comparison for every cell
affected by the anchored .gitignore fix.

Usage:
    python3 docs/benchmarks/phase2_final_analysis.py \
        docs/benchmarks/results/phase2-final-valid-runs.json \
        docs/benchmarks/results/phase2-valid-runs.json \
        docs/benchmarks/results/phase2-final-analysis.json
"""

from __future__ import annotations

import json
import statistics
import sys
from collections import Counter, defaultdict

TRACKS = ["A-lexical", "B-structural", "C-hybrid"]
EFFICIENCY = [
    "turns", "discovery_calls", "candidate_files", "irrelevant_candidates",
    "files_opened", "lines_inspected", "context_bytes", "input_tokens",
    "output_tokens", "duration_ms",
]


def quartiles(values):
    if not values:
        return {"n": 0}
    if len(values) < 2:
        v = float(values[0])
        return {"n": 1, "q1": v, "median": v, "q3": v, "min": v, "max": v}
    q1, med, q3 = statistics.quantiles(values, n=4, method="inclusive")
    return {"n": len(values), "q1": q1, "median": med, "q3": q3,
            "min": min(values), "max": max(values)}


def is_negative(task):
    return task["expected_status"] == "not_found"


def is_ambiguous(task):
    return task["expected_status"] == "ambiguous"


def track_metrics(runs, tasks):
    by_id = {t["id"]: t for t in tasks}
    out = {}
    for track in TRACKS:
        rs = [r for r in runs if r["track"] == track]
        n = len(rs)
        ok = sum(r["success"] for r in rs)
        fp = sum(1 for r in rs if r.get("extra_targets", 0) > 0 or (is_negative(by_id[r["task_id"]]) and r.get("targets_reported")))
        fn = sum(1 for r in rs if r["missing_targets"] > 0)
        wrong = sum(1 for r in rs if r["outcome"] == "wrong_target")
        no_answer = sum(1 for r in rs if r["outcome"] == "no_answer")
        neg = [r for r in rs if is_negative(by_id[r["task_id"]])]
        amb = [r for r in rs if is_ambiguous(by_id[r["task_id"]])]
        out[track] = {
            "runs": n,
            "correct": ok,
            "correct_rate": round(ok / n, 3) if n else 0,
            "false_positive_runs": fp,
            "false_negative_runs": fn,
            "wrong_target": wrong,
            "no_answer": no_answer,
            "not_found_correct": f"{sum(r['success'] for r in neg)}/{len(neg)}",
            "ambiguity_correct": f"{sum(r['success'] and r['status_reported'] == 'ambiguous' for r in amb)}/{len(amb)}",
            "exact_coordinates_on_correct": sum(r["line_exact"] > 0 for r in rs if r["success"]),
            "efficiency": {k: quartiles([r[k] for r in rs]) for k in EFFICIENCY},
            "precision": precision(rs),
        }
    return out


def precision(rs):
    total = sum(r["candidate_files"] for r in rs)
    irrelevant = sum(r["irrelevant_candidates"] for r in rs)
    return {
        "candidate_files_total": total,
        "irrelevant_candidates_total": irrelevant,
        "relevant_candidates_total": total - irrelevant,
        "ambiguous_status_runs": sum(r["status_reported"] == "ambiguous" for r in rs),
        "truncated_status_runs": sum(r["status_reported"] == "truncated" for r in rs),
    }


def paired_17(runs, tasks):
    # The original 17 tasks were the ones with all three tracks in the first
    # merge. Kept for direct comparison with the Phase 2 closure report.
    original = {
        "A1-exact-new-symbols-tool", "A3-exact-grep-cap",
        "B1-ambiguous-classify", "B2-ambiguous-agent-config", "B3-ambiguous-execute-methods",
        "C1-path-settings-config", "C2-path-project-load", "C3-path-workspace-prediction",
        "D1-mod-provider-env-table", "D2-mod-tool-registration", "D3-mod-grep-single-file-cap",
        "D4-mod-prompt-identity", "E1-cross-grep-def-and-registration", "E2-cross-lookup-def-and-caller",
        "F3-negative-grep-symbol", "H1-lexical-error-text", "H4-lexical-doc-line",
    }
    return track_metrics([r for r in runs if r["task_id"] in original], tasks)


def fix_comparison(final_runs, base_runs):
    affected = [
        ("A1-exact-new-symbols-tool", "A-lexical"), ("A2-exact-index-lookup", "A-lexical"),
        ("B1-ambiguous-classify", "A-lexical"), ("D2-mod-tool-registration", "A-lexical"),
        ("D2-mod-tool-registration", "C-hybrid"), ("D3-mod-grep-single-file-cap", "A-lexical"),
        ("D4-mod-prompt-identity", "A-lexical"), ("E1-cross-grep-def-and-registration", "A-lexical"),
        ("E1-cross-grep-def-and-registration", "C-hybrid"),
    ]
    before = {(r["task_id"], r["track"]): r for r in base_runs}
    after = {(r["task_id"], r["track"]): r for r in final_runs}
    rows = []
    for key in affected:
        b, a = before.get(key), after.get(key)
        rows.append({
            "task": key[0], "track": key[1],
            "pre_fix": b["outcome"] if b else None,
            "post_fix": a["outcome"] if a else None,
            "pre_fix_candidates": b["candidate_files"] if b else None,
            "post_fix_candidates": a["candidate_files"] if a else None,
            "outcome_changed": (b["outcome"] if b else None) != (a["outcome"] if a else None),
        })
    return rows


def main() -> int:
    if len(sys.argv) != 4:
        print(__doc__, file=sys.stderr)
        return 2
    final = json.load(open(sys.argv[1], encoding="utf-8"))
    base = json.load(open(sys.argv[2], encoding="utf-8"))
    tasks = final["tasks"]
    runs = final["runs"]
    result = {
        "dataset": {
            "valid_cells": len(runs),
            "planned_cells": len(tasks) * len(TRACKS),
            "pre_fix_cells": final["metadata"]["pre_fix_cells"],
            "post_fix_cells": final["metadata"]["post_fix_cells"],
            "outcome_distribution": dict(Counter(r["outcome"] for r in runs)),
        },
        "all_27_tasks": track_metrics(runs, tasks),
        "original_17_tasks": paired_17(runs, tasks),
        "fix_affected_cells": fix_comparison(runs, base["runs"]),
    }
    json.dump(result, open(sys.argv[3], "w", encoding="utf-8"), indent=1, ensure_ascii=False)
    print(f"wrote {sys.argv[3]}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
