# MCP Capability Layer

> **Scope:** the external MCP capability boundary and its authority path.
> **Canonical topology:** [`OHGO_ARCHITECTURE_MAP.md`](../../OHGO_ARCHITECTURE_MAP.md).
> **Permanent rules:** [`OHGO_ARCHITECTURE_RULES.md`](../../OHGO_ARCHITECTURE_RULES.md) §13–§15.
> **Runtime behaviour:** [`ARCHITECTURE.md`](ARCHITECTURE.md).

MCP is an **external capability protocol**. It is not an authority mechanism,
not a second execution loop, and not an agent. The authority path is unchanged:

```text
LLM proposal
    -> intent grounding
    -> capability admission
    -> authority gate
    -> execution plane
    -> evidence
    -> Evaluate()
```

MCP capabilities enter at the capability/admission boundary exactly like native
capabilities:

```text
native capability ---+
                     +--> admission -> authority -> execution
MCP capability ------+
```

There is no `LLM -> MCP -> execute` path and no MCP-specific authority gate.

## Capability model

Discovery normalizes every advertised item into `mcp.Capability`:

- stable identity `<server>:<capability>` for tools, namespace-qualified for
  prompts (`server:prompt/<name>`) and resources (`server:resource/<uri>`)
- server provenance (never flattened into a global namespace)
- kind: `tool`, `resource`, or `prompt`
- configured classification: `read`, `execute`, `interact`, `mutate`, `unknown`
- normalized authority: `READ`, `EXECUTE`, `MUTATE`, `EXTERNAL_SIDE_EFFECT`,
  `UNKNOWN`
- trust provenance: `local`, `external`, `untrusted`
- bounded, compiled input schema when one exists

`MUTATE` is a normalized class for native capabilities; every state-changing MCP
tool is an `EXTERNAL_SIDE_EFFECT`, because it acts outside the workspace. That
distinction keeps an external call from looking like repository mutation to
completion evaluation.

Classification is configuration metadata. It never grants authority. A tool
with no explicit entry is `unknown` and fails closed. No model call classifies a
tool.

## Authority

| Workspace | MCP read | MCP execute | MCP interact/mutate |
| --- | --- | --- | --- |
| ASK | allowed | denied | denied |
| FUZZ | allowed | approval | denied |
| BUILD | allowed | approval | approval |

`mcp_read` is read-tier and never reaches a mutating boundary. `mcp_call`
declares write as its conservative default, but an `execute`-class tool reports
`external.mcp.execute` and execution tier per call, which maps to the workspace
execution operation rather than repository write. An unresolved class stays at
the mutation boundary. Execute, interaction, and mutation calls also require
`allowMutation: true` on the server; the authority table is necessary but not
sufficient.

Intent grounding runs before the security boundary. An external
mutation/interaction call is a mutation proposal, so a request that did not ask
for a change is rejected before any approval prompt. An execute call is a
bounded process/external action, not a repository mutation.

Trust is provenance only. `trust: local` does not permit mutation; `trust:
external` is not refused for being external.

## Lifecycle and cache

Startup loads configuration only. A server connects on first discovery or call,
then reuses the client. Discovery snapshots are cached for `cacheTtlSeconds`
(default five minutes). `Refresh` bypasses the cache; `mcp_list` accepts
`refresh: true`. A failed refresh drops the snapshot rather than serving stale
metadata as current. A tool call validates against the cached schema, forces
one refresh if the tool is absent, and fails closed on an unusable schema.

## Transports

- `stdio`: newline-delimited JSON-RPC to a filtered child environment.
- `streamable-http`: one JSON-RPC POST per message, JSON or server-sent-event
  response, optional `Mcp-Session-Id`, optional header values named by
  environment variable.

Not implemented: standalone GET streams, resumability, and server-to-client
requests. A server request is refused, never executed by the client. In
particular, MCP elicitation is not supported in this phase: a request that needs
mid-call human input fails explicitly rather than opening a second approval UI.

## Execution and evidence

Every model-invoked exchange is charged to the root budget (external calls and
bytes) and bounded. Cancellation propagates to the transport; a stdio child is
killed so no orphan remains. Progress notifications are mapped into the existing
tool progress callback and never imply completion.

Tool results are bounded, structured where available, and wrapped as untrusted
data. Evidence records kind, provenance (`mcp:<server>`, tool/resource/prompt),
retrieval time, byte/item/text bounds, and a content hash. Prompt and resource
content is context, not instruction. MCP success is never completion evidence;
only `Evaluate()` can prove a task.

## UI and configuration

`/mcp` lists configured servers from cache. `/mcp inspect <server>` discovers
one, `/mcp refresh [server]` bypasses the cache. The view shows provenance,
name, kind, and authority class, never a secret. Disabling a server
(`"enabled": false`) removes its capabilities without changing native execution;
an MCP block with no servers is an explicit configuration failure.
