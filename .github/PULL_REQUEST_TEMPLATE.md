# Pull request

## What
<!-- One-sentence summary of the behavior change. -->

## Why
<!-- Link the issue (`Fixes #...`) or state the reason. -->

## How
<!-- Key design points. Note any contract taken from the TypeScript original. -->

## Checks
<!-- Must all be true before review. See CONTRIBUTING.md and AGENTS.md. -->
- [ ] `go build ./...`
- [ ] `go test ./...`
- [ ] `go vet ./...`
- [ ] `gofmt -l .` is empty
- [ ] Test defends one externally observable contract (no source-grepping, no passthrough assertions)
- [ ] No new dependency the standard library already covers
- [ ] No model/provider special-casing; no prompts in code outside `prompt/` or tool constructors
- [ ] `CHANGELOG.md` has an `Unreleased` entry for user-visible changes
- [ ] Release PRs follow `docs/RELEASING.md` and add the matching `update/changelog.json` entry
