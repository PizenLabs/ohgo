# ohgo

**Module:** `github.com/PizenLabs/ohgo` · **Status:** working product (see
[`CHANGELOG.md`](CHANGELOG.md))

---

## Install

```sh
# From source (static binary, no cgo)
make build
./ohgo --help

# Install into ~/.local/bin (first on PATH, no sudo needed)
make install

# System-wide install instead
sudo make install PREFIX=/usr/local

# Release tarball, once a version is published on GitHub
OHGO_VERSION=v0.1.0 sh install.sh

# Docker
make docker
docker run -it -v "$PWD:/work" ohgo:dev
```

Requires Go 1.27+ for source builds. No C toolchain: `CGO_ENABLED=0 go build ./...`
succeeds and the dependency graph contains zero C packages.

First run:

```sh
ohgo init          # writes ~/.ohgo/settings.json (never overwrites without --force)
ohgo doctor        # every failure names its fix
ohgo               # interactive session
```

## Providers

| Provider | Auth | Protocol | Models |
| --- | --- | --- | --- |
| Anthropic | `ANTHROPIC_API_KEY` | Messages | 28 bundled |
| OpenAI | `OPENAI_API_KEY` | Chat Completions | 47 bundled (Responses-only lines excluded) |
| Groq, Together, Fireworks, DeepSeek, Cerebras, Mistral | per-provider key | Chat Completions | bundled |
| OpenRouter | `OPENROUTER_API_KEY` | Chat Completions | 7 seeds + live discovery |
| OpenCode Zen / OpenCode Go | `OPENCODE_API_KEY` (shared) | Chat Completions, Messages | 71 / 34 seeds + live discovery |
| Ollama (local) | none | native chat | live discovery only |
| Ollama Cloud | `OLLAMA_API_KEY` | native chat | 52 seeds + live discovery |

Not yet supported: xAI and Azure/OpenAI Responses-only models (no Responses
transport), Google Gemini (no `google-generative-ai` transport), AWS Bedrock
(SigV4 signing), `pi-vfs`/`pi-walker` internals, MCP/LSP/DAP.

Credentials resolve in order: explicit request key, `~/.ohgo/credentials.json`
(0600, written only when you save), environment, then parsed shell startup
files (read as text, never executed). `ohgo doctor` and `/providers` show
which source won — values are never printed.

Shell completion and reference docs ship with the binary:

```sh
ohgo completion bash > /etc/bash_completion.d/ohgo   # bash|zsh|fish|powershell
ohgo docs --dir ./ohgo-docs                          # man pages + markdown
```

## Run

```sh
export ANTHROPIC_API_KEY=sk-...   # or OPENAI_API_KEY, GROQ_API_KEY, ...
ohgo doctor                       # check the setup first; every failure names its fix
ohgo "summarise this repository"  # one-shot via `ohgo run "…"`
ohgo                              # interactive session (full-screen UI on a TTY)
ohgo --no-tui                     # plain REPL instead
ohgo --resume 12                  # continue session 12
ohgo models groq                  # list one provider's models
ohgo sessions show 12             # inspect a saved transcript
```

First run? `ohgo doctor` tells you what is missing — usually one environment
variable — and nothing else fails silently because of it.

## What it does

Reads, edits, and runs code in your repository through eleven tools
(`read write edit bash wait glob grep ls webfetch todo task`), with session
persistence, context compaction, subagent delegation (`--max-depth`),
a wall-clock run budget (`--deadline`), and per-tool approval prompts.

In-session commands: `/help /commands /clear /model [provider/id] /models /sessions /jobs /theme [dark|light] /connect /disconnect [provider] /providers /status /usage /doctor /diff [target] /retry /compact /quit`. The REPL and the full-screen UI share the same backend, so every command means the same thing in both.
Mid-session `/model` resolves through the same catalog as `ohgo models`,
so a listed model is always switchable. `/commands` (or Tab on a `/`-line)
opens a searchable command palette; every row runs the command it names.

`/diff` opens an interactive diff inspector: a hierarchical changed-file tree
with `A`/`M`/`D` badges, a `+X -Y` stats bar, and per-file hunks with
word-level highlighting. It fetches only the file list and counts on open and
loads each file's hunks on demand (cached), so a large changeset opens
instantly. `/diff` compares the working tree against `HEAD`, `/diff <target>`
compares it against a revision, and `/diff <base>..<target>` compares two
revisions. Keys: `tab` focus · `j`/`k` move · `enter` open · `/` filter ·
`esc` close.

## Terminal workspace

The full-screen UI is one adaptive workbench: header (identity, runtime
phase, model, connection), conversation, composer, status line
(cwd, session, context %, measured usage). Layout follows the terminal size
(`WindowSizeMsg` is a first-class input; a resize re-renders but never
starts work or drops history):

