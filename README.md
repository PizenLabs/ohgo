<p align="center">
  <img src="https://raw.githubusercontent.com/PizenLabs/onpic/refs/heads/main/ohgo/ohgo.png" width="15%" alt="izen" />
</p>

<h1 align="center">ohgo</h1>

<p align="center">
  <strong>A coding agent with a controlled runtime.</strong><br />
  Reads, edits, and runs code in your terminal — with explicit authority, <br> bounded budgets, and evidence-based completion.
</p>

<p align="center">
  <a href="https://github.com/PizenLabs/ohgo/actions/workflows/ci.yml"><img src="https://github.com/PizenLabs/ohgo/actions/workflows/ci.yml/badge.svg" alt="CI" /></a>
  <a href="https://codecov.io/gh/PizenLabs/ohgo"><img src="https://codecov.io/gh/PizenLabs/ohgo/branch/main/graph/badge.svg" alt="Coverage" /></a>
  <img src="https://img.shields.io/badge/Go-1.27%2B-00ADD8?logo=go&logoColor=white" alt="Go 1.27+" />
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-Apache--2.0-blue.svg" alt="Apache-2.0" /></a>
  <a href="https://github.com/PizenLabs/ohgo/discussions"><img src="https://img.shields.io/badge/PRs-welcome-brightgreen.svg" alt="PRs welcome" /></a>
</p>

> **Status:** pre-1.0 and under active development. The CLI, configuration, and
> session formats may change between minor releases. User-visible changes are
> recorded in [`CHANGELOG.md`](CHANGELOG.md).

---

## Contents

