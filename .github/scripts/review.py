#!/usr/bin/env python3
"""
ohgo Zero-Cost PR Reviewer Engine
Extracts changed lines only, bypasses unchanged context, and calls Free-tier LLMs.
"""

import os
import sys
import re
import json
import urllib.request
import urllib.error

# Files or patterns to ignore to keep zero-cost footprint
IGNORE_PATTERNS = [
    r"go\.sum$",
    r"Cargo\.lock$",
    r"package-lock\.json$",
    r"yarn\.lock$",
    r"\.min\.(js|css)$",
    r"assets/.*",
    r"vendor/.*",
    r"dist/.*",
]

def is_ignored(filename: str) -> bool:
    """Check if file matches the ignore patterns."""
    return any(re.search(pattern, filename) for pattern in IGNORE_PATTERNS)

def parse_git_diff_minimal(diff_text: str) -> dict:
    """
    Extract ONLY modified, added, or removed lines (+ / -).
    Strips out unchanged surrounding context lines to save 70%+ of LLM tokens.
    """
    files = {}
    current_file = None
    hunks = []

    for line in diff_text.splitlines():
        if line.startswith("diff --git"):
            if current_file and not is_ignored(current_file) and hunks:
                files[current_file] = "\n".join(hunks)
            match = re.search(r"b/(.+)$", line)
            current_file = match.group(1) if match else None
            hunks = []
        elif current_file and not is_ignored(current_file):
            # Include diff headers and lines that are added (+) or removed (-)
            if line.startswith("@@") or line.startswith("+") or line.startswith("-"):
                hunks.append(line)

    if current_file and not is_ignored(current_file) and hunks:
        files[current_file] = "\n".join(hunks)

    return files

def build_prompt(parsed_diffs: dict) -> str:
    """Construct a strict systems-engineering prompt for the Free-Tier LLM."""
    formatted_diff = ""
    for filename, content in parsed_diffs.items():
        formatted_diff += f"\n--- File: {filename} ---\n{content[:3000]}\n"

    prompt = f"""You are `ohgo`, an autonomous backend & systems engineering reviewer.
Analyze the following minimal git diff (added/removed lines only) and output a concise critique.

### GUIDELINES:
1. FOCUS STRICTLY ON:
   - Concurrency bugs (race conditions, deadlocks, channel leaks).
   - Resource leaks (unclosed sockets, file descriptors, goroutines).
   - Error handling traps & unhandled edge cases.
   - High-impact performance hot paths.
2. IGNORE:
   - Formatting, code style, imports, and naming preferences.
3. OUTPUT FORMAT:
   - Serious, direct, systems-engineer tone. Concise Markdown only.

### MINIMAL PR DIFF:
{formatted_diff}
"""
    return prompt

def query_gemini_free(prompt: str, api_key: str) -> str:
    """
    Query Google Gemini API directly using Python standard library (Zero-Cost Free Tier).
    Endpoint: gemini-3.1-flash-lite via Google AI Studio API key.
    """
    url = f"https://generativelanguage.googleapis.com/v1beta/models/gemini-3.1-flash-lite:generateContent?key={api_key}"
    
    payload = {
        "contents": [{
            "parts": [{"text": prompt}]
        }],
        "generationConfig": {
            "temperature": 0.2,
            "maxOutputTokens": 1024
        }
    }

    req = urllib.request.Request(
        url,
        data=json.dumps(payload).encode("utf-8"),
        headers={"Content-Type": "application/json"}
    )

    try:
        with urllib.request.urlopen(req, timeout=30) as response:
            res_data = json.loads(response.read().decode("utf-8"))
            return res_data["candidates"][0]["content"]["parts"][0]["text"]
    except Exception as e:
        sys.stderr.write(f"Free Gemini API Call failed: {e}\n")
        return None

def fallback_review(parsed_diffs: dict) -> str:
    """Zero-cost static analysis fallback if API call limit is reached."""
    total_files = len(parsed_diffs)
    summary = f"### ⚯  `ohgo` zero-cost diff summary (Offline Mode)\n\n"
    summary += f"- **Files touched:** {total_files}\n"
    summary += f"- **Notice:** LLM invocation bypassed or offline fallback active.\n\n"
    summary += "| File | Status |\n| :--- | :--- |\n"
    for f in parsed_diffs.keys():
        summary += f"| `{f}` | Minimal diff parsed successfully |\n"
    return summary

def main():
    if len(sys.argv) < 2:
        print("Usage: python3 review.py <diff_file>")
        sys.exit(1)

    diff_file = sys.argv[1]
    api_key = os.environ.get("GEMINI_API_KEY", "").strip()

    try:
        with open(diff_file, "r", encoding="utf-8", errors="ignore") as f:
            diff_text = f.read()
    except Exception as e:
        print(f"Error reading diff file: {e}")
        sys.exit(1)

    parsed_diffs = parse_git_diff_minimal(diff_text)

    if not parsed_diffs:
        print("### ⚯  `ohgo` code review\n\nNo critical logic changes detected (only ignored files or lockfiles modified).")
        return

    review_text = None
    if api_key:
        prompt = build_prompt(parsed_diffs)
        review_text = query_gemini_free(prompt, api_key)

    if not review_text:
        review_text = fallback_review(parsed_diffs)

    print(review_text)

if __name__ == "__main__":
    main()
