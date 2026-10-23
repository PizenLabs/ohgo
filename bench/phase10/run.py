#!/usr/bin/env python3
"""Run the Phase 10 CONTROL/COGNITIVE real-model matrix.

The runner is deliberately a thin orchestration layer over the existing
`bench/phase9` harness. It does not implement another agent, another authority
gate, or another evaluator. It:

  * pins each model reference on the command line;
  * runs the same Phase 10 scenario with projection off (CONTROL) and on
    (COGNITIVE), with identical tools, authority, budget, and timeout;
  * retries only transient provider failures, and records the attempt count;
  * writes the control, cognitive, and merged final artifacts.

Usage:
  python3 bench/phase10/run.py --out-dir docs/benchmarks/results
  python3 bench/phase10/run.py --models openrouter/openrouter/free --scenarios A-pure-read
"""

from __future__ import annotations

import argparse
import datetime as dt
import json
import os
import pathlib
import subprocess
import sys
import tempfile
import time

ROOT = pathlib.Path(__file__).resolve().parents[2]
PHASE9 = ROOT / "bench" / "phase9"

# The default matrix deliberately spans capability classes rather than
# provider prestige. A model is included only if it is available in this
# environment; unavailable providers are recorded by their failures, never
# replaced silently.
DEFAULT_MATRIX = [
    {
        "class": "local/small",
        "ref": "ollama/llama3.2:3b",
        "scenarios": [
            "A-pure-read",
            "B-read-build",
            "C-explicit-boundary",
            "P10-C1-read-file",
            "P10-C2-read-then-update",
            "G-debugging",
            "K1-irrelevant-large-file",
            "K7-stop-after-proof",
        ],
        "min_interval": "0",
    },
    {
        "class": "local/medium",
        "ref": "ollama/qwen2.5:7b",
        "scenarios": [
            "A-pure-read",
            "B-read-build",
            "C-explicit-boundary",
            "P10-C1-read-file",
            "P10-C2-read-then-update",
            "G-debugging",
            "K1-irrelevant-large-file",
            "K7-stop-after-proof",
        ],
        "min_interval": "0",
    },
    {
        "class": "remote/fast-primary",
        "ref": "gemini/gemini-2.5-flash-lite",
        "scenarios": [
            "A-pure-read",
            "B-read-build",
            "C-explicit-boundary",
            "D-mutation-after-investigation",
            "E-embedded-mutation",
            "F-negative-control",
            "G-debugging",
            "H-refactoring",
            "I-fuzzing",
            "J-external-knowledge",
            "K1-irrelevant-large-file",
            "K2-missing-file",
            "K3-failed-verification",
            "K4-unexpected-mutation",
            "K5-partial-execution",
            "K6-large-source-context",
            "K7-stop-after-proof",
            "P10-C1-read-file",
            "P10-C2-read-then-update",
            "P10-F-package-name",
        ],
        "min_interval": "0",
    },
    {
        "class": "remote/free-router",
        "ref": "openrouter/openrouter/free",
        "scenarios": [
            "A-pure-read",
            "G-debugging",
            "K1-irrelevant-large-file",
        ],
        "min_interval": "1s",
        "max_turns": 8,
        "max_tokens": 1024,
        "timeout": "3m",
    },
    {
        "class": "freetier/constrained",
        "ref": "openrouter/nvidia/nemotron-3-ultra-550b-a55b:free",
        "scenarios": [
            "A-pure-read",
            "G-debugging",
            "K1-irrelevant-large-file",
        ],
        "min_interval": "1s",
        "max_turns": 8,
        "max_tokens": 1024,
        "timeout": "3m",
    },
]

TRANSIENT_MARKERS = (
    "http 408",
    "http 409",
    "http 425",
    "http 429",
    "http 500",
    "http 502",
    "http 503",
    "http 504",
    "upstream error",
    "overloaded",
    "temporarily",
    "timeout",
    "connection reset",
    "rate limit",
)


def git_state() -> tuple[str, str]:
    try:
        revision = subprocess.check_output(
            ["git", "rev-parse", "HEAD"], cwd=ROOT, text=True
        ).strip()
        status = subprocess.check_output(
            ["git", "status", "--porcelain"], cwd=ROOT, text=True
        ).strip()
        return revision, status
    except Exception as exc:  # pragma: no cover - environment failure
        return "unknown", f"git state unavailable: {exc}"


