package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// fixtureKind names a repository shape used by the benchmark matrix.
type fixtureKind string

const (
	fixtureStatic     fixtureKind = "static"
	fixtureGoDebug    fixtureKind = "go-debug"
	fixtureGoRefactor fixtureKind = "go-refactor"
	fixtureGoFuzz     fixtureKind = "go-fuzz"
	fixtureLarge      fixtureKind = "large"
	fixtureHeavy      fixtureKind = "heavy"
	// fixtureWorkflow is the evidence benchmark's repository: a CI workflow
	// that runs a script and pushes generated output, plus a passing Go
	// module. It makes "does this run automatically?" answerable from local
	// configuration while leaving "did GitHub run it?" unobservable.
	fixtureWorkflow fixtureKind = "workflow"
	// fixtureWorkflowBug is the same repository with one failing test.
	fixtureWorkflowBug fixtureKind = "workflow-bug"
)

// createFixture writes a deterministic repository and returns its root.
//
// The fixture is deliberately small enough to execute offline, but the source
// files carry enough bytes that repeated context is visible in the measured
// request sizes. Absolute token counts are fixture-specific and are reported as
// such; the comparison between baseline and projected runs is the evidence.
func createFixture(kind fixtureKind) (string, error) {
	root, err := os.MkdirTemp("", "ohgo-phase9-")
	if err != nil {
		return "", err
	}
	var files map[string]string
	switch kind {
	case fixtureStatic:
		files = staticFiles()
	case fixtureLarge:
		files = staticFiles()
		files["logs/huge.log"] = hugeLog()
	case fixtureHeavy:
		files = heavyFiles()
	case fixtureGoDebug:
		files = debugFiles()
	case fixtureGoRefactor:
		files = refactorFiles()
	case fixtureGoFuzz:
		files = fuzzFiles()
	case fixtureWorkflow:
		files = workflowFiles(true)
	case fixtureWorkflowBug:
		files = workflowFiles(false)
	default:
		_ = os.RemoveAll(root)
		return "", fmt.Errorf("unknown fixture %q", kind)
	}
	for path, content := range files {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			_ = os.RemoveAll(root)
			return "", err
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			_ = os.RemoveAll(root)
			return "", err
		}
	}
	return root, nil
}

// staticFiles is a small static-web project with one Go component.
func staticFiles() map[string]string {
	files := map[string]string{
		"index.html": `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <title>Nimbus Notes</title>
  <link rel="stylesheet" href="style.css">
</head>
<body>
  <header class="topbar">
    <h1>Nimbus Notes</h1>
    <p class="tagline">A tiny offline-first notebook.</p>
  </header>
  <main id="app">
    <form id="note-form">
      <label for="note-input">New note</label>
      <textarea id="note-input" rows="4" placeholder="Write something..."></textarea>
      <button type="submit">Save note</button>
    </form>
    <section id="notes" aria-live="polite"></section>
  </main>
  <footer>
    <p>Everything stays in this browser. No account, no server.</p>
  </footer>
  <script src="script.js"></script>
</body>
</html>
`,
		"style.css": `:root {
  --ink: #17202a;
  --paper: #fbfaf7;
  --accent: #2f6f8f;
  --line: #d9d4ca;
}
* { box-sizing: border-box; }
body {
  margin: 0;
  font-family: ui-sans-serif, system-ui, sans-serif;
  color: var(--ink);
  background: var(--paper);
}
.topbar { padding: 2rem; border-bottom: 1px solid var(--line); }
.topbar h1 { margin: 0; font-size: 2rem; }
.tagline { color: #5c6670; }
main { max-width: 48rem; margin: 0 auto; padding: 2rem; }
form { display: grid; gap: .5rem; }
textarea { width: 100%; padding: .75rem; border: 1px solid var(--line); border-radius: .5rem; }
button { justify-self: start; padding: .5rem 1rem; background: var(--accent); color: white; border: 0; border-radius: .4rem; }
#notes { display: grid; gap: .75rem; margin-top: 2rem; }
.note { padding: 1rem; border: 1px solid var(--line); border-radius: .5rem; background: white; }
.note time { display: block; color: #6b7280; font-size: .8rem; }
`,
		"script.js": `'use strict';

const STORAGE_KEY = 'nimbus-notes-v1';

function loadNotes() {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    return raw ? JSON.parse(raw) : [];
  } catch (error) {
    console.warn('could not read notes', error);
    return [];
  }
}

function saveNotes(notes) {
  localStorage.setItem(STORAGE_KEY, JSON.stringify(notes));
}

function render() {
  const notes = loadNotes();
  const list = document.getElementById('notes');
  list.replaceChildren();
  for (const note of notes) {
    const article = document.createElement('article');
    article.className = 'note';
    const body = document.createElement('p');
    body.textContent = note.text;
    const stamp = document.createElement('time');
    stamp.textContent = new Date(note.at).toLocaleString();
    article.append(body, stamp);
    list.append(article);
  }
}

document.getElementById('note-form').addEventListener('submit', (event) => {
  event.preventDefault();
  const input = document.getElementById('note-input');
  const text = input.value.trim();
  if (!text) return;
  const notes = loadNotes();
  notes.unshift({ text, at: Date.now() });
  saveNotes(notes);
  input.value = '';
  render();
});

render();
`,
		"tui/approval.go": `package tui

import "fmt"

// Approval is the UI projection of one authorization decision.
type Approval struct {
	Tool    string
	Reason  string
	Preview string
}

// Describe renders the decision for a human.
func (a Approval) Describe() string {
	if a.Reason == "" {
		return a.Tool
	}
	return fmt.Sprintf("%s (%s)", a.Tool, a.Reason)
}
`,
		"docs/ARCHITECTURE.md": `# Architecture

Nimbus Notes is a static web application. The browser owns all state.

- index.html is the entry point and contains the application shell.
- style.css owns the visual system.
- script.js owns note persistence and rendering.
- tui/approval.go is a small Go component used by the terminal tooling.
`,
		"go.mod": `module example.com/nimbus

go 1.22
`,
		"Makefile": `test:
	go test ./...

build:
	go build ./...

race:
	go test -race ./...
`,
	}
	return files
}

