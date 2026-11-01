# Pull request

## What
<!-- One-sentence summary of the behavior change. -->

## Why
<!-- Link the issue (`Fixes #...`) or state the reason. -->

## How
<!-- Key design points. If the change alters a documented contract, name the canonical document you updated (`OHGO_ARCHITECTURE_*`, `docs/RELEASING.md`, ...). -->

## Checks
<!-- Must all be true before review. See CONTRIBUTING.md and AGENTS.md. -->
- [ ] `go build ./...`
- [ ] `go test ./...`
- [ ] `go vet ./...`
- [ ] `go test -race ./...`
- [ ] `gofmt -l .` is empty
- [ ] `golangci-lint run ./...` reports 0 issues
- [ ] Test defends one externally observable contract (no source-grepping, no passthrough assertions)
- [ ] No new dependency the standard library already covers
- [ ] No model/provider special-casing; no prompts in code outside `prompt/` or tool constructors
- [ ] The canonical document for any changed fact is updated in this PR, not a follow-up
- [ ] `CHANGELOG.md` has an `Unreleased` entry for user-visible changes
- [ ] Release PRs follow `docs/RELEASING.md` and add the matching `internal/update/changelog.json` entry