def transient_failure(result: dict) -> bool:
    reason = str(result.get("completion_reason") or "").lower()
    return any(marker in reason for marker in TRANSIENT_MARKERS)


def result_scenario_id(result: dict) -> str:
    """The scenario id without the model suffix used in the result label."""
    return str(result.get("scenario", "")).split("@", 1)[0]


def dedupe_results(results: list[dict]) -> list[dict]:
    """Keep one result per (model, scenario, repeat); later resume wins."""
    unique: dict[tuple, dict] = {}
    for result in results:
        key = (
            result.get("model_ref", ""),
            result_scenario_id(result),
            result.get("repeat", 0),
        )
        unique[key] = result
    return list(unique.values())


def run_one(
    binary: pathlib.Path,
    model: dict,
    scenario: str,
    projection: str,
    args: argparse.Namespace,
) -> dict:
    """Run one scenario and return its result plus harness metadata."""
    fd, name = tempfile.mkstemp(prefix="phase10-", suffix=".json")
    os.close(fd)
    out = pathlib.Path(name)
    out.unlink(missing_ok=True)
    command = [
        str(binary),
        "-live-model",
        model["ref"],
        "-suite",
        "phase10",
        "-scenario",
        scenario,
        "-projection",
        projection,
        "-max-turns",
        str(model.get("max_turns", args.max_turns)),
        "-max-tokens",
        str(model.get("max_tokens", args.max_tokens)),
        "-temperature",
        str(args.temperature),
        "-timeout",
        model.get("timeout", args.timeout),
        "-retries",
        str(args.retries),
        "-min-interval",
        model.get("min_interval", "0"),
        "-out",
        str(out),
    ]
    started = time.time()
    attempts = 0
    while True:
        attempts += 1
        try:
            completed = subprocess.run(
                command,
                cwd=ROOT,
                text=True,
                capture_output=True,
                timeout=args.process_timeout,
            )
            if out.exists():
                data = json.loads(out.read_text())
                if data:
                    result = data[0]
                else:
                    result = {}
            else:
                result = {}
            if not result:
                result = {
                    "scenario": scenario,
                    "objective_status": "HARNESS_ERROR",
                    "completion_reason": (
                        completed.stderr.strip()
                        or completed.stdout.strip()
                        or f"phase9 exited {completed.returncode} without a result"
                    ),
                    "turn_count": 0,
                    "tool_call_count": 0,
                    "estimated_input_tokens": 0,
                }
        except subprocess.TimeoutExpired:
            result = {
                "scenario": scenario,
                "objective_status": "HARNESS_ERROR",
                "completion_reason": f"process timeout after {args.process_timeout}s",
                "turn_count": 0,
                "tool_call_count": 0,
                "estimated_input_tokens": 0,
            }
        finally:
            out.unlink(missing_ok=True)

        if (
            result.get("objective_status") != "FAILED"
            or attempts > args.run_retries
            or not transient_failure(result)
        ):
            break
        time.sleep(args.retry_pause)

    result["model_class"] = model["class"]
    result["model_ref"] = model["ref"]
    result["attempts"] = attempts
    result["runtime"] = "control" if projection == "off" else "cognitive"
    result["wall_seconds"] = round(time.time() - started, 3)
    facts_expected = int(result.get("answer_facts_expected") or 0)
    facts_matched = int(result.get("answer_facts_matched") or 0)
    result["verified_success"] = bool(
        result.get("objective_status") == "COMPLETED"
        and (facts_expected == 0 or facts_matched >= facts_expected)
        and not (result.get("mutated") and not result.get("verified"))
    )
    return result


