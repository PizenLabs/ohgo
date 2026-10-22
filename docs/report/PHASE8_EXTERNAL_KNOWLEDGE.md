# Phase 8 — External Knowledge Foundation

- **Date:** 2026-10-23
- **Phase:** 8 — Search, MCP, Browser
- **Mode:** source-first audit, capability contract first, then Search, MCP,
  Browser, adversarial validation, closure
- **Tree:** working tree based on `d48474d` plus the Phase 8 changes. No commit
  was made. The tree also contains unrelated in-progress TUI documentation
  modal work; every result below was measured on the combined tree.
- **Decision frame:** *Dynamic Path, Static Authority, Truthful State
  Transition*

## 1. Phase 8 scope

The internal runtime (Phases 3–7) is frozen. Phase 8 adds external knowledge
without widening authority, budget, evidence, verification, cancellation, or
completion:

- one shared capability boundary for external data;
- **Search** over configured HTTP providers;
- **MCP** over configured stdio servers, with discovery separated from
  authorization;
- **Browser** as bounded GET read/navigation, explicitly not an automation or
  mutation surface;
- provenance, bounded results, truthful failure classes, cancellation, and
  root-budget consumption for every capability;
- adversarial tests for injection, authority/budget escalation, secret
  requests, oversized responses, malformed protocol, and false completion.

External information is treated as **untrusted input**:

```text
External information ≠ truth
External information ≠ authority
External information ≠ verification
External information ≠ completion
```

## 2. Capability architecture

### 2.1 Source-first audit matrix

| Capability concern | Existing mechanism | Reuse | New mechanism required |
|---|---|---|---|
| authority | `agent.Authorizer` → `securityGate` → `security.Policy`; `workspace.Permits` | Yes, unchanged seam | Distinct external capabilities: `external.search`, `external.mcp.read`, `external.mcp.mutate`, `external.browser.read`; reserved `external.browser.mutate` |
| budget | Root-owned `budget.Budget`; loop admits one tool call | Yes, same instance | `external_calls` and `external_bytes` dimensions; atomic `AdmitN`; `Remaining`; per-request caps |
| cancellation | `context.Context`; loop checks before/after approval; tool checks | Yes | MCP process kill on cancel; post-response and pre-publication checks; no cancelled evidence |
| timeout | Root deadline; tool-specific contexts | Yes | Provider, MCP-exchange, and browser-request timeouts clamped by the root deadline |
| result size | `io.LimitReader`, schema validation, compaction | Partial | `knowledge.BoundText`, byte/char/item/depth/link/session/redirect caps, explicit truncation reasons |
| evidence | `agent.Result` flags; `ToolOutcome.Authorization`; completion evaluator | Yes | `knowledge.Evidence`/`Provenance`; evidence kinds; no `Mutates`, `Executes`, or `Verifies` from external content |
| persistence | Session transcript; task checkpoint | Not extended | External audit details are bounded and transient; external text is never durable authoritative memory |
| failure classification | `agent.Result.IsError`; `budget.ExhaustedError` | Partial | `knowledge.Failure` classes: timeout, cancelled, unavailable, rate limited, invalid request, empty, partial, malformed, authentication, response limit, unsupported, unknown, not configured, protocol, mutation denied, remote tool error |
| audit/logging | `ToolProgress`, `Result.Details`, transcript | Partial | Details carry capability, provider/server, operation, request identity, duration, bytes, budget snapshot, evidence, and failure class; no credentials, cookies, or raw provider payloads |
| user approval | `security.OutcomeApprove` + `agent.Approval` + TUI gate | Yes | External reads are medium risk and prompt; external mutation is high risk, BUILD-only, and separately marked `ExternalMutation` |

### 2.2 Control flow