- [Why ohgo](#why-ohgo)
- [Features](#features)
- [Install](#install)
- [Quick start](#quick-start)
- [Usage](#usage)
- [Workspaces and authority](#workspaces-and-authority)
- [Providers](#providers)
- [Terminal workspace](#terminal-workspace)
- [Completion and exit status](#completion-and-exit-status)
- [Configuration](#configuration)
- [External knowledge](#external-knowledge)
- [Security model](#security-model)
- [Architecture](#architecture)
- [Development](#development)
- [Contributing](#contributing)
- [Security policy](#security-policy)
- [Code of conduct](#code-of-conduct)
- [License](#license)

## Why ohgo

Most coding agents trade control for convenience: they run whatever the model
asks for, spend whatever the provider bills, and stop whenever the model says
it is done. ohgo is built around the opposite contract.

- **Authority is explicit.** `ask`, `fuzz`, and `build` are runtime boundaries,
  not UI modes. The model cannot grant itself permission to mutate.
- **Budgets are finite.** Turns, model calls, tool calls, delegated children,
  background jobs, external I/O, and wall-clock time are all bounded.
- **Completion is evidence-based.** A run ends with a verdict derived from
  observed results, never from the model's claim of success.
- **The transport is a seam.** The agent loop never touches HTTP, so every
  layer can be tested against a real request body instead of a mock.
- **The binary is static.** Pure Go, zero cgo, no C toolchain, no shared
  libraries at runtime.

## Features

- **A focused tool set** — `read`, `write`, `edit`, `delete`, `bash`, `wait`,
  `glob`, `grep`, `ls`, `symbols`, `graph`, `refs`, `webfetch`, `todo`, `task`,
  and `artifact` when a session is attached.
- **Repository intelligence** — `symbols`, `graph`, and `refs` share one Go
  AST index. Reference and call queries report `ambiguous`, `truncated`,
  `unsupported`, and `unknown` explicitly instead of guessing.
- **Sessions that survive** — transcripts are stored in a pure-Go SQLite
  database, and a resumed session restores its own workspace, project, and
  effective model.
- **Subagent delegation** — `--max-depth` bounds the tree. A child at the depth
  cap is constructed without a `task` tool, so it cannot delegate regardless of
  what the prompt says.
- **Context compaction** — long conversations are compacted under the same
  budget and persistence rules as the rest of the run.
- **Two interfaces, one backend** — a full-screen terminal workspace and a
  plain line REPL; every slash command means the same thing in both.
- **A complete provider catalog** — bundled model facts plus live discovery,
  with prices, context windows, and capabilities resolved as data.
- **Bounded external knowledge** — opt-in search, MCP, and browser tools
  wrapped as untrusted data, with provenance and byte-level limits.
- **Recovery** — safe undo and redo for workspace mutations.

## Install

### Prebuilt release

```sh
OHGO_VERSION=v0.1.0 sh install.sh
```

The script selects the tarball for your OS and architecture, verifies nothing
is guessed about the install prefix, installs to `$PREFIX/bin` (default
`/usr/local/bin`), and runs `ohgo version` as a smoke test.

### From source

```sh
git clone https://github.com/PizenLabs/ohgo.git
cd ohgo
make build          # CGO_ENABLED=0, static binary at ./ohgo
./ohgo --help
```

Requires **Go 1.27+**. No C toolchain: `CGO_ENABLED=0 go build ./...` succeeds
and the dependency graph contains zero C packages.

### Install into your user prefix

```sh
make install                    # ~/.local/bin, first on a typical PATH
sudo make install PREFIX=/usr/local
```

`DESTDIR` is supported for packagers.

### Go install

```sh
go install github.com/PizenLabs/ohgo/cmd/ohgo@latest
```

### Docker

```sh
make docker
docker run -it -v "$PWD:/work" ohgo:dev
```

The image is built `CGO_ENABLED=0` and runs as a non-root user on a distroless
static base.

### Shell completion and reference docs

```sh
ohgo completion bash > /etc/bash_completion.d/ohgo   # bash|zsh|fish|powershell
ohgo docs --dir ./ohgo-docs                          # man pages + markdown
```

## Quick start

```sh
ohgo init          # writes ~/.ohgo/settings.json (never overwrites without --force)
ohgo doctor        # every failure names its fix
ohgo               # interactive session (full-screen UI on a TTY)
```

Set one provider credential and start working:

```sh
export ANTHROPIC_API_KEY=sk-...
ohgo doctor                       # check the setup first
ohgo run "summarise this repository"
ohgo                              # interactive session
```

A missing credential is the most common first-run failure; `ohgo doctor`
reports which source won without ever printing the secret.

## Usage

```sh
ohgo                              # interactive session
ohgo run "fix the failing test"   # one-shot, prints a verdict
ohgo --no-tui                     # plain line REPL
ohgo --resume 12                  # continue session 12
ohgo models groq                  # list one provider's models
ohgo sessions                     # list saved sessions
ohgo sessions show 12             # inspect a saved transcript
ohgo version
```

### Global flags

| Flag | Purpose |
| --- | --- |
| `-m, --model provider/id` | Select a model, e.g. `anthropic/claude-sonnet-4-5` |
| `--workspace ask\|fuzz\|build` | Execution authority (default `build`) |
| `-d, --dir path` | Working directory |
| `--resume id` | Resume a saved session |
| `--no-tui` | Force the line REPL |
| `--plain` | No colour, no full-screen UI |
| `--no-tools` | Run without any tools |
| `-y, --yes` | Auto-approve every tool call |
| `--reasoning` | Enable extended thinking |
| `--effort level` | Reasoning effort: `minimal`, `low`, `medium`, `high` |
| `--max-depth n` | Subagent nesting depth, counting this agent (1 disables delegation) |
| `--max-turns n` | Maximum model turns per run |
| `--max-model-calls n` | Tree-wide provider requests per run |
| `--max-tool-calls n` | Tree-wide tool executions per run |
| `--max-children n` | Tree-wide delegated executions per run |
| `--max-background-jobs n` | Tree-wide background jobs per run |
| `--deadline duration` | Wall-clock budget for the whole run, e.g. `10m` |
| `--mouse select\|report` | Pointer handling in the full-screen UI |
| `--live` | Output-first mode; the terminal owns scrolling and selection |

### In-session commands

```text
/ask /fuzz /build                      switch execution authority
/permissions                           choose the execution authority
/help /commands                        command & help center
/clear /new [name]                     fresh context or session
/model [provider/id] /models           model picker
/sessions /resume                      manage or resume sessions
/jobs                                  inspect background jobs
/task [status|list|show|resume|cancel|accept]
/audit                                 explain the last context projection
/theme [dark|light]                    show or switch the palette
/connect /disconnect [provider]        provider credentials
/status /usage /info                    workspace, cost, docs
/doctor /diff [target]                  setup diagnosis, diff inspector
/mcp [list|inspect|refresh]             MCP servers and capabilities
/undo /redo                             revert or restore the last agent edit
/retry /compact /quit
```

The REPL and the full-screen UI share the same backend, so every command means
the same thing in both. `/task` manages durable task continuity, which exists
only in the full-screen UI; the plain REPL reports it as unavailable.

### Model picker

`/model` (or `ohgo models`) lists the merged catalog — bundled rows plus
live-cached rows — with the active model marked, provider scope, result count,
and freshness (`live`, `cached`, `bundled`, `refreshing…`).

Search is multiword and case-insensitive across name, id, provider, and
metadata (`openrouter`, `free`, `deepseek v4 free`, `provider:openrouter`).
`free` matches verified zero-price rows, falling back to name text only when
pricing is unknown; unknown prices are never rendered as free.

Keys: printable input including space searches · `↑↓` move · `PgUp`/`PgDn`
page · `Home`/`End` jump · `←→` move the query cursor · `enter` selects the
highlighted canonical id · `esc` clears the query first and closes second ·
`ctrl+r` refreshes the live catalog in the background. A stale snapshot
refreshes automatically on open; failures keep cached rows with a reason, and a
slower older refresh never overwrites a newer one.

`/diff` opens an interactive diff inspector: a hierarchical changed-file tree
with `A`/`M`/`D` badges, a `+X -Y` stats bar, and per-file hunks with
word-level highlighting. It fetches only the file list and counts on open and
loads each file's hunks on demand, so a large changeset opens instantly.
`/diff` compares the working tree against `HEAD`, `/diff <target>` compares it
against a revision, and `/diff <base>..<target>` compares two revisions.
Keys: `tab` focus · `j`/`k` move · `enter` open · `/` filter · `esc` close.

## Workspaces and authority

The three workspaces are runtime authority boundaries. The authoritative path
is `workspace → capability policy → tool admission → execution`:

| Workspace | Authority | Can read | Can execute | Can mutate |
| --- | --- | --- | --- | --- |
| `ask` | Understand | yes | no | no |
| `fuzz` | Discover | yes | yes | no |
| `build` | Change | yes | yes | yes |

Production installs `Config.Authorize` as the **only** admission path. Every
call is evaluated before execution: `DENY` is terminal, `APPROVE` requires a
fresh confirming evaluation, and `AUTO` executes directly. A forbidden call is
refused outright rather than offered to the user. The UI may display the
workspace; it cannot grant capability.

Tools may refine their capability per invocation. `bash` classifies a command
as a mutation when it sees a real redirect, a mutating program, or a mutating
git subcommand, so `fuzz` cannot use the shell as a back door. The classifier
is a strong default, not a sandbox; an interpreter one-liner that writes a
file is a documented residual.

`/model`, `/permissions`, and session resume all go through the same resolved
configuration. A resumed session restores its own snapshot rather than
silently following a changed configuration.

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

The bundled catalog contains **379 model facts across 13 providers**. Model
facts are data: `catalog.Resolve(provider/id)` is the only source, and no code
branches on a model id string. An unknown model name is an error everywhere —
a silent fallback would send your conversation to a provider you never chose.

Not yet supported: xAI and Azure/OpenAI Responses-only models (no Responses
transport), Google Gemini (no `google-generative-ai` transport), AWS Bedrock
(SigV4 signing), `pi-vfs`/`pi-walker` internals, LSP/DAP, and MCP features
beyond bounded stdio plus the streamable-HTTP request/response core (standalone
GET streams, resumability, and server-to-client requests are not implemented).

Credentials resolve in order: explicit request key, `~/.ohgo/credentials.json`
(0600, written only when you save), environment, then parsed shell startup
files (read as text, never executed). `ohgo doctor` and `/usage` show which
source won — values are never printed.

## Terminal workspace

The full-screen UI is one adaptive workbench: header (identity, runtime phase,
model, connection), conversation, composer, and a status line (cwd, session,
context %, measured usage). Layout follows the terminal size; a resize
re-renders but never starts work or drops history.

- **Wide (≥100 cols):** full metadata plus keyboard hints.
- **Medium (70–99):** reduced metadata, full conversation.
- **Narrow (<70):** conversation first; secondary metadata collapses before
  readability does. Footers stay one row.
- **Mini (<40 cols or <8 rows):** a size notice instead of overlapping
  regions. Input and history are preserved; enlarge to resume.

The transcript keeps stable block identities, computes visual rows at the
current width, and slices only the rows on screen. Scrolling never rewrites
terminal scrollback and never moves the cursor into history. New output follows
the bottom while you are at the live edge; scrolling up detaches and pins the
block you are reading, so streaming does not steal the viewport. `end` (or
scrolling back down) reattaches.

Pointer behaviour follows the `--mouse` setting:

- **`select`** (default outside tmux) — the terminal keeps drag-select and
  copy. The wheel arrives through alternate-scroll mode (DEC 1007).
- **`report`** (default inside tmux) — the application owns the pointer. Drag
  selects logical rows; click expands an expandable tool block; alt-click
  copies that block's canonical content; releasing a drag copies via OSC 52.
  Copy payloads are logical text — never ANSI, cursor controls, or padding. A
  terminal that ignores OSC 52 silently does nothing; `OHGO_CLIPBOARD=off`
  disables the transport explicitly.

Widths are measured in terminal cells (CJK/emoji/ANSI aware); long URLs and
tool output wrap and bound rather than overflow. Expanding a block (`ctrl+o`
for the latest activity, or a click in reporting mode) changes presentation
only; it never reruns a tool, reads the repository, or changes authority.

Shortcuts that are shown always work: `enter` send (queues while busy),
`alt+enter`/`ctrl+j` insert a break, `↑` recalls history on a single line,
`pgup`/`pgdn` scroll, `home` jumps to the transcript top while the composer is
idle, `end` returns to the live edge, `esc` cancels a turn (never quits),
`ctrl+c` cancels when busy, clears a non-empty draft, or points at the exit
chords when idle, `ctrl+d` on an empty composer quits. Approval dialogs default
to the conservative choice; closing, cancelling, or resizing never approves.

On an empty conversation the workspace shows a short welcome — identity, one
invitation to describe a task, and at most three examples — as chrome, never as
a transcript entry: it cannot enter the session log, it disappears the moment
real content arrives, it never reappears over a resumed transcript, and
`/clear` brings it back.

`--live` is the separate output-first mode: it writes sealed transcript rows to
the terminal's normal buffer, so terminal scrollback, native selection, and pipe
behaviour remain the terminal's job. It never requests a mouse mode or attempts
historical expansion.

## Completion and exit status

Completion is decided from observed evidence, not from the model stopping.
`ohgo run` prints one verdict and exits accordingly:

| Verdict | Exit | Meaning |
| --- | --- | --- |
| `COMPLETED` | 0 | The requested contract was satisfied by observed evidence |
| `NEEDS_REVIEW` | 2 | The run stopped without enough evidence (missing execution, empty answer, unverified change) |
| `PAUSED` | 3 | A budget, deadline, or turn limit stopped the run before a verdict |
| `FAILED` | 1 | Execution, provider, or authority failure |
| `CANCELLED` | 130 | The process was interrupted |

The plain REPL prints the same verdict and keeps reading. The full-screen UI
persists the task contract and its pre-run target evidence; a `NEEDS_REVIEW`
task stays open until a later turn supplies evidence for that persisted
contract, or the user explicitly runs `/task cancel` (abandon) or `/task
accept` (close without claiming completion). Background `wait` reports a
timed-out or non-zero job as an error, and only a successful recognised check
is verification evidence.

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
each time. `ohgo init` writes `model` and `theme`; `/theme` updates `theme` in
place.

Precedence for the model: `--model` flag, then settings, then `OHGO_MODEL`,
then the catalog default (`anthropic/claude-sonnet-4-5`). An unknown name is an
error everywhere — a silent fallback would send your conversation to a provider
you never chose.

`OHGO_HISTORY_FILE` overrides the plain-REPL history location
(`~/.ohgo/history` by default; empty disables it).

Machine-level paths live under `~/.ohgo`; project identity and project-local
metadata live under `<root>/.ohgo`. The one creation authority for
`<root>/.ohgo` is the startup `Ensure` path: it decides the Git, worker, and
project name first and mutates last, so a cancelled startup creates nothing.

## External knowledge

Three bounded external capabilities are opt-in. A missing configuration block
means the tool is not offered. External content is untrusted data, never
authority, verification, or completion evidence.

**Search** (`search`) uses an explicit provider list. Credentials are named
environment variables; the value never appears in settings, results, or logs.

```json
{
  "search": {
    "defaultProvider": "brave",
    "providers": [
      {
        "name": "brave",
        "kind": "brave",
        "endpoint": "https://api.search.brave.com/res/v1/web/search",
        "apiKeyEnv": "BRAVE_API_KEY",
        "maxResults": 8,
        "maxBytes": 262144,
        "retries": 1
      }
    ]
  }
}
```

`kind` is `brave` or `tavily`. A query, result page, snippet, and response body
are bounded. Each result carries title, source host, URL, retrieval time,
provider, and a request identity. A retry is a separate admitted call.

**MCP** (`mcp_list`, `mcp_read`, `mcp_call`) is the external capability
boundary. It speaks bounded newline-delimited JSON-RPC to a configured stdio
server, or JSON-RPC over HTTP POST (JSON or server-sent events) to a
`streamable-http` endpoint. Discovery lists advertised capabilities but grants
nothing: every tool must be classified in configuration. Discovery follows
`nextCursor`, so paginated servers are read completely, and it publishes the
server's `initialize` instructions plus a compact input schema (required fields,
types, enums, defaults and bounds) for every tool. Use `schema: "full"` for
property descriptions or `schema: "none"` to omit them.

Every capability has the stable identity `<server>:<capability>`
(for example `github:search_code`). Prompts and resources are namespace
qualified (`server:prompt/<name>`, `server:resource/<uri>`) because a server
may advertise a tool and a prompt with the same name. The server prefix is
provenance, not decoration: two servers may advertise the same tool name, and
one is never presented as the other. Configured MCP capabilities stay in the
candidate tool surface for ordinary code questions; they are not treated as
optional web knowledge.

```json
{
  "mcp": {
    "servers": [
      {
        "name": "notes",
        "transport": "stdio",
        "command": "/usr/local/bin/notes-mcp",
        "args": ["--stdio"],
        "dir": "/path/to/project",
        "env": ["NOTES_TOKEN"],
        "trust": "local",
        "tools": { "search_notes": "read", "run_query": "execute", "create_note": "mutate" },
        "allowMutation": true,
        "cacheTtlSeconds": 300,
        "maxBytes": 262144,
        "maxItems": 32,
        "maxTextChars": 12000,
        "idleTimeoutSeconds": 120
      },
      {
        "name": "remote",
        "transport": "streamable-http",
        "url": "https://mcp.example.com/rpc",
        "headerEnv": { "Authorization": "REMOTE_MCP_TOKEN" },
        "trust": "external",
        "tools": { "search": "read" }
      }
    ]
  }
}
```

Classifications are `read`, `execute`, `interact`, `mutate`, or `unknown`; a
missing tool is `unknown` and fails closed. They map to normalized authority
classes: `READ`, `EXECUTE`, and `EXTERNAL_SIDE_EFFECT`. `mcp_read` accepts only
`read` tools and prompt/resource reads. `mcp_call` accepts `execute`, `interact`,
or `mutate` with `allowMutation`; mutation and interaction additionally require
BUILD authority, while execution follows the workspace's execution authority
(ASK cannot execute, FUZZ and BUILD can after approval). `mcp_call` prompts for
approval unless the user explicitly configured auto-approval or passed `--yes`.
The classification is configuration metadata: it never
grants authority by itself, and a `local` server gains nothing from its trust
label.

Capability snapshots are cached for `cacheTtlSeconds` (default five minutes).
`mcp_list` with `refresh: true` (or `/mcp refresh`) bypasses the cache; a tool
missing from a fresh snapshot forces one refresh, and an expired schema is
refreshed before execution. `mcp_read` also renders MCP **prompts** as bounded,
untrusted context with prompt provenance; a prompt never bypasses intent
grounding, authority, or approval.

The model never chooses the server command, server process arguments, URL, or
environment; only the named environment variables and header environment names
are passed to the server. The child starts in the workspace root unless `dir`
overrides it, so repository-aware servers see the project the agent is working
in. Binary content is never copied into model context.

`/mcp` shows the configured servers without connecting. `/mcp inspect <server>`
discovers one server, and `/mcp refresh [server]` bypasses the cache. The view
shows trust, transport, capability name, kind, and authority class; it never
shows a request header value or a secret.

> MCP capabilities do not receive authority merely by being configured.
> MCP output is untrusted external data and cannot redefine user intent.
> MCP success is never completion evidence; `Evaluate()` still owns PROVEN.

For [gry](https://github.com/PizenLabs/gry) code intelligence, let gry write the
entry and classifications:

```sh
gry install --client ohgo
```

That adds a `gry` server to `~/.ohgo/settings.json` with read tools classified
`read`, graph-mutating tools classified `mutate`, and `GRY_CACHE_DIR`/`GRY_DB`
passthrough. `gry uninstall --client ohgo` removes only that entry.

**Browser** (`browser`) is bounded GET read/navigation, not an automation
surface: no JavaScript, forms, clicks, or non-GET requests. A session has an
exact host policy, navigation and redirect limits, byte/character/link bounds,
a session deadline, and a bounded history. Loopback, private, link-local, and
metadata addresses are blocked by default.

```json
{
  "browser": {
    "allowedHosts": ["example.com", "docs.example.com"],
    "maxNavigations": 8,
    "maxRedirects": 3,
    "maxBytes": 1048576,
    "maxChars": 20000,
    "maxLinks": 20,
    "maxSessions": 4,
    "sessionTtlSeconds": 300
  }
}
```

Every external answer is wrapped as `[EXTERNAL KNOWLEDGE — UNTRUSTED DATA]`
with each content line prefixed, so injected text cannot forge a new section.
External calls and bytes consume the root run budget (`external_calls`,
`external_bytes`); providers also enforce per-request bounds. A cancelled call
publishes no evidence. The pre-existing `web_fetch` remains available and uses
the same untrusted envelope with `WEB_FETCH` provenance; it is a simple legacy
read, not a browser session.

## Security model

- **Path containment is structural.** Tool paths resolve inside the working
  root in one place, so no call site can forget the check.
- **Approval defaults are conservative.** Every tool declares its approval
  tier; an omitted tier prompts every time. Closing, cancelling, or resizing a
  dialog never approves.
- **Secrets stay in one owner.** Nothing outside `creds` reads a provider
  credential from the environment or shell startup files, and values are never
  printed.
- **External data is inert.** Search, MCP, and browser results are labelled,
  bounded, and cannot complete a task or grant authority.
- **Prompt injection is a threat, not a feature.** Model output is untrusted
  input; only the runtime decides whether an action is permitted.

See [`SECURITY.md`](SECURITY.md) for the vulnerability reporting process and
the supported-version policy.

## Architecture

See [`docs/architecture/ARCHITECTURE.md`](docs/architecture/ARCHITECTURE.md)
for the layer map, turn lifecycle, concurrency rules, error taxonomy, storage,
transports, and performance notes. The implemented authority model is described
in [`docs/architecture/WORKSPACE_AUTHORITY.md`](docs/architecture/WORKSPACE_AUTHORITY.md);
state and persistence in
[`docs/architecture/STATE_AND_PERSISTENCE.md`](docs/architecture/STATE_AND_PERSISTENCE.md).

Key properties:

- **The loop never touches HTTP.** All transport goes through `ai.StreamFn`,
  the seam every test drives.
- **Model facts are data.** `catalog.Resolve(provider/id)` is the only source;
  no code branches on model id strings.
- **Containment is structural.** Tool paths resolve inside the working root in
  one place, so no call site can forget the check.
- **Isolation is structural.** A subagent at the depth cap is built without a
  `task` tool, so it cannot delegate however it is prompted.
- **Budgets are explicit.** Completion budget defaults to the model's ceiling;
  the run is bounded by a wall-clock deadline, and expiry returns completed
  turns rather than an error.

The dependency arrows point one way: `catalog → ai` (type only), `ai → creds`
(secrets only), `tools → agent` (subagent spec only), `workspace → agent`
(tool admission only), and `cmd → everything`. A cycle here is a bug, not a
shortcut.

## Development

```sh
make build    # static binary
make test     # go test ./...
make vet      # go vet ./...
make fmt      # gofmt check
make check    # build + vet + gofmt + full test suite
make race     # same, with the race detector
make bench    # glob/grep/agent/ai benchmarks
make lint     # golangci-lint
```

All four checks must pass before a change is considered done:

```sh
go build ./...
go test ./...
go vet ./...
gofmt -l .          # must be empty
```

`go test ./...` must be full-suite safe: never leave global mutable state,
`os.Chdir`, or an un-restored `t.Setenv` that can leak into another file's
tests.

The integration suite (`cmd/ohgo/integration_test.go`) drives a full tool call
through the real transport and filesystem against an `httptest` provider.
`TestWireRequestCarries…` asserts budgets and endpoints on the actual request
body, because unit tests once missed a duplicated budget block.

Project conventions live in [`AGENTS.md`](AGENTS.md). In short: keep logic in a
testable seam, never write policy by model identity, keep prompts in `prompt/`
or the tool constructor, and centralise before adding a helper.

## Contributing

Contributions of every size are welcome — bug reports, documentation fixes,
tests, provider adapters, and new tools. Start with
[`CONTRIBUTING.md`](CONTRIBUTING.md) for the development setup, test rules, and
the provider/tool checklists.

- Ask questions and propose ideas in
  [GitHub Discussions](https://github.com/PizenLabs/ohgo/discussions).
- Report reproducible bugs with the
  [bug report template](https://github.com/PizenLabs/ohgo/issues/new?template=bug_report.yml).
- Request features with the
  [feature request template](https://github.com/PizenLabs/ohgo/issues/new?template=feature_request.yml).

## Security policy

Please do **not** open a public issue for a vulnerability. Report it privately
through [GitHub Security Advisories](https://github.com/PizenLabs/ohgo/security/advisories/new)
or by email to **contact@pizenlabs.dev**. See [`SECURITY.md`](SECURITY.md) for
scope, timelines, and safe-harbor terms.

## Code of conduct

This project follows the
[Contributor Covenant](https://www.contributor-covenant.org/version/2/1/code_of_conduct/).
Read [`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md) before participating. Report
concerns privately to **contact@pizenlabs.dev**.

## License

Licensed under the [Apache License, Version 2.0](LICENSE).

```
Copyright 2026 PizenLabs
```

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS, WITHOUT
WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
