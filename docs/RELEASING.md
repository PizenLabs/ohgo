# Releasing ohgo

This is the edit contract for cutting a new ohgo release. It exists because a
version appears in more than one document, and those copies have different
jobs:

- the **Git tag** is the version authority;
- `CHANGELOG.md` is the human-authored history;
- `update/changelog.json` is the release content embedded in the binary for the
  one-shot What's New card;
- `README.md`, `SECURITY.md`, and `docs/` describe behaviour, not the build
  version, and change only when their contract changes.

Do not treat any one of these as a substitute for another.

## 1. Decide the version

Use semantic versioning and tag it with a leading `v`:

```text
vMAJOR.MINOR.PATCH
```

The tag is the single version authority. `Makefile` derives `VERSION` with
`git describe`, and the release workflow passes that value into the binary with
`-X main.version=...`; do not hand-edit the default `version` variable in
`cmd/ohgo/main.go` for a release.

Before tagging:

- [ ] The release branch is `main` and the worktree is clean.
- [ ] CI is green.
- [ ] `go build ./...`, `go test ./...`, `go vet ./...`, and `gofmt -l .` pass.

## 2. Files that change for a release

| File | When it changes | Why |
| --- | --- | --- |
| `CHANGELOG.md` | every public release | Human-readable release history |
| `update/changelog.json` | every public release | Local What's New content embedded in the binary |
| `README.md` | install, update, or user-facing behaviour changes | User documentation |
| `docs/architecture/STATE_AND_PERSISTENCE.md` | a new persistent file or state boundary appears | State ownership contract |
| `docs/architecture/ARCHITECTURE.md` | an architectural contract changes | System contract |
| `SECURITY.md` | supported-version policy changes | Security policy |
| `CONTRIBUTING.md` | the contributor or release process changes | Contributor guidance |
| `AGENTS.md` | engineering rules or workflows change | Agent-facing conventions |
| `docs/RELEASING.md` | this checklist changes | Release process |

Do **not** bump a version string in source just to make the binary report the
new release. The release build already does that from the tag.

## 3. Edit the two release records

### 3.1 `CHANGELOG.md`

Move the user-visible entries from `## [Unreleased]` into a new released
section, oldest release at the bottom:

```markdown
## [v0.8.3] - 2026-10-29

### Added

- Complete update lifecycle.

### Fixed

- ...

## [Unreleased]
```

Rules:

- Released sections are immutable. Never rewrite an older release's history.
- Keep `## [Unreleased]` present at the top, even when empty.
- Write what a user observes, not a file-by-file implementation summary.

### 3.2 `update/changelog.json`

Add an entry whose key is exactly the release tag. This file is embedded into
the binary with `//go:embed`; it is **not** fetched from GitHub. A release with
no entry here still installs and restarts correctly, but its What's New card
says no notes are available.

```json
{
  "schema": 1,
  "releases": {
    "v0.8.3": {
      "title": "What's new in v0.8.3",
      "changes": [
        {
          "id": "update-lifecycle",
          "type": "added",
          "title": "Complete update lifecycle",
          "description": "A successful update is acknowledged once on the next launch."
        }
      ]
    }
  }
}
```

Rules:

- `schema` is `1`.
- The release key matches the tag, including the leading `v`.
- `id` is stable and unique within the release. Use the same id for the same
  change if it is mentioned again in later notes.
- Allowed `type` values: `added`, `improved`, `fixed`, `changed`.
- `title` is required. `description` is optional.
- Keep the list short. The card shows at most six titles; the full record stays
  in the binary and repository.
- Do not add a Markdown parser, remote release-note lookup, or a new dependency
  for this file. The What's New path must remain local and offline.

Validate the file before committing:

```sh
python3 -m json.tool update/changelog.json >/dev/null
go test ./update/ -run 'Changelog|Transition'
```

The tests also prove the embedded file parses, every change has an id and a
supported type, and release lookup accepts both `v1.2.3` and `1.2.3`.