```text
HUMAN
  ↓
INTENT / CONTRACT
  ↓
agent.Loop
  ↓
MODEL PROPOSAL (tool call)
  ↓
securityGate.Evaluate
  ├─ authority: workspace.Permits(capability)
  ├─ risk: LOW / MEDIUM / HIGH
  └─ grant / human approval
  ↓
budget.Admit(tool_calls)
  ↓
external tool
  ├─ budget.Admit(external_calls)
  ├─ bounded request / exchange
  ├─ budget.AdmitN(external_bytes)
  └─ bounded, provenance-preserving result
  ↓
agent.Turn.Results (informational evidence only)
  ↓
agent.EvaluateTaskCompletion
  ↓
COMPLETED / NEEDS_REVIEW / PAUSED
```

The model never chooses a provider endpoint, an MCP command/environment, or a
mutation classification. It proposes arguments; the runtime classifies and
admits them.

### 2.3 Shared package boundary

`knowledge/` is data-only. It defines:

- `Capability` and `Operation` (`read`, `interact`, `mutate`);
- `Provenance` (capability, source, locator, provider, retrieval time, request
  identity);
- `EvidenceKind` (`SEARCH_RESULT`, `MCP_RESOURCE`, `MCP_TOOL_RESULT`,
  `BROWSER_PAGE`, `BROWSER_NAVIGATION`, `WEB_FETCH`);
- `Bound` (bytes, chars, items, truncated, reason);
- `Trust` (only `untrusted_external`);
- `Failure` classes and HTTP classification;
- `BoundText`, `Envelope`, `ValidateStructure`, and `DecodeObject`.

It imports no runtime package that can authorize or complete work.

The conceptual boundary maps onto existing production types instead of a
parallel runtime:

| Conceptual concern | Existing type/seam |
|---|---|
| `CapabilityRequest` | `agent.Tool` + arguments; `cmd/ohgo.securityRequest` derives capability/scope/risk |
| `CapabilityAdmission` | `agent.Authorizer` → `securityGate` → `security.Policy`; root `budget.Budget` |
| `CapabilityExecution` | `Tool.Execute(agent.Context, args)` |
| `CapabilityResult` | `agent.Result` |
| `CapabilityEvidence` | `knowledge.Evidence` in `Result.Details` |
| `CapabilityFailure` | `knowledge.Failure`, `budget.ExhaustedError`, `Result.IsError` |
| `CapabilityCancellation` | `context.Context`; MCP process kill; post-read/pre-publish checks |

## 3. Search implementation

`search/` is a provider transport. `tools/search.go` is the capability.

- **Configuration:** `settings.search.providers[]` with `name`, explicit
  `kind` (`brave` or `tavily`), HTTPS `endpoint`, and `apiKeyEnv`. The key is
  read from the environment at request time; values never enter settings,
  results, or logs. `defaultProvider` is optional.
- **Request bounds:** query ≤ 512 runes, results ≤ 20 (default 8), response
  body ≤ 256 KiB default / 1 MiB ceiling, snippet ≤ 1,200 chars, provider
  timeout ≤ 60 s, retries ≤ 2.
- **Result model:** each hit preserves title, source host, URL (≤ 2,048
  chars), snippet, retrieval timestamp, provider, and request identity.
- **Failures:** timeout, cancelled, provider unavailable, rate limited, invalid
  request, empty result, partial result, malformed response, authentication
  failure, response limit, unsupported, not configured. Empty is `status:
  empty`, not an error; a skipped unusable hit is `partial`.
- **Retries:** only transient classes; each attempt is a separate
  `external_calls` admission and uses a bounded, context-aware backoff.
- **Output:** `[EXTERNAL KNOWLEDGE — UNTRUSTED DATA]` envelope, every content
  line prefixed with `| `, 32,000-character output cap.
- **Completion:** the tool sets no `Mutates`, `Executes`, or `Verifies` flags.
  A search result cannot complete a mutation or execution objective.

## 4. MCP implementation

`mcp/` is a bounded, newline-delimited JSON-RPC 2.0 stdio client.
`tools/mcp.go` exposes three capabilities:

| Tool | Purpose | Authority |
|---|---|---|
| `mcp_list` | Discover advertised tools/resources | `external.mcp.read`, risk MEDIUM, approval |
| `mcp_read` | Read a resource or call a `read`-classified tool | `external.mcp.read`, risk MEDIUM, approval |
| `mcp_call` | Call an `interact`/`mutate`-classified tool | `external.mcp.mutate`, risk HIGH, BUILD + approval |

- **Discovery ≠ authorization.** `mcp_list` returns descriptions and
  classifications; it grants nothing. A capability becomes callable only
  through the explicit classification map and the authority/approval gates.
- **Explicit classification.** `tools` is a configuration map of
  `read|interact|mutate|unknown`. A missing tool, an invalid value, or a typo
  is `unknown` and fails closed. `mcp_read` accepts only `read`;
  `mcp_call` accepts only `interact`/`mutate` with `allowMutation` true.
- **Schema validation.** Tool arguments are structure-bounded (depth, value
  count, encoded bytes) and validated against the server's `inputSchema` before
  `tools/call`. Missing/invalid schemas fail closed.
- **Response bounds.** One exchange is bounded by `maxBytes` (default 256 KiB,
  ceiling 1 MiB), including ignored notifications. Content is bounded by item
  count, total text chars, and binary omission. Truncation is explicit
  (`item_limit`, `text_limit`, `response_limit`, `output_limit`).
- **Mutation boundary.** `mcp_call` is exclusive, tier write, and reports
  `CapabilityExternalMCPMutate`. `security.Policy` requires BUILD, records
  `ExternalMutation`, and sets `Mutates=false`: the call is not workspace
  mutation evidence and cannot demand local verification.
- **Process lifetime.** Servers are started lazily, one in-flight exchange per
  server, filtered environment (only named variables), killed on cancellation
  or idle timeout, and restarted on the next call after a disconnect.
- **Protocol safety.** Server-initiated requests are refused, not executed;
  malformed JSON, disconnects, and over-limit lines become classified failures.
  Binary content is represented by a marker; base64 data never enters context.
- **Injection.** Text is wrapped by the same untrusted envelope. Fake system
  messages, tool instructions, authority/budget requests, and secret
  exfiltration requests remain data and cannot change runtime state.

## 5. Browser implementation

`tools/browser.go` and `tools/browser_html.go` implement bounded GET
read/navigation.

- **Explicit session state:** named sessions, current URL/page, bounded history,
  navigation count, creation time, deadline, host allowlist, and per-session
  cookie jar. The jar is never rendered or logged.
- **Operations:** `open`, `follow` (indexed link), `read`, `links`, `history`,
  `close`. No JavaScript, form submission, click, download, or non-GET method.
  `submit`/`click`/`interact`/`post`/`delete` fail closed with
  `unsupported_operation` and never reach the network.
- **Navigation bounds:** 8 navigations default / 32 ceiling per session; 3
  redirects default / 8 ceiling; 1 MiB response default / 4 MiB ceiling; 20,000
  text chars; 20 links; 4 sessions; 5-minute session TTL; 20-second request
  timeout. All are configurable within hard ceilings.
- **Host policy:** exact host allowlist; empty configuration means the host
  opened first. Redirects must stay in the session host policy. A follow only
  works for an allowed link. HTTPS-to-HTTP redirects are refused.
- **SSRF:** loopback, private, link-local, multicast, and unspecified addresses
  are blocked by a resolving dialer. The browser transport is direct-only
  (`Proxy = nil`), so a proxy cannot bypass the destination check. Loopback is
  enabled only by the test/local constructor flag, not user configuration.
- **Content isolation:** extracted text and links are wrapped in the untrusted
  envelope; links display `allowed: true|false`; output is capped at 32,000
  characters. Page text is never placed in a system prompt.

## 6. Authority matrix