// heavyFiles is the same project with realistically large source files. It
// stresses repeated-context retention rather than the fixed tool surface.
func heavyFiles() map[string]string {
	files := staticFiles()
	files["index.html"] += strings.Repeat("<!-- layout note: the shell stays deliberately small. -->\n", 120)
	files["style.css"] += strings.Repeat("/* spacing scale: 4px base, 8px rhythm, 16px section. */\n", 140)
	files["script.js"] += strings.Repeat("// The browser is the data store; no network call is made here.\n", 120)
	files["tui/approval.go"] += strings.Repeat("// The terminal projection is presentation only; it never authorizes.\n", 80)
	files["docs/ARCHITECTURE.md"] += strings.Repeat("\nThe browser owns persistence; the terminal component is optional.\n", 60)
	return files
}

// debugFiles is a Go module with one real defect.
func debugFiles() map[string]string {
	return map[string]string{
		"go.mod": `module example.com/calc

go 1.22
`,
		"calc.go": `package calc

// Add returns the sum of a and b.
func Add(a, b int) int {
	return a - b
}

// Mul returns the product of a and b.
func Mul(a, b int) int {
	return a * b
}
`,
		"calc_test.go": `package calc

import "testing"

func TestAdd(t *testing.T) {
	if got := Add(2, 3); got != 5 {
		t.Fatalf("Add(2, 3) = %d, want 5", got)
	}
}

func TestMul(t *testing.T) {
	if got := Mul(2, 3); got != 6 {
		t.Fatalf("Mul(2, 3) = %d, want 6", got)
	}
}
`,
		"Makefile": `test:
	go test ./...
`,
	}
}

// refactorFiles is a Go module with duplicated geometry logic.
func refactorFiles() map[string]string {
	return map[string]string{
		"go.mod": `module example.com/geometry

go 1.22
`,
		"shape.go": `package geometry

// Area returns the area of a rectangle.
func Area(width, height int) int {
	return width * height
}

// Perimeter returns the perimeter of a rectangle.
func Perimeter(width, height int) int {
	return 2 * (width + height)
}
`,
		"extra.go": `package geometry

// DoubleArea returns twice the area of a rectangle.
func DoubleArea(width, height int) int {
	return 2 * Area(width, height)
}
`,
		"shape_test.go": `package geometry

import "testing"

func TestArea(t *testing.T) {
	if got := Area(3, 4); got != 12 {
		t.Fatalf("Area(3, 4) = %d, want 12", got)
	}
}
`,
		"extra_test.go": `package geometry

import "testing"

func TestDoubleArea(t *testing.T) {
	if got := DoubleArea(3, 4); got != 24 {
		t.Fatalf("DoubleArea(3, 4) = %d, want 24", got)
	}
}
`,
		"Makefile": `test:
	go test ./...
`,
	}
}

