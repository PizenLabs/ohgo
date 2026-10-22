#!/usr/bin/env python3
"""Render a Phase 2 repository-discovery benchmark JSON file as Markdown.

Usage:
    python3 docs/benchmarks/phase2_summary.py docs/benchmarks/results/phase2-baseline.json

Only the standard library is used so the table can be regenerated from the
committed result file without installing anything.
"""

from __future__ import annotations

import json
import statistics
import sys
from collections import defaultdict


def median(values):
    return statistics.median(values) if values else 0


def mean(values):
    return statistics.mean(values) if values else 0


def fmt(value, digits=1):
    if isinstance(value, float):
        return f"{value:.{digits}f}"
    return str(value)


def aggregate(runs):
    return {
        "runs": len(runs),
        "ok": sum(1 for run in runs if run["success"]),
        "complete": sum(1 for run in runs if run.get("_target_complete")),
        "coordinate_ok": sum(1 for run in runs if run.get("_coordinate_ok")),
        "status_ok": sum(1 for run in runs if run.get("_status_ok")),
        "turns_med": median([run["turns"] for run in runs]),
        "turns_mean": mean([run["turns"] for run in runs]),
        "calls_med": median([run["tool_calls"] for run in runs]),
        "discovery_med": median([run["discovery_calls"] for run in runs]),
        "candidates_med": median([run["candidate_files"] for run in runs]),
        "irrelevant_med": median([run.get("irrelevant_candidates", 0) for run in runs]),
        "opened_med": median([run["files_opened"] for run in runs]),
        "lines_med": median([run["lines_inspected"] for run in runs]),
        "context_med": median([run["context_bytes"] for run in runs]) / 1024,
        "input_med": median([run["input_tokens"] for run in runs]),
        "output_med": median([run["output_tokens"] for run in runs]),
        "duration_med": median([run["duration_ms"] for run in runs]),
        "first_target_med": median([run.get("time_to_first_target_ms", 0) for run in runs]),
        "all_targets_med": median([run.get("time_to_all_targets_ms", 0) for run in runs]),
    }


def annotate(runs, tasks):
    by_id = {task["id"]: task for task in tasks}
    for run in runs:
        task = by_id[run["task_id"]]
        if task["expected_status"] == "not_found":
            run["_target_complete"] = run["extra_targets"] == 0 and run["status_reported"] == "not_found"
        else:
            run["_target_complete"] = (
                run["matched_targets"] == len(task["targets"]) and run["extra_targets"] == 0
            )
        run["_status_ok"] = run["status_reported"] == task["expected_status"]
        if task["expected_status"] == "not_found":
            run["_coordinate_ok"] = run["success"]
        else:
            run["_coordinate_ok"] = run["success"] and run["line_exact"] == len(task["targets"])


def main() -> int:
    if len(sys.argv) != 2:
        print(__doc__, file=sys.stderr)
        return 2
    with open(sys.argv[1], encoding="utf-8") as handle:
        data = json.load(handle)

    meta = data["metadata"]
    runs = data["runs"]
    tracks = meta["tracks"]
    annotate(runs, data["tasks"])

    print("# Phase 2 benchmark summary")
    print()
    print(f"- Repository revision: `{meta['repository_revision']}`")
    print(f"- Benchmark: `{meta['benchmark_revision']}` (`{meta['harness_version']}`)")
    print(f"- Task hash: `{meta['task_hash']}`")
    print(f"- Model: `{meta['provider']}/{meta['model']}`")
    print(f"- Fixture: {meta['fixture_files']} files ({meta['fixture_go_files']} Go)")
    print(f"- Temperature: {meta['temperature']}; max turns: {meta['max_turns']}; max tokens: {meta['max_tokens']}")
    print(f"- Runs: {len(runs)}")
    print()

    print("## Aggregate by track")
    print()
    print("| Track | Runs | Correct | Complete targets | Coordinate exact | Status preserved | Turns (med) | Discovery calls (med) | Candidate files (med) | Irrelevant candidates (med) | Files opened (med) | Lines inspected (med) | Context KB (med) | Input tok (med) | Output tok (med) | Duration ms (med) |")
    print("| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |")
    for track in tracks:
        subset = [run for run in runs if run["track"] == track]
        agg = aggregate(subset)
        rate = 100 * agg["ok"] / agg["runs"] if agg["runs"] else 0
        print(
            f"| {track} | {agg['runs']} | {agg['ok']} ({rate:.1f}%) | {agg['complete']} | {agg['coordinate_ok']} | {agg['status_ok']} | "
            f"{fmt(agg['turns_med'])} | {fmt(agg['discovery_med'])} | {fmt(agg['candidates_med'])} | "
            f"{fmt(agg['irrelevant_med'])} | {fmt(agg['opened_med'])} | {fmt(agg['lines_med'])} | "
            f"{fmt(agg['context_med'])} | {fmt(agg['input_med'])} | {fmt(agg['output_med'])} | "
            f"{fmt(agg['duration_med'], 0)} |"
        )
    print()

    print("## Correctness by category")
    print()
    categories = sorted({run["category"] for run in runs})
    print("| Category | " + " | ".join(tracks) + " |")
    print("| --- | " + " | ".join("---:" for _ in tracks) + " |")
    for category in categories:
        cells = []
        for track in tracks:
            subset = [run for run in runs if run["category"] == category and run["track"] == track]
            ok = sum(1 for run in subset if run["success"])
            complete = sum(1 for run in subset if run.get("_target_complete"))
            cells.append(f"{ok}/{len(subset)} correct; {complete}/{len(subset)} complete")
        print(f"| {category} | " + " | ".join(cells) + " |")
    print()

    print("## Per-run measurements")
    print()
    print("| Task | Track | Outcome | Status | Turns | Calls | Cand | Opened | Lines | Context KB | In tok | Out tok | ms |")
    print("| --- | --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |")
    for run in runs:
        print(
            f"| {run['task_id']} | {run['track']} | {run['outcome']} | {run['status_reported'] or '-'} | "
            f"{run['turns']} | {run['tool_calls']} | {run['candidate_files']} | {run['files_opened']} | "
            f"{run['lines_inspected']} | {run['context_bytes']/1024:.1f} | {run['input_tokens']} | "
            f"{run['output_tokens']} | {run['duration_ms']} |"
        )
    print()

    failures = [run for run in runs if not run["success"]]
    print(f"## Failures ({len(failures)})")
    print()
    if not failures:
        print("None.")
    else:
        print("| Task | Track | Outcome | Status | Error |")
        print("| --- | --- | --- | --- | --- |")
        for run in failures:
            error = (run.get("error") or "").replace("|", "\\|")
            print(f"| {run['task_id']} | {run['track']} | {run['outcome']} | {run['status_reported'] or '-'} | {error} |")
    print()

    print("## Outcome distribution")
    print()
    outcomes = defaultdict(int)
    for run in runs:
        outcomes[run["outcome"]] += 1
    for outcome, count in sorted(outcomes.items()):
        print(f"- `{outcome}`: {count}")
    print()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
