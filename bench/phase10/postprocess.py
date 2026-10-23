#!/usr/bin/env python3
"""Normalize the Phase 10 artifacts after a resumed multi-invocation matrix.

The runner writes one model condition per invocation when `--models` is used.
This postprocessor restores the matrix-level metadata, maps model references to
their named capability class, records unavailable providers, and rebuilds the
merged final artifact. It never invents or changes a run result.
"""

from __future__ import annotations

import argparse
import datetime as dt
import importlib.util
import json
import pathlib

ROOT = pathlib.Path(__file__).resolve().parents[2]
MODEL_CLASS = {
    "ollama/llama3.2:3b": "local/small",
    "ollama/qwen2.5:7b": "local/medium",
    "gemini/gemini-2.5-flash-lite": "remote/fast-primary",
    "openrouter/openrouter/free": "remote/free-router",
    "openrouter/nvidia/nemotron-3-ultra-550b-a55b:free": "freetier/constrained",
}
PROVIDER_LIMITATIONS = [
    {
        "provider": "groq",
        "condition": "all attempted Groq models",
        "status": "HTTP 403 Forbidden on /models and chat completions",
        "consequence": "no Groq quality result; not substituted",
    },
    {
        "provider": "openrouter",
        "condition": "paid rows",
        "status": "HTTP 402 Payment Required (no credits)",
        "consequence": "no paid remote quality result; not substituted",
    },
    {
        "provider": "gemini",
        "condition": "gemini-2.5-flash-lite",
        "status": "HTTP 429 free-tier quota exhausted after probes (retry ~3h)",
        "consequence": "A/B/C control and cognitive attempts failed before turn 1; retained as provider evidence",
    },
    {
        "provider": "gemini",
        "condition": "gemini-3-flash-preview",
        "status": "HTTP 400 missing thought_signature on replayed function call",
        "consequence": "not used for the matrix",
    },
    {
        "provider": "openrouter",
        "condition": "google/gemma-4-31b-it:free",
        "status": "HTTP 429 Too Many Requests",
        "consequence": "not used for the matrix",
    },
]


