#!/usr/bin/env python3
"""Merge valid agent-level cells from the baseline and the supplementary run.

The merge is deliberately conservative:

- a run with a provider error is never admitted;
- a cell present in both files keeps the baseline run;
- the output records its sources so a reader can audit the merge.

Usage:
    python3 docs/benchmarks/phase2_merged.py \
        docs/benchmarks/results/phase2-baseline.json \
        docs/benchmarks/results/phase2-supplementary-cohere.json \
        docs/benchmarks/results/phase2-valid-runs.json
"""

from __future__ import annotations

import json
import statistics
import sys
from collections import Counter, defaultdict


def annotate(runs, tasks):
    by_id = {task["id"]: task for task in tasks}
    for run in runs:
        task = by_id[run["task_id"]]
        if task["expected_status"] == "not_found":
            run["_target_complete"] = run["extra_targets"] == 0 and run["status_reported"] == "not_found"
        else:
            run["_target_complete"] = run["matched_targets"] == len(task["targets"]) and run["extra_targets"] == 0
        run["_status_ok"] = run["status_reported"] == task["expected_status"]
        if task["expected_status"] == "not_found":
            run["_coordinate_ok"] = run["success"]
        else:
            run["_coordinate_ok"] = run["success"] and run["line_exact"] == len(task["targets"])


def median(values):
    return statistics.median(values) if values else 0


def main() -> int:
    if len(sys.argv) != 4:
        print(__doc__, file=sys.stderr)
        return 2
    baseline = json.load(open(sys.argv[1], encoding="utf-8"))
    supplement = json.load(open(sys.argv[2], encoding="utf-8"))
    tasks = baseline["tasks"]
    merged = {}
    sources = defaultdict(list)
    for source, data in (("baseline", baseline), ("supplementary", supplement)):
        for run in data["runs"]:
            if run.get("error"):
                continue
            key = (run["task_id"], run["track"])
            if key not in merged:
                merged[key] = run
                sources[key].append(source)
            elif source == "baseline":
                merged[key] = run
                sources[key].append(source)
    runs = list(merged.values())
    annotate(runs, tasks)
    runs.sort(key=lambda run: (run["task_id"], run["track"]))

    output = {
        "metadata": {
            "description": "Valid agent-level cells only; provider errors excluded",
            "benchmark_revision": "phase2-valid-runs",
            "harness_version": baseline["metadata"]["harness_version"],
            "sources": [sys.argv[1], sys.argv[2]],
            "model": baseline["metadata"]["model"],
            "provider": baseline["metadata"]["provider"],
            "api": baseline["metadata"]["api"],
            "base_url": baseline["metadata"]["base_url"],
            "repository_revision": baseline["metadata"]["repository_revision"],
            "task_hash": baseline["metadata"]["task_hash"],
            "fixture_files": baseline["metadata"]["fixture_files"],
            "fixture_go_files": baseline["metadata"]["fixture_go_files"],
            "max_turns": baseline["metadata"]["max_turns"],
            "max_tokens": baseline["metadata"]["max_tokens"],
            "temperature": baseline["metadata"]["temperature"],
            "timeout_seconds": baseline["metadata"]["timeout_seconds"],
            "concurrency": baseline["metadata"]["concurrency"],
            "repeats": baseline["metadata"]["repeats"],
            "tracks": baseline["metadata"]["tracks"],
            "excluded_paths": baseline["metadata"]["excluded_paths"],
        },
        "tasks": tasks,
        "runs": runs,
        "coverage": {
            "cells": len(runs),
            "attempted_cells": len(baseline["runs"]) + len(supplement["runs"]),
            "tasks_with_all_tracks": sorted(
                task_id
                for task_id in {run["task_id"] for run in runs}
                if len({run["track"] for run in runs if run["task_id"] == task_id}) == 3
            ),
        },
        "sources": {f"{task}/{track}": value for (task, track), value in sources.items()},
    }
    with open(sys.argv[3], "w", encoding="utf-8") as handle:
        json.dump(output, handle, indent=2)
        handle.write("\n")

    print("valid cells:", len(runs))
    print("tasks with all tracks:", len(output["coverage"]["tasks_with_all_tracks"]))
    print("outcomes:", dict(Counter(run["outcome"] for run in runs)))
    print()
    print("| Track | Valid | Correct | Complete | Coordinate | Status | Turns (med) | Discovery calls (med) | Candidate files (med) | Files opened (med) | Context KB (med) | Input tok (med) | Output tok (med) | Duration ms (med) |")
    print("| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |")
    for track in sorted({run["track"] for run in runs}):
        subset = [run for run in runs if run["track"] == track]
        print(
            f"| {track} | {len(subset)} | {sum(run['success'] for run in subset)} | "
            f"{sum(run['_target_complete'] for run in subset)} | {sum(run['_coordinate_ok'] for run in subset)} | "
            f"{sum(run['_status_ok'] for run in subset)} | {median([run['turns'] for run in subset])} | "
            f"{median([run['discovery_calls'] for run in subset])} | {median([run['candidate_files'] for run in subset])} | "
            f"{median([run['files_opened'] for run in subset])} | {median([run['context_bytes'] for run in subset])/1024:.1f} | "
            f"{median([run['input_tokens'] for run in subset])} | {median([run['output_tokens'] for run in subset])} | "
            f"{median([run['duration_ms'] for run in subset]):.0f} |"
        )
    print()
    print("| Category | Track | Correct | Complete | Status |")
    print("| --- | --- | ---: | ---: | ---: |")
    for category in sorted({run["category"] for run in runs}):
        for track in sorted({run["track"] for run in runs}):
            subset = [run for run in runs if run["category"] == category and run["track"] == track]
            if not subset:
                continue
            print(
                f"| {category} | {track} | {sum(run['success'] for run in subset)}/{len(subset)} | "
                f"{sum(run['_target_complete'] for run in subset)}/{len(subset)} | "
                f"{sum(run['_status_ok'] for run in subset)}/{len(subset)} |"
            )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