## 4. Edit documentation only when its contract changed

Version numbers are not sprinkled through the documentation. Update prose only
when the release changes what a document promises:

- **Persistent state** — add or rename rows in
  `docs/architecture/STATE_AND_PERSISTENCE.md`. For example,
  `~/.ohgo/update-transition.json` is lifecycle truth, while
  `~/.ohgo/update.json` is a derived release-observation cache.
- **Architecture** — update `docs/architecture/ARCHITECTURE.md` when a
  boundary, lifecycle, or layer changes.
- **User instructions** — update `README.md` when a command, install path, or
  update behaviour changes. Keep example versions clearly illustrative; do not
  claim they are the current release.
- **Security policy** — update `SECURITY.md` when supported versions change.
- **Contributor workflow** — update `CONTRIBUTING.md` and `AGENTS.md` when the
  process or engineering rules change.

If a release adds a new machine-state file, document its owner, location,
lifetime, and whether it is authoritative or derived. A persisted file without
an ownership row is an undocumented boundary.

## 5. Run the release checks

```sh
gofmt -l .          # must print nothing
go build ./...
go vet ./...
go test ./...
go test -race ./update/ ./cmd/ohgo/ ./tui/
```

Then build the release artifacts from the clean release tree. Pass the version
explicitly because the tag does not exist yet:

```sh
make release VERSION=vX.Y.Z
```

Inspect the result:

```sh
ls -l dist/
cat dist/sha256sums.txt
tar -tzf dist/ohgo-vX.Y.Z-darwin-arm64.tar.gz
```

The tarballs must contain the `ohgo` binary, `README.md`, and `LICENSE`, and
`sha256sums.txt` must name every archive the release publishes.

## 6. Pre-publish verification

No network is needed for this phase. It proves the artifact and the embedded
release content agree before the tag exists:

```sh
make build VERSION=vX.Y.Z
./ohgo version
```

`ohgo version` must print `vX.Y.Z`. Confirm `update/changelog.json` has the same
key and that `go test ./update/ -run 'Changelog|Transition'` passes.

A complete updater smoke test needs a published release because the updater
resolves GitHub releases. Use a staging repository/release when you need that
verification before the public tag; otherwise run the lifecycle checklist in
section 8 immediately after publishing and fix forward with a patch release if
it fails.

The governing rules remain: an available update is information; `/update` is
authority; the post-update notice exists only because an explicit update
succeeded.

## 7. Commit, tag, publish

Commit the release records and documentation edits, then create and push an
annotated tag:

```sh
git tag -a vX.Y.Z -m "ohgo vX.Y.Z"
git push origin main
git push origin vX.Y.Z
```

The tag starts `.github/workflows/release.yml`, which runs `make release`,
publishes the four tarballs plus `sha256sums.txt`, and generates GitHub release
notes. `CHANGELOG.md` remains the human-authored record; generated release notes
are not a replacement for it.

## 8. Post-release verification

Run the remote update path from the previous release, using a temporary `HOME`
so real machine state is untouched:

1. Start the previous release.
2. Run `/update` or `ohgo update`.
3. Confirm the process stays on the old binary and shows the restart-required
   screen; press Enter to exit.
4. Start the new binary and confirm the centered What's New card appears once.
5. Press Enter and confirm the notice is acknowledged.
6. Start again and confirm the card does not reappear.
7. Confirm `ohgo version`, `ohgo models`, and `ohgo update --check` do not
   consume a pending notice.
8. Confirm a failed update and a rollback never present the card.

Then check the published surface:

- [ ] The GitHub release has all four archives and `sha256sums.txt`.
- [ ] `install.sh` installs the new tag and `ohgo version` reports it.
- [ ] `ohgo update --check` sees the new tag from the previous release.
- [ ] Updating from the previous release produces exactly one pending notice.
- [ ] The next interactive launch shows What's New and acknowledges it once.
- [ ] `update/changelog.json` and `CHANGELOG.md` describe the same release.