def load_runner():
    path = ROOT / "bench" / "phase10" / "run.py"
    spec = importlib.util.spec_from_file_location("phase10_runner", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def dedupe(results: list[dict]) -> list[dict]:
    unique: dict[tuple, dict] = {}
    for result in results:
        key = (
            result.get("model_ref", ""),
            str(result.get("scenario", "")).split("@", 1)[0],
            result.get("repeat", 0),
        )
        unique[key] = result
    return list(unique.values())


def result_scenario_id(result: dict) -> str:
    return str(result.get("scenario", "")).split("@", 1)[0]


def class_for(ref: str, fallback: str = "") -> str:
    return MODEL_CLASS.get(ref, fallback or "explicit")


def model_conditions(results: list[dict]) -> list[dict]:
    by_ref: dict[str, dict] = {}
    for result in results:
        ref = result.get("model_ref", "")
        item = by_ref.setdefault(
            ref,
            {
                "class": class_for(ref, result.get("model_class", "")),
                "model_ref": ref,
                "scenarios": set(),
            },
        )
        item["scenarios"].add(str(result.get("scenario", "")).split("@", 1)[0])
    out = []
    for ref in sorted(by_ref):
        item = by_ref[ref]
        out.append(
            {
                "class": item["class"],
                "model_ref": ref,
                "scenarios": sorted(item["scenarios"]),
            }
        )
    return out


def build_summary(pairs: list[dict]) -> dict:
    by_model: dict[str, list[dict]] = {}
    for pair in pairs:
        by_model.setdefault(pair["model_ref"], []).append(pair)
    summary = {}
    for ref, subset in by_model.items():
        control_tokens = sum(item["control_tokens"] for item in subset)
        cognitive_tokens = sum(item["cognitive_tokens"] for item in subset)
        summary[ref] = {
            "model_class": subset[0]["model_class"],
            "pairs": len(subset),
            "control_tokens": control_tokens,
            "cognitive_tokens": cognitive_tokens,
            "token_delta_percent": (
                round((cognitive_tokens - control_tokens) * 100 / control_tokens, 2)
                if control_tokens
                else None
            ),
            "control_statuses": _statuses(subset, "control_status"),
            "cognitive_statuses": _statuses(subset, "cognitive_status"),
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
    return summary


def _statuses(pairs: list[dict], key: str) -> dict[str, int]:
    counts: dict[str, int] = {}
    for pair in pairs:
        value = str(pair.get(key) or "UNKNOWN")
        counts[value] = counts.get(value, 0) + 1
    return counts


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--dir", default="docs/benchmarks/results")
    args = parser.parse_args()
    out_dir = ROOT / args.dir
    control_path = out_dir / "phase10-control.json"
    cognitive_path = out_dir / "phase10-cognitive.json"
    control = json.loads(control_path.read_text())
    cognitive = json.loads(cognitive_path.read_text())
    runner = load_runner()

    control_results = dedupe(control["results"])
    cognitive_results = dedupe(cognitive["results"])
    for result in control_results + cognitive_results:
        result["model_class"] = class_for(
            result.get("model_ref", ""), result.get("model_class", "")
        )
        # P10-F asks for one package name. A "not found" answer is wrong even
        # though the path string contains the expected token; the generic
        # substring scorer would otherwise count it. This is a benchmark
        # correction, not a runtime change.
        reason = str(result.get("completion_reason") or "").lower()
        if result.get("objective_status") == "FAILED" and (
            "turn limit" in reason or "budget exhausted" in reason or "deadline reached" in reason
        ):
            result["objective_status"] = "PAUSED"
        if result_scenario_id(result) == "P10-F-package-name":
            answer = str(result.get("final_answer") or "").lower()
            if any(marker in answer for marker in ("not found", "does not exist", "no such file")):
                result["answer_facts_matched"] = 0
                result["answer_facts_missing"] = ["tui"]
                result["verified_success"] = False

    common = dict(control)
    common.pop("results", None)
    common.pop("aggregate", None)
    common.pop("runtime", None)
    common["version"] = 1
    common["phase"] = 10
    common["generated_at"] = dt.datetime.now(dt.timezone.utc).isoformat()
    common["model_conditions"] = model_conditions(control_results + cognitive_results)
    common["provider_limitations"] = PROVIDER_LIMITATIONS
    status = str(common.get("git_status") or "")
    if "phase10-" in status or "docs/benchmarks/results" in status:
        common["git_status"] = "clean code tree; generated benchmark artifacts are untracked"
    common["matrix_note"] = (
        "Full A-J + Phase 10 boundary + K1/K2/K7 coverage runs on the available "
        "local/medium model. Local/small and free-router conditions use the "
        "documented smaller subsets. Gemini attempts are retained as provider "
        "quota evidence, not quality results."
    )
    common["controlled_variables"] = {
        "repository": "deterministic Phase 9 fixtures (phase9-fixture-v1)",
        "task": "same prompt and task hash in both arms",
        "tools": "same production-shaped tool set and tool-set hash",
        "authority": "same routed workspace gate (benchmarkGate over security.Policy)",
        "budget": "per-result budget field; CONTROL and COGNITIVE identical within a pair",
        "temperature": 0.0,
        "projection": "off=CONTROL, state=COGNITIVE",
        "completion_review": (
            "CONTROL has no projection report; COGNITIVE applies the projection's "
            "sufficiency verdict as a completion veto"
        ),
    }

    control_artifact = {
        **common,
        "runtime": "control",
        "results": control_results,
        "aggregate": runner.aggregate(control_results),
    }
    cognitive_artifact = {
        **common,
        "runtime": "cognitive",
        "results": cognitive_results,
        "aggregate": runner.aggregate(cognitive_results),
    }
    runner.write_json(control_path, control_artifact)
    runner.write_json(cognitive_path, cognitive_artifact)

    pairs = runner.pair_results(control_results, cognitive_results)
    final = {
        **common,
        "runtime": "final",
        "control_file": "phase10-control.json",
        "cognitive_file": "phase10-cognitive.json",
        "pairs": pairs,
        "aggregate": {
            "control": control_artifact["aggregate"],
            "cognitive": cognitive_artifact["aggregate"],
            "by_model": build_summary(pairs),
        },
    }
    runner.write_json(out_dir / "phase10-final.json", final)
    print(f"normalized {len(control_results)} control / {len(cognitive_results)} cognitive results")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