def aggregate(results: list[dict]) -> dict:
    def numbers(key: str):
        return [float(r.get(key) or 0) for r in results]

    def statuses() -> dict:
        counts: dict[str, int] = {}
        for result in results:
            state = result.get("objective_status") or "UNKNOWN"
            counts[state] = counts.get(state, 0) + 1
        return counts

    def mean(values):
        return round(sum(values) / len(values), 3) if values else 0

    by_model: dict[str, dict] = {}
    for result in results:
        key = result.get("model_ref") or "unknown"
        bucket = by_model.setdefault(
            key,
            {
                "model_class": result.get("model_class", ""),
                "runs": 0,
                "statuses": {},
                "estimated_input_tokens": 0,
                "provider_input_tokens": 0,
                "provider_output_tokens": 0,
                "tool_calls": 0,
                "turns": 0,
                "candidate_rejections": 0,
                "authorization_refusals": 0,
                "facts_matched": 0,
                "facts_expected": 0,
                "verified_success": 0,
                "provider_cost_usd": 0.0,
            },
        )
        bucket["runs"] += 1
        state = result.get("objective_status") or "UNKNOWN"
        bucket["statuses"][state] = bucket["statuses"].get(state, 0) + 1
        bucket["estimated_input_tokens"] += int(result.get("estimated_input_tokens") or 0)
        bucket["provider_input_tokens"] += int(result.get("provider_input_tokens") or 0)
        bucket["provider_output_tokens"] += int(result.get("provider_output_tokens") or 0)
        bucket["tool_calls"] += int(result.get("tool_call_count") or 0)
        bucket["turns"] += int(result.get("turn_count") or 0)
        bucket["candidate_rejections"] += int(result.get("candidate_rejections") or 0)
        bucket["authorization_refusals"] += int(result.get("authorization_refusals") or 0)
        bucket["facts_matched"] += int(result.get("answer_facts_matched") or 0)
        bucket["facts_expected"] += int(result.get("answer_facts_expected") or 0)
        bucket["verified_success"] += int(bool(result.get("verified_success")))
        bucket["provider_cost_usd"] += float(result.get("provider_cost_usd") or 0)

    return {
        "runs": len(results),
        "statuses": statuses(),
        "estimated_input_tokens": sum(numbers("estimated_input_tokens")),
        "provider_input_tokens": sum(numbers("provider_input_tokens")),
        "provider_output_tokens": sum(numbers("provider_output_tokens")),
        "tool_calls": sum(numbers("tool_call_count")),
        "turns": sum(numbers("turn_count")),
        "candidate_rejections": sum(numbers("candidate_rejections")),
        "authorization_refusals": sum(numbers("authorization_refusals")),
        "facts_matched": sum(numbers("answer_facts_matched")),
        "facts_expected": sum(numbers("answer_facts_expected")),
        "verified_success": sum(1 for result in results if result.get("verified_success")),
        "mean_estimated_input_tokens": mean(numbers("estimated_input_tokens")),
        "by_model": by_model,
    }


def pair_results(control: list[dict], cognitive: list[dict]) -> list[dict]:
    def key(result: dict):
        return (
            result.get("model_ref", ""),
            result.get("scenario", ""),
            result.get("repeat", 0),
        )

    cognitive_map = {key(result): result for result in cognitive}
    pairs = []
    for control_result in control:
        match = cognitive_map.get(key(control_result))
        if match is None:
            continue
        pairs.append(
            {
                "model_ref": control_result.get("model_ref"),
                "model_class": control_result.get("model_class"),
                "scenario": control_result.get("scenario"),
                "repeat": control_result.get("repeat", 0),
                "control_status": control_result.get("objective_status"),
                "cognitive_status": match.get("objective_status"),
                "control_tokens": control_result.get("estimated_input_tokens", 0),
                "cognitive_tokens": match.get("estimated_input_tokens", 0),
                "control_turns": control_result.get("turn_count", 0),
                "cognitive_turns": match.get("turn_count", 0),
                "control_tools": control_result.get("tool_call_count", 0),
                "cognitive_tools": match.get("tool_call_count", 0),
                "control_facts": control_result.get("answer_facts_matched", 0),
                "cognitive_facts": match.get("answer_facts_matched", 0),
                "facts_expected": match.get("answer_facts_expected", 0),
                "control_verified_success": bool(control_result.get("verified_success")),
                "cognitive_verified_success": bool(match.get("verified_success")),
            }
        )
    return pairs


