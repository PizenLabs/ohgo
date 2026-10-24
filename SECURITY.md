# Security Policy

## Supported versions

ohgo is pre-1.0. Security fixes land on the latest minor release and on `main`;
older releases do not receive backports. If you are running a commit older than
the latest release, reproduce the issue on `main` before reporting it.

| Version | Supported |
| --- | --- |
| `main` (latest commit) | Yes |
| Latest `0.1.x` release | Yes |
| Older releases | No |

## Reporting a vulnerability

**Do not open a public issue for a security vulnerability.**

Report privately using GitHub's
[private vulnerability reporting](https://github.com/PizenLabs/ohgo/security/advisories/new).
If you cannot use GitHub, email **contact@pizenlabs.dev** with the subject
`[ohgo security]` and encrypt sensitive material where possible.

Please include:

- The version or commit you tested (`ohgo version`, or `git rev-parse HEAD`).
- Your operating system, architecture, and terminal.
- The execution authority in use (`ask`, `fuzz`, or `build`).
- The provider and model, if relevant.
- A minimal reproduction: exact commands, configuration, and the observed
  result.
- The impact you believe is possible, and any proof-of-concept you have.
- Whether you have disclosed the issue elsewhere.

Do not include live credentials. Revoke any credential that appeared in a
transcript before sending it.

## What to expect

| Stage | Target |
| --- | --- |
| Acknowledge receipt | 3 business days |
| Initial assessment and severity | 7 business days |
| Fix or mitigation plan | 30 days for confirmed issues |
| Coordinated disclosure | 90 days after the report, or earlier by agreement |

We will keep you informed of progress, credit you in the advisory unless you
ask otherwise, and coordinate a disclosure date with you. If a fix takes longer
than 90 days, we will explain why and agree on a revised timeline rather than
publish silently.

## Scope

In scope — issues that cross a boundary the documentation claims to enforce:

- **Authority bypass** — reaching mutation from `ask` or `fuzz`, approving a
  call without user consent, or bypassing `Config.Authorize`.
- **Path containment escape** — reading or writing outside the working root
  through a tool path, symlink, or shell command.
- **Credential exposure** — a secret reaching model context, logs, session
  storage, an error message, or a rendered transcript.
- **Prompt-injection escalation** — untrusted external content or tool output
  that gains authority, forges evidence, or completes a task.
- **SSRF** — the browser tool reaching loopback, private, link-local, or cloud
  metadata addresses despite the host policy.
- **MCP command injection** — model-controlled command, arguments, or
  environment reaching a configured stdio server.
- **Budget or isolation escape** — exceeding the tree-wide model, tool, child,
  job, deadline, or external-I/O limits; a depth-capped subagent acquiring a
  `task` tool.
- **Session integrity** — a resumed session following changed configuration,
  or transcript data being attributed to the wrong session, project, or model.
- **Supply chain** — a dependency, build script, release artifact, or CI
  workflow that can be compromised to affect users.

Out of scope:

- The model producing incorrect, insecure, or low-quality code. That is a model
  behaviour question, not a runtime vulnerability.
- Prompt injection that only changes model output and does not cross an
  authority, evidence, or data boundary.
- Running ohgo with `--yes`, `--workspace build`, or an approval set to
  `"allow"` and then expecting the tool to refuse the action anyway. These
  settings intentionally grant authority.
- Vulnerabilities in third-party providers, MCP servers you configured, or
  websites you ask the browser to read.
- Denial of service through a deliberately unbounded local input that does not
  cross a documented budget.
- Issues that require an attacker to already have code execution or write
  access on the machine.

## Threat model

ohgo treats **model output as untrusted input**. The runtime, not the model,
decides whether an action is permitted. The security properties the project
intends to guarantee are:

1. A workspace determines what the agent is allowed to do, not how the terminal
   looks.
2. Tool paths resolve inside the working root in one place.
3. Secrets are read by one owner (`creds`) and never printed.
4. External content is inert data: bounded, labelled, and unable to grant
   authority or complete a task.
5. Every run is bounded by explicit turn, call, child, job, byte, and
   wall-clock limits.
6. Approval dialogs default to the conservative choice, and closing, cancelling,
   or resizing never approves.
7. A resumed session restores its own snapshot rather than following a changed
   configuration.

The shell capability classifier is a strong default, not a sandbox. An
interpreter one-liner that writes a file is a documented residual; use the
`ask` workspace or a container when that residual matters.

## Safe harbor

We will not pursue legal action against researchers who:

- Make a good-faith effort to avoid privacy violations, data destruction, and
  service interruption.
- Test only against their own installation, accounts, and repositories.
- Report the issue privately and give us a reasonable window to fix it before
  public disclosure.
- Do not exploit the issue beyond the minimum needed to demonstrate it.

## Recognition

With your permission, we will credit you in the GitHub Security Advisory and in
the release notes for the version that contains the fix.