| Capability | Read | Interaction | Mutation | Evidence | Completion |
|---|---:|---:|---:|---|---:|
| Search | YES | NO | NO | informational `SEARCH_RESULT` | NO |
| MCP discovery (`mcp_list`) | YES (metadata) | NO | NO | none (description only) | NO |
| MCP resource (`mcp_read resource`) | YES | NO | NO | informational `MCP_RESOURCE` | NO |
| MCP read tool (`mcp_read tool`) | YES | NO | NO | informational `MCP_TOOL_RESULT` | NO |
| MCP mutation tool (`mcp_call`) | NO | YES | YES (external) | explicit `MCP_TOOL_RESULT` + `ExternalMutation` | NO |
| Browser read/navigation (`open/follow/read/links/history`) | YES (GET) | Navigation YES | NO | `BROWSER_PAGE`, `BROWSER_NAVIGATION` | NO |
| Browser mutation | NO | NO (not implemented) | NO (not implemented) | — | NO |

Browser navigation is interaction with the *session* (current page, history,
navigation count), not with the remote service: it consumes
`external.browser.read` authority and never sends a non-GET request.

Workspace implementation:

| External capability | Workspace operation | ASK | FUZZ | BUILD | Risk |
|---|---|---:|---:|---:|---|
| `external.search` | `OpRead` | ✓ | ✓ | ✓ | MEDIUM → APPROVE |
| `external.mcp.read` | `OpRead` | ✓ | ✓ | ✓ | MEDIUM → APPROVE |
| `external.browser.read` | `OpRead` | ✓ | ✓ | ✓ | MEDIUM → APPROVE |
| `external.mcp.mutate` | `OpWrite` | ✗ | ✗ | ✓ | HIGH → APPROVE |
| `external.browser.mutate` (reserved) | `OpWrite` | ✗ | ✗ | ✓ | HIGH → APPROVE |

`Decision.Mutates` is false for every external capability.
`Decision.ExternalMutation` is true only for `mutate` requests. This is the
load-bearing separation that keeps external actions out of repository
completion evidence. As with local tools, an explicit `--yes` or a per-tool
settings policy can skip the prompt; workspace authority and DENY still win.

## 7. Budget model

The root grant remains authoritative. No separate external budget exists.

| Dimension | Unit | Derived default | Charged |
|---|---|---|---|
| `external_calls` | one attempt/exchange | `toolCalls` (default `turns*8`) | Search attempt, MCP RPC exchange, browser navigation |
| `external_bytes` | bytes | 16 MiB | Observed response bytes, after the per-request reader is clamped |

- `Budget.AdmitN` checks and charges atomically; a partial admission is
  impossible.
- `Budget.Remaining` lets a response reader clamp to the remaining aggregate
  bytes before reading. It is a hint, never a reservation; the post-read
  `AdmitN` is still the enforcement point.
- Search retries are separate calls. MCP initialization, discovery, schema
  lookup, and the call are separate exchanges. Browser `open`/`follow` are
  navigations; `read`/`links`/`history` are local snapshot reads and only
  consume the ordinary tool-call dimension.
- `agent.New`/`SetBudget(nil)`/direct `RunSubagent` fallbacks now install
  finite external dimensions, so an embedder cannot accidentally get an
  unmetered external tree.
- A refused admission consumes nothing and surfaces `Result.Exhausted` so the
  loop records a resource stop.

## 8. Cancellation model

- **Loop:** existing checks before and after authorization, and before charging
  a tool call.
- **Search:** before admission, before each attempt, after the provider
  response, and before evidence publication. Retry backoff observes `ctx`.
- **MCP:** each write and read is selected against `ctx`; cancellation kills the
  server process and closes the exchange. The next call starts a clean process.
  A cancelled exchange publishes no evidence and is not a remote tool error.
- **Browser:** the HTTP request inherits the parent context and a request
  deadline; after the body read, cancellation is checked again before charging
  bytes or publishing. A cancelled `open` stores no session; a cancelled
  `follow` does not advance the current page.
- **Result:** a cancelled call is `IsError`, `status: cancelled`, with no
  evidence and no completion flag. The cancellation tests fail if the
  cancellation result is changed to success.

## 9. Evidence and provenance model

Every successful external result carries `knowledge.Evidence` with:

```text
kind        SEARCH_RESULT | MCP_RESOURCE | MCP_TOOL_RESULT |
            BROWSER_PAGE | BROWSER_NAVIGATION
trust       untrusted_external (always)
provenance  capability, source, locator, provider/server,
            retrieved_at, request_id
bound       bytes, chars, items, truncated, reason
content     SHA-256 prefix for audit identity
```

- Provenance is mandatory; an unknown source is explicitly `unknown`, never
  omitted.
- Search publishes one evidence record per hit. Browser publishes a navigation
  record plus a page record for `open`/`follow`, and a page record for `read`.
  MCP publishes resource or tool-result evidence; discovery publishes no
  knowledge evidence. The pre-existing `web_fetch` also uses the untrusted
  envelope and `WEB_FETCH` evidence, but remains outside the Phase 8 browser
  session and external budget dimensions.
- Evidence is informational. It is never fed into completion as verification,
  and it cannot satisfy a mutation target.
- `Result.Details` also carries `capability`, `operation`, `request_id`,
  `duration_ms`, and a budget snapshot (`external_calls_used/left`,
  `external_bytes_used/left`). Credentials, cookies, headers, and raw payloads
  are excluded.

## 10. Security model

| Threat | Boundary |
|---|---|
| Prompt injection / fake system or tool instruction | Untrusted envelope with every line prefixed; tools return data only; no runtime state channel from content |
| Authority escalation | External capabilities are fixed by the tool and configuration; the model cannot select a capability or risk |
| Budget escalation | Root-owned dimensions; retries and exchanges charged individually; per-request caps |
| Fake MCP server request (`sampling`, `roots`, …) | Refused with JSON-RPC `-32601`; never executed |
| MCP tool mutation by misclassified read | Explicit classification map; missing/typo = `unknown`; `mcp_read` rejects non-read |
| MCP response flooding | Byte, item, text, and depth bounds; explicit truncation; process kill on over-limit line |
| MCP argument abuse | Structure bounds and server schema validation before `tools/call` |
| Secret exfiltration through MCP env | Only named environment variables are passed; values never logged |
| SSRF / cloud metadata | Loopback/private/link-local blocked by a resolving dialer; direct-only transport, no proxy |
| Redirect abuse | Redirect count, host policy, scheme policy, HTTPS downgrade refusal |
| Oversized page | Response byte cap, text cap, link cap, 32k output cap, explicit truncation |
| External claim of completion | Tools set no completion flags; external mutation decision is not workspace mutation |
| Credential leakage | Search stores an environment variable name only; browser excludes cookies/headers; MCP excludes env values |

The runtime cannot prevent a model from being persuaded by text. It can and
does prevent external text from directly changing authority, budget, task
intent, tool permissions, verification, or completion. `ohgo doctor` lists the
built tool names and reports a malformed external configuration as a failing
`external` check with the settings-file fix.

## 11. Prompt-injection tests

