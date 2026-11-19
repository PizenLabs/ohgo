# OpenRouter integration

OpenRouter is one provider behind the shared Chat Completions transport. This
document covers application attribution, eligibility diagnostics, and model
availability; topology and transport ownership stay in
[`ARCHITECTURE.md`](ARCHITECTURE.md).

## Attribution identity

Every OpenRouter request carries the application identity:

```http
HTTP-Referer: https://ohgo.pizenlabs.dev/
X-OpenRouter-Title: OHGO
X-OpenRouter-Categories: cli-agent
```

The values are constants in `internal/provider/ai/openrouter.go`. They are
application identity, never prompts, session ids, workspace paths, repository
names, or credentials. The canonical title header is used instead of the
legacy `X-Title`; both set the same field, and sending both risks disagreement.

`cli-agent` is OpenRouter's documented category for terminal-based coding
assistants. OHGO is not an IDE extension, cloud agent, or general chat
application, and `agent-runtime` is not a recognized OpenRouter category —
unrecognized values are dropped without an error. No secondary category is
sent.

Attribution applies to inference and to OpenRouter discovery. The registry
keys it on the provider (`openrouter`) but never applies it to another provider
or custom endpoint.

## Website metadata is separate

The website's Next.js metadata (`../ohgo-agent/app/layout.tsx`) controls the
HTML title and description. It does not configure OpenRouter. In particular,
`category: "agent-runtime"` is website metadata, not an OpenRouter category.
OpenRouter receives only the request headers above.

OpenRouter's documentation exposes no header or public API for registering an
application description: the app record's description is set outside the
attribution headers. Treat that as a manual/dashboard or support action, not
something the runtime can send.

## Attribution is not eligibility

These states are independent and must not be inferred from one another:

1. Attribution configured — OHGO sends the headers.
2. Attribution observed — an app page exists at
   `https://openrouter.ai/apps?url=https%3A%2F%2Fohgo.pizenlabs.dev%2F`.
3. Application listed — the app appears publicly in the marketplace.
4. Agentic-harness eligibility — OpenRouter accepts the app for endpoints
   gated to recognized agentic harnesses.
5. Model endpoint available — the selected model or endpoint can actually
   serve the request.

Attribution headers identify the application; they do not grant eligibility.
OpenRouter evaluates the gate server-side. A successful ordinary completion
does not prove eligibility for a gated free endpoint, and a correctly
attributed request can still receive the gate's 403.

## Diagnosing the agentic-harness 403

OpenRouter reports the rejection in the response body. The transport preserves
the HTTP status, the provider error code, and `metadata.failed_routing_step`,
then classifies the specific routing step
`Gate Free Endpoints by Agentic Harness`:

```text
ai: provider returned HTTP 403: ...
OpenRouter blocked this request at "Gate Free Endpoints by Agentic Harness": ...
OHGO sends app attribution (...), but attribution is not eligibility.
Check the app listing at https://openrouter.ai/apps?url=...
```

Other 403 responses — disabled keys, spend limits, region blocks — keep their
status and body without this classification. The diagnostic never claims that
OHGO is approved, and the runtime never substitutes another model or moves
from a free endpoint to a paid one.

If the gate persists, confirm the app listing and category above, then contact
OpenRouter support. Eligibility is an OpenRouter policy decision, not an OHGO
runtime capability.

## Model availability

Model lifecycle is provider data. A model can be listed in `/models` and still
be retired, restricted, or unavailable at request time. OHGO does not treat
catalog presence as proof of availability, and it does not maintain a
hard-coded per-model eligibility table.

For `thinkingmachines/inkling-2-small:free`, OpenRouter documents the endpoint
as available only to agentic harnesses. The paid
`thinkingmachines/inkling-2-small` variant is a distinct endpoint and is never
selected automatically.
