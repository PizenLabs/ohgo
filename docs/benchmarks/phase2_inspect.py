#!/usr/bin/env python3
"""Print the evidence behind every failed run in a Phase 2 result file.

Usage:
    python3 docs/benchmarks/phase2_inspect.py docs/benchmarks/results/phase2-baseline.json [task-id ...]

This is an analysis aid, not an evaluator. It never changes a score; it shows
the model's own calls, the tool output sizes, and the final text so a failure
can be classified as a discovery, reasoning, model, or harness failure.
"""

from __future__ import annotations

import json
import sys


def main() -> int:
    if len(sys.argv) < 2:
        print(__doc__, file=sys.stderr)
        return 2
    with open(sys.argv[1], encoding="utf-8") as handle:
        data = json.load(handle)
    wanted = set(sys.argv[2:])
    for run in data["runs"]:
        if run["success"]:
            continue
        if wanted and run["task_id"] not in wanted:
            continue
        print("=" * 100)
        print(
            f"{run['task_id']} | {run['track']} | {run['outcome']} | "
            f"status={run['status_reported']!r} turns={run['turns']} calls={run['tool_calls']}"
        )
        print(
            f"tokens in/out={run['input_tokens']}/{run['output_tokens']} "
            f"candidate_files={run['candidate_files']} opened={run['files_opened']} "
            f"context={run['context_bytes']}B duration={run['duration_ms']}ms"
        )
        if run.get("error"):
            print(f"error: {run['error']}")
        for call in run.get("calls", []):
            args = json.dumps(call.get("args"), sort_keys=True)
            print(
                f"  [{call['index']:02d}] {call['tool']:<8} {args} "
                f"-> {call['result_bytes']}B/{call['result_lines']}L "
                f"files={len(call.get('candidate_files') or [])} "
                f"revealed={call.get('revealed_targets') or []}"
            )
        print("  final:")
        for line in (run.get("final_text") or "<empty>").splitlines()[-12:]:
            print(f"    {line}")
        print()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