| Test | What it defends |
|---|---|
| `TestEnvelopeCannotBeClosedByContent` | Injected delimiter cannot forge a new transcript section |
| `TestSearchInjectionStaysInsideDataEnvelope` | Search snippets with `[SYSTEM]`/authority requests remain prefixed data |
| `TestMCPInjectionStaysInsideTheDataEnvelope` | MCP text with fake system/authority/budget/secret requests remains data |
| `TestBrowserInjectionStaysInsideDataEnvelope` | Page text cannot escape the untrusted block |
| `TestWebFetchUsesUntrustedEnvelope` | Legacy fetch text cannot escape the untrusted block |
| `TestMCPServerRequestIsRefusedNotExecuted` | Server-initiated `sampling/createMessage` is refused, not executed |
| `TestMCPReadBoundaryRefusesMutatingAndUnknownTools` | Read boundary rejects mutate/unknown classifications |
| `TestMCPMutationRequiresExplicitConfiguration` | Mutation needs explicit classification and `allowMutation` |
| `TestMCPResultBoundsTextAndOmitsBinary` | Text/items bounded; base64 payload absent |
| `TestMCPDiscoveryTruncationIsExplicit` | Discovery over the item bound is marked truncated |
| `TestExternalClaimCannotCompleteAMutationObjective` | A search claim cannot complete a repository mutation |
| `TestExternalMutationIsNotWorkspaceMutationEvidence` | External mutation decision is not repository evidence |
| `TestBrowserMutationOperationsFailClosed` | Browser interaction/mutation never reaches the network |
| `TestBrowserBlocksLoopbackByDefault` | Loopback/private SSRF is blocked |
| `TestBrowserTransportDisablesProxy` | A proxy cannot bypass destination checks |
| `TestBrowserRedirectLimitIsEnforced` | Redirect chain is bounded |
| `TestBrowserNavigationLimitIsEnforced` | Navigation budget is bounded |
| `TestFilteredEnvExcludesUnnamedSecrets` | MCP does not inherit unrelated credentials |
| `TestSearchRefusesOversizedResponse` | Search response over the byte cap fails truthfully |
| `TestMCPResponseLimitKillsTheExchange` | Over-limit MCP line kills the process |
| `TestMCPToolSchemaValidationHappensBeforeExecution` | Malformed arguments never reach `tools/call` |
| `TestMCPArgumentsAreStructurallyBounded` | Deep argument trees are refused |
| `TestSearchCancellationIsNotSuccessfulEvidence` | Cancellation publishes no evidence |
| `TestMCPCancellationPublishesNoEvidence` | Cancellation is not a successful MCP result |
| `TestBrowserCancellationPublishesNoEvidence` | Cancellation is not a successful page result |

## 12. Mutation boundary

- **Search:** read-only; no external mutation surface.
- **MCP:** classification is explicit configuration. `mcp_read` accepts only
  `read`; `mcp_call` accepts only `interact`/`mutate` with `allowMutation`, and
  the security gate additionally requires BUILD and approval. Unknown fails
  closed. A server request is never executed.
- **Browser:** no mutation surface exists. `submit`, `click`, `interact`,
  `post`, and `delete` return `unsupported_operation` without a network call.
- **Completion:** external mutation is `ExternalMutation=true`,
  `Mutates=false`. It cannot satisfy a repository target, demand local
  verification, or create `COMPLETED`.
- **Discovered but unclassified** is not callable. Discovery never widens
  authority.

## 13. Browser boundary

The Browser is a bounded HTTP text transport, not a general-purpose browser:

- GET only; no JavaScript engine, DOM interaction, forms, downloads, or auth
  profiles;
- exact host policy, with empty configuration meaning the first host only;
- redirect, navigation, byte, character, link, session-count, and TTL bounds;
- direct-only network path with private/loopback/link-local/metadata blocking;
- per-session cookie jar, never rendered or persisted;
- explicit named sessions; a stale session returns `timeout`, not a stale page;
- page text/links are untrusted data with `BROWSER_PAGE` and
  `BROWSER_NAVIGATION` evidence.

A GET request can have side effects on a poorly designed endpoint. This is
outside what the runtime can prove; the capability is approved as an external
read, has no authenticated profile, and cannot mutate through a documented
method.

## 14. Regression results

Commands run on the final tree:

```sh
go build ./...                                   # PASS
go vet ./...                                     # PASS
gofmt -l .                                       # empty
go test ./... -count=1                           # PASS (all packages)
go test -race ./... -count=1                     # PASS (all packages)
```

- `go test ./...` includes the Phase 1–7 suites (`agent`, `task`, `tui`,
  `cmd/ohgo`, `ast`, `security`, `session`, …); all passed.
- `agent`, `task`, `tui`, and `cmd/ohgo` completion/cancellation/budget tests
  remain green. No Phase 3–6 invariant was relaxed.
- New package coverage (statement): `knowledge` 62.9%, `search` 75.0%, `mcp`
  65.9%, `tools` 77.1%.