// fuzzFiles is a Go module with a malformed-input crash.
func fuzzFiles() map[string]string {
	return map[string]string{
		"go.mod": `module example.com/parser

go 1.22
`,
		"parser.go": `package parser

import "strings"

// Split splits a comma-separated list. It assumes at least one item.
func Split(input string) []string {
	parts := strings.Split(input, ",")
	_ = parts[0]
	return parts
}
`,
		"parser_test.go": `package parser

import "testing"

func TestSplitEmpty(t *testing.T) {
	got := Split("")
	if len(got) != 1 || got[0] != "" {
		t.Fatalf("Split(\"\") = %#v", got)
	}
}

func TestSplitPanicsOnNothing(t *testing.T) {
	// This test documents the intended failure surface for the investigation.
	defer func() {
		if recover() == nil {
			t.Fatal("expected malformed input handling to be investigated")
		}
	}()
	_ = Split("")
}
`,
	}
}

// workflowFiles is the evidence benchmark's repository shape.
//
// The workflow is deliberately unambiguous: a cron schedule makes it automatic,
// workflow_dispatch makes it startable by hand, and the job runs a script and
// pushes generated output. Every claim the benchmark asks about is therefore
// provable from local configuration, except the remote question — whether
// GitHub actually ran it — which no local file can establish.
func workflowFiles(passing bool) map[string]string {
	add := `	return a + b`
	if !passing {
		add = `	return a - b`
	}
	return map[string]string{
		"go.mod": `module example.com/telemetry

go 1.22
`,
		"calc.go": `package calc

// Add returns the sum of a and b.
func Add(a, b int) int {
` + add + `
}
`,
		"calc_test.go": `package calc

import "testing"

func TestAdd(t *testing.T) {
	if got := Add(2, 3); got != 5 {
		t.Fatalf("Add(2, 3) = %d, want 5", got)
	}
}
`,
		".github/workflows/telemetry.yml": `name: telemetry

on:
  schedule:
    - cron: "17 3 * * *"
  workflow_dispatch:

permissions:
  contents: write

jobs:
  refresh:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - name: Regenerate telemetry.svg
        run: python3 scripts/telemetry.py
      - name: Commit the regenerated file
        run: |
          git config user.name "telemetry-bot"
          git add telemetry.svg
          git commit -m "chore: refresh telemetry" || echo "no changes"
          git push
`,
		"scripts/telemetry.py": `#!/usr/bin/env python3
"""Regenerate telemetry.svg from the repository's own history.

The workflow runs this script on a schedule and then commits the result, so the
chart in the README stays current without a human in the loop.
"""

from pathlib import Path


def main() -> None:
    out = Path(__file__).resolve().parent.parent / "telemetry.svg"
    out.write_text(
        '<svg xmlns="http://www.w3.org/2000/svg" width="120" height="20">'
        "<text x=\"4\" y=\"15\">telemetry</text></svg>\n",
        encoding="utf-8",
    )


if __name__ == "__main__":
    main()
`,
		"README.md": `# Telemetry

A small Go module with a scheduled telemetry refresh.

The chart is regenerated by ` + "`scripts/telemetry.py`" + ` and committed by
` + "`.github/workflows/telemetry.yml`" + `.
`,
		"Makefile": `test:
	go test ./...

build:
	go build ./...
`,
	}
}

// hugeLog produces a large, largely irrelevant file for retention tests.
func hugeLog() string {
	var sb strings.Builder
	for i := 0; i < 6000; i++ {
		fmt.Fprintf(&sb, "%05d  routine scheduler heartbeat: queue=%d latency_ms=%d status=ok\n", i, i%17, i%93)
	}
	return sb.String()
}