- **Wide (≥100 cols):** full metadata plus keyboard hints.
- **Medium (70–99):** reduced metadata, full conversation.
- **Narrow (<70):** conversation first; secondary metadata collapses before
  readability does. Footers stay one row.
- **Mini (<40 cols or <8 rows):** a size notice instead of overlapping
  regions. Input and history are preserved; enlarge to resume.

On an empty conversation the workspace shows a short welcome — identity, one
invitation to describe a task, and at most three examples — as chrome, never
as a transcript entry: it cannot enter the session log, it disappears the
moment real content arrives, it never reappears over a resumed transcript, and
`/clear` brings it back. Narrow terminals drop the examples and keep the
introduction plus a `/help` pointer. The header owns identity, model, and
runtime phase; the status line owns workspace context (cwd, session, context
%, measured usage) and never repeats them. Short content is padded below the
transcript so the composer stays pinned to the bottom of the screen.

Shortcuts that are shown always work: `enter` send (queues while busy),
`alt+enter`/`ctrl+j` insert a break, `↑` recalls history on a single line,
`pgup`/`pgdn` scroll, `esc` cancels a turn (never quits), `ctrl+c` cancels
when busy, clears a non-empty draft, or points at the exit chords when idle
(never quits), `ctrl+d` on an empty composer quits. Approval
dialogs default to the conservative choice; closing, cancelling, or resizing
never approves. Widths are measured in terminal cells (CJK/emoji/ANSI
aware), long URLs and tool output wrap and bound rather than overflow.

The `/model` picker lists the merged catalog (bundled plus live-cached
rows) with the active model marked, provider scope, result count, and
freshness (`live`, `cached`, `bundled`, `refreshing…`). Search is
multiword and case-insensitive across name, id, provider, and metadata
(`openrouter`, `free`, `deepseek v4 free`, `provider:openrouter`);
`free` matches verified zero-price rows, falling back to name text only
when pricing is unknown — unknown prices are never rendered as free.
Picker keys: printable input including space searches, `↑↓` move,
`PgUp`/`PgDn` page, `Home`/`End` jump, `←→` move the query cursor,
`enter` selects the highlighted canonical id, `esc` clears the query
first and closes second, `ctrl+r` refreshes the live catalog in the
background. A stale snapshot refreshes automatically on open; failures keep
cached rows with a reason, and a slower older refresh never overwrites a
newer one.

## Configuration

`~/.ohgo/settings.json` (project `.ohgo/settings.json` merges over it):

```json
{
  "model": "groq/llama-3.1-8b-instant",
  "maxTokens": 32000,
  "theme": "dark",
  "approvals": { "bash": "deny", "write": "allow" }
}
```

`"allow"` never asks, `"deny"` never runs; anything else (or absent) prompts
each time. `ohgo init` writes `model` and `theme`; `/theme` updates `theme`
in place.

Precedence for the model: `--model` flag, then settings, then `OHGO_MODEL`,
then the catalog default (`anthropic/claude-sonnet-4-5`). An unknown name is
an error everywhere — a silent fallback would send your conversation to a
provider you never chose.

`OHGO_HISTORY_FILE` overrides the plain-REPL history location
(`~/.ohgo/history` by default; empty disables it).

## Architecture

See [`ARCHITECTURE.md`](ARCHITECTURE.md) for the layer map, turn lifecycle,
concurrency rules, error taxonomy, storage, transports, and performance
notes. Port invariants live in [`MEMORY.md`](MEMORY.md); the incremental plan
in [`PORT.md`](PORT.md); status in [`PROGRESS.md`](PROGRESS.md).

Key properties:

- **The loop never touches HTTP.** All transport goes through `ai.StreamFn`,
  the seam every test drives.
- **Model facts are data.** `catalog.Resolve(provider/id)` is the only source;
  no code branches on model id strings.
- **Containment is structural.** Tool paths resolve inside the working root in
  one place, so no call site can forget the check.
- **Isolation is structural.** A subagent at the depth cap is built without a
  `task` tool, so it cannot delegate however prompted.
- **Budgets are explicit.** Completion budget defaults to the model's ceiling;
  the run is bounded by wall-clock deadline, and expiry returns completed
  turns rather than an error.

## Tests

```sh
make check   # build + vet + gofmt + full test suite
make race    # same, with the race detector
make bench   # glob/grep/agent/ai benchmarks
```

`cmd/ohgo/integration_test.go` drives a full tool call through the real
transport and filesystem against an `httptest` provider; `TestWireRequestCarries…`
tests assert budgets and endpoints on the actual request body, because unit
tests once missed a duplicated budget block.