- The combined tree contains unrelated in-progress TUI documentation-modal
  work; it also passes its own suite. Phase 8 changes are isolated to the
  packages listed in this report plus the wiring/security/config seams.

## 15. Mutation-test results

Each mutation was applied to the final source, the named test was run, the
failure was observed, and the source was restored from a backup. All restored
suites are green.

| # | Mutation | Test result |
|---|---|---|
| 1 | `admitExternal` returns nil (external admission removed) | `TestSearchToolRefusesWithoutExternalBudget` **FAIL** — provider contacted, successful result returned |
| 2 | `chargeExternalBytes` returns nil (byte charging removed) | `TestSearchToolPublishesBoundedProvenanceAndChargesBudget` **FAIL** — `external bytes = 0` |
| 3 | `externalCancelledResult` returns success | `TestSearchCancellationIsNotSuccessfulEvidence`, `TestMCPCancellationPublishesNoEvidence`, `TestBrowserCancellationPublishesNoEvidence` **FAIL** — cancelled calls returned `IsError:false` |
| 4 | MCP read mode accepts every classification | `TestMCPReadBoundaryRefusesMutatingAndUnknownTools` **FAIL** — `send_tool` returned no error |
| 5 | `knowledge.Envelope` stops prefixing content lines | `TestEnvelopeCannotBeClosedByContent`, `TestBrowserInjectionStaysInsideDataEnvelope`, `TestMCPInjectionStaysInsideTheDataEnvelope` **FAIL** — injected delimiter escaped the data block |
| 6 | Search result reports `Mutates: true` | `TestSearchIntegrationAdmissionBudgetEvidenceCompletion` and `TestExternalClaimCannotCompleteAMutationObjective` **FAIL** — search claimed mutation evidence |
| 7 | `Policy` records external mutation as workspace `Mutates` | `TestMCPIntegrationMutationBoundaryAndCompletion`, `TestExternalMutationIsApprovedButNotWorkspaceMutation` **FAIL** — external mutation decision claimed workspace mutation |
| 8 | `browserRoutable` returns true for every address | `TestBrowserBlocksLoopbackByDefault` **FAIL** — loopback page was fetched |
| 9 | Missing MCP classification defaults to `read` | `TestDiscoverySeparatesDescriptionFromAuthority`, `TestMCPReadBoundaryRefusesMutatingAndUnknownTools` **FAIL** — unknown tool became readable |

These mutations cover the Phase 8 stop conditions: admission, budget, byte
accounting, cancellation, injection isolation, mutation classification,
external-vs-workspace mutation, SSRF, and discovery-vs-authorization.

## 16. Performance and resource results

Hard bounds are part of the contract, not tuning knobs:

| Capability | Bound |
|---|---|
| Search | 20 results, 1,200-char snippet, 256 KiB response default / 1 MiB ceiling, 32k output, ≤ 2 retries |
| MCP | 256 KiB exchange default / 1 MiB ceiling, 32 items, 12k text chars, depth 16, 256 KiB arguments, 2-minute default idle kill |
| Browser | 8 navigations / 32 ceiling, 3 redirects / 8 ceiling, 1 MiB body / 4 MiB ceiling, 20k text, 20 links, 4 sessions, 5-minute TTL |
| Budget | 16 MiB external bytes and `toolCalls` external calls per derived root grant |

Local bounding/rendering benchmarks (Darwin arm64, Apple M1 Pro, Go 1.27,
`-benchtime=200x`):

```text
BenchmarkBoundTextOneMiB-8              4,777,540 ns/op   209.31 MB/s
BenchmarkEnvelopeTenThousandLines-8       263,536 ns/op
BenchmarkExtractLinksBounded-8            926,046 ns/op   (1,000 anchors, 20 returned)
BenchmarkSearchPublishBoundedPage-8       173,861 ns/op   (20 hits)
BenchmarkParseToolResultBounded-8         684,984 ns/op   561.83 MB/s (32 × 12k items, bounded)
```

These measure the local bound/normalization/rendering path, not provider or
network latency. The point is that work is bounded by the configured caps, not
by what an external actor sends.

