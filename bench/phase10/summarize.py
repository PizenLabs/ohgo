#!/usr/bin/env python3
"""Print Markdown tables from the Phase 10 final artifact."""

from __future__ import annotations

import argparse
import json
import pathlib


def pct(value):
    return "—" if value is None else f"{value:+.1f}%"


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "artifact",
        nargs="?",
        default="docs/benchmarks/results/phase10-final.json",
    )
    parser.add_argument("--model", default="", help="per-scenario table for one model_ref")
    args = parser.parse_args()
    data = json.loads(pathlib.Path(args.artifact).read_text())

    print("## Model summary\n")
    print(
        "| Class | Model | Pairs | Control statuses | Cognitive statuses | "
        "Control tokens | Cognitive tokens | Token delta | Facts C/G (expected) | Verified success C/G |"
    )
    print("|---|---|---:|---|---|---:|---:|---:|---:|---:|")
    for model, item in data["aggregate"]["by_model"].items():
        print(
            f"| {item['model_class']} | `{model}` | {item['pairs']} | "
            f"{item['control_statuses']} | {item['cognitive_statuses']} | "
            f"{item['control_tokens']:,} | {item['cognitive_tokens']:,} | "
            f"{pct(item['token_delta_percent'])} | "
            f"{item['control_facts']}/{item['cognitive_facts']} "
            f"({item['facts_expected']}) | "
            f"{item['control_verified_success']}/{item['cognitive_verified_success']} |"
        )

    if args.model:
        print(f"\n## Per-scenario: `{args.model}`\n")
        print(
            "| Scenario | Control | Cognitive | C tokens | G tokens | "
            "C tools | G tools | C facts | G facts | Expected facts |"
        )
        print("|---|---|---|---:|---:|---:|---:|---:|---:|---:|")
        for pair in data["pairs"]:
            if pair["model_ref"] != args.model:
                continue
            print(
                f"| {pair['scenario']} | {pair['control_status']} | "
                f"{pair['cognitive_status']} | {pair['control_tokens']:,} | "
                f"{pair['cognitive_tokens']:,} | {pair['control_tools']} | "
                f"{pair['cognitive_tools']} | {pair['control_facts']} | "
                f"{pair['cognitive_facts']} | {pair['facts_expected']} |"
            )

    print("\n## Aggregate\n")
    print(f"- Git revision: `{data.get('git_revision')}`")
    print(f"- Generated: {data.get('generated_at')}")
    print(f"- CONTROL: {data['aggregate']['control']['statuses']}")
    print(f"- COGNITIVE: {data['aggregate']['cognitive']['statuses']}")
    print(
        f"- Estimated input tokens: "
        f"{data['aggregate']['control']['estimated_input_tokens']:,} → "
        f"{data['aggregate']['cognitive']['estimated_input_tokens']:,}"
    )
    print(
        f"- Verified successes: "
        f"{data['aggregate']['control']['verified_success']} → "
        f"{data['aggregate']['cognitive']['verified_success']}"
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