def write_json(path: pathlib.Path, value: dict) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, indent=2, sort_keys=False) + "\n")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--out-dir", default="docs/benchmarks/results")
    parser.add_argument("--models", default="", help="comma-separated model refs; default matrix")
    parser.add_argument("--scenarios", default="", help="comma-separated scenario ids; default per model")
    parser.add_argument("--repeats", type=int, default=1)
    parser.add_argument("--max-turns", type=int, default=12)
    parser.add_argument("--max-tokens", type=int, default=2048)
    parser.add_argument("--temperature", type=float, default=0.0)
    parser.add_argument("--timeout", default="5m", help="per-run wall-clock budget passed to the harness")
    parser.add_argument("--process-timeout", type=int, default=420)
    parser.add_argument("--retries", type=int, default=3, help="stream-open retries inside the harness")
    parser.add_argument("--run-retries", type=int, default=1, help="whole-run retries for transient failures")
    parser.add_argument("--retry-pause", type=float, default=5.0)
    parser.add_argument("--pause", type=float, default=0.5)
    parser.add_argument("--control-only", action="store_true")
    parser.add_argument("--cognitive-only", action="store_true")
    parser.add_argument("--resume", action="store_true", help="reuse completed pairs from existing control/cognitive artifacts")
    args = parser.parse_args()

    out_dir = ROOT / args.out_dir
    revision, status = git_state()

    matrix = [dict(item) for item in DEFAULT_MATRIX]
    if args.models:
        wanted = [item.strip() for item in args.models.split(",") if item.strip()]
        matrix = [
            {
                "class": "explicit",
                "ref": ref,
                "scenarios": [
                    item.strip()
                    for item in (args.scenarios.split(",") if args.scenarios else [])
                    if item.strip()
                ],
                "min_interval": "1s" if ref.startswith("openrouter/") else "0",
            }
            for ref in wanted
        ]
        if not args.scenarios:
            print("--models without --scenarios would run the whole matrix; pass --scenarios", file=sys.stderr)
            return 2

    common = {
        "version": 1,
        "phase": 10,
        "generated_at": dt.datetime.now(dt.timezone.utc).isoformat(),
        "git_revision": revision,
        "git_status": status,
        "harness": "bench/phase9 -suite phase10",
        "controlled_variables": {
            "repository": "deterministic Phase 9 fixtures (phase9-fixture-v1)",
            "task": "same prompt and task hash in both arms",
            "tools": "same production-shaped tool set and tool-set hash",
            "authority": "same routed workspace gate (benchmarkGate over security.Policy)",
            "budget": f"max-turns={args.max_turns} max-tokens={args.max_tokens} timeout={args.timeout}",
            "temperature": args.temperature,
            "projection": "off=CONTROL, state=COGNITIVE",
            "completion_review": (
                "CONTROL has no projection report; COGNITIVE applies the "
                "projection's sufficiency verdict as a completion veto"
            ),
        },
        "model_conditions": [
            {
                "class": item["class"],
                "model_ref": item["ref"],
                "scenarios": item["scenarios"],
                "min_interval": item.get("min_interval", "0"),
            }
            for item in matrix
        ],
    }
    with tempfile.TemporaryDirectory(prefix="phase10-bin-") as tmp:
        binary = pathlib.Path(tmp) / "phase9"
        subprocess.run(
            ["go", "build", "-o", str(binary), "./bench/phase9"],
            cwd=ROOT,
            check=True,
        )

        control: list[dict] = []
        cognitive: list[dict] = []
        if args.resume:
            for path, target in (
                (out_dir / "phase10-control.json", control),
                (out_dir / "phase10-cognitive.json", cognitive),
            ):
                if path.exists():
                    try:
                        target.extend(json.loads(path.read_text()).get("results", []))
                    except Exception as exc:
                        print(f"resume: ignoring {path}: {exc}", file=sys.stderr)
        control[:] = dedupe_results(control)
        cognitive[:] = dedupe_results(cognitive)
        completed = {
            (result.get("model_ref", ""), result_scenario_id(result), result.get("repeat", 0))
            for result in control
        }
        completed &= {
            (result.get("model_ref", ""), result_scenario_id(result), result.get("repeat", 0))
            for result in cognitive
        }

        def persist(runtime: str) -> None:
            if runtime == "control":
                write_json(
                    out_dir / "phase10-control.json",
                    {**common, "runtime": "control", "results": control, "aggregate": aggregate(control)},
                )
            else:
                write_json(
                    out_dir / "phase10-cognitive.json",
                    {**common, "runtime": "cognitive", "results": cognitive, "aggregate": aggregate(cognitive)},
                )

        for model in matrix:
            scenarios = model["scenarios"]
            if not scenarios:
                print(f"no scenarios for {model['ref']}", file=sys.stderr)
                continue
            for scenario in scenarios:
                for repeat in range(args.repeats):
                    key = (model["ref"], scenario, repeat)
                    if key in completed:
                        print(f"skip      {model['ref']:<52} {scenario} #{repeat + 1} (resume)", flush=True)
                        continue
                    if not args.cognitive_only:
                        print(f"control   {model['ref']:<52} {scenario} #{repeat + 1}", flush=True)
                        result = run_one(binary, model, scenario, "off", args)
                        result["repeat"] = repeat
                        control.append(result)
                        persist("control")
                        time.sleep(args.pause)
                    if not args.control_only:
                        print(f"cognitive {model['ref']:<52} {scenario} #{repeat + 1}", flush=True)
                        result = run_one(binary, model, scenario, "state", args)
                        result["repeat"] = repeat
                        cognitive.append(result)
                        persist("cognitive")
                        time.sleep(args.pause)

    control_artifact = {
        **common,
        "runtime": "control",
        "results": control,
        "aggregate": aggregate(control),
    }
    cognitive_artifact = {
        **common,
        "runtime": "cognitive",
        "results": cognitive,
        "aggregate": aggregate(cognitive),
    }
    write_json(out_dir / "phase10-control.json", control_artifact)
    write_json(out_dir / "phase10-cognitive.json", cognitive_artifact)

    pairs = pair_results(control, cognitive)
    by_model: dict[str, list[dict]] = {}
    for pair in pairs:
        by_model.setdefault(pair["model_ref"], []).append(pair)
    model_summary = {}
    for model_ref, subset in by_model.items():
        control_tokens = sum(item["control_tokens"] for item in subset)
        cognitive_tokens = sum(item["cognitive_tokens"] for item in subset)
        model_summary[model_ref] = {
            "model_class": subset[0]["model_class"],
            "pairs": len(subset),
            "control_tokens": control_tokens,
            "cognitive_tokens": cognitive_tokens,
            "token_delta_percent": (
                round((cognitive_tokens - control_tokens) * 100 / control_tokens, 2)
                if control_tokens
                else None
            ),
            "control_statuses": _status_counts(subset, "control_status"),
            "cognitive_statuses": _status_counts(subset, "cognitive_status"),
            "control_facts": sum(item["control_facts"] for item in subset),
            "cognitive_facts": sum(item["cognitive_facts"] for item in subset),
            "facts_expected": sum(item["facts_expected"] for item in subset),
            "control_verified_success": sum(
                1 for item in subset if item["control_verified_success"]
            ),
            "cognitive_verified_success": sum(
                1 for item in subset if item["cognitive_verified_success"]
            ),
        }

    final_artifact = {
        **common,
        "runtime": "final",
        "control_file": "phase10-control.json",
        "cognitive_file": "phase10-cognitive.json",
        "pairs": pairs,
        "aggregate": {
            "control": control_artifact["aggregate"],
            "cognitive": cognitive_artifact["aggregate"],
            "by_model": model_summary,
        },
    }
    write_json(out_dir / "phase10-final.json", final_artifact)
    print(f"wrote {out_dir / 'phase10-control.json'}")
    print(f"wrote {out_dir / 'phase10-cognitive.json'}")
    print(f"wrote {out_dir / 'phase10-final.json'}")
    return 0


def _status_counts(pairs: list[dict], key: str) -> dict[str, int]:
    counts: dict[str, int] = {}
    for pair in pairs:
        value = str(pair.get(key) or "UNKNOWN")
        counts[value] = counts.get(value, 0) + 1
    return counts


if __name__ == "__main__":
    raise SystemExit(main())