## 17. Accepted limitations

1. **MCP transport is stdio only.** HTTP/SSE/WebSocket MCP transports are not
   implemented; the client is newline-delimited JSON-RPC over a child process.
2. **MCP classification is human configuration.** A user who classifies a
   mutating tool as `read` defeats that tool's boundary; unknown/typo
   classifications fail closed, but configuration is trusted.
3. **MCP server processes are not sandboxed.** The command is
   human-configured, never model-chosen, and runs with the user's OS
   privileges. Only explicitly named environment variables are passed, but the
   server can read the user's files.
4. **Browser is an HTTP text extractor, not a JavaScript browser.** SPAs,
   client-rendered content, forms, downloads, auth profiles, and interaction
   are unavailable.
5. **A GET endpoint can have side effects.** Browser navigation is approved as
   an external read and uses no authenticated profile; the runtime cannot prove
   a GET endpoint is side-effect free.
6. **External audit details are transient.** `Result.Details` carries the
   structured record, but there is no durable external evidence journal or
   replay store. Session transcripts store model-visible text only.
7. **Search provider scope is Brave/Tavily wire formats.** There is no generic
   provider plugin, pagination, or result reranking; one bounded page is
   returned.
8. **External information is not truth.** Search/provider accuracy is not
   verified; an external claim can be wrong or stale and is still only data.
9. **JSON Schema subset.** MCP tool schemas are validated with the project's
   `jsonschema` subset; unsupported keywords are not a second implementation,
   while the hard structural/size bounds still apply.
10. **Browser proxy behavior is deliberately disabled.** Corporate proxy users
    must use a direct route; this preserves the SSRF destination check.
11. **MCP cancellation waits briefly for reaping.** `Kill` waits up to two
    seconds for the child; in-flight work is terminated at the OS process.
12. **No browser mutation, no cross-capability fusion.** There is no browser
    POST/form/click capability and no automatic combination of search/MCP/page
    evidence into a stronger claim.
13. **External byte allowance is a fixed derived default.** The root grant
    derives `external_calls` from the tool-call grant and `external_bytes` as
    16 MiB; Phase 8 has no dedicated CLI flag for those two dimensions.
14. **External content may itself contain secrets.** The runtime never adds
    provider credentials, cookies, authorization headers, or MCP environment
    values to context/logs, but it does not attempt semantic secret detection
    inside a page or MCP response; such content remains untrusted data.
15. **`web_fetch` remains a legacy simple fetch.** It now uses the untrusted
    envelope and `WEB_FETCH` provenance, but keeps its own 5 MiB/50k caps and
    consumes only the ordinary tool-call budget; it is not one of the three
    Phase 8 capabilities and has no browser session state.

## 18. Deferred capabilities

- MCP HTTP/SSE transports, OAuth, prompts, sampling, roots, subscriptions,
  pagination, and server sandboxing.
- Browser JavaScript execution, DOM interaction, forms, downloads, auth
  profiles, multi-page crawling, and browser mutation.
- Search provider plugins, pagination, caching, ranking, and cost accounting.
- A durable external evidence/audit journal and replay.
- Cross-capability evidence fusion and semantic verification of external
  claims.
- External mutation approval policies beyond BUILD + one-shot human approval.
- Per-domain egress policy, rate limiting across providers, and proxy-aware
  SSRF policy.

## 19. Final verdict

```text
PHASE 8 — VERDICT: PASS
```

Search, MCP, and Browser capabilities are implemented within explicit
authority, budget, cancellation, provenance, and evidence boundaries. External
knowledge remains untrusted and cannot independently authorize execution or
completion.

The accepted limitations above bound what the runtime claims: MCP is a bounded
stdio client with human-configured classification and server trust; Browser is
bounded GET read/navigation without JavaScript or mutation; external evidence
is informational and transient. None of these limitations permits an authority
bypass, a budget escape, a cancellation-as-success, secret leakage, or a false
completion on the tested paths.
