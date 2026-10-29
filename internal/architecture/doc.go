// Package architecture is the machine-checked half of OHGO's architecture
// governance.
//
// It contains no production code and exports nothing. Its only job is to fail
// `go test ./...` when a package crosses a boundary that OHGO declares
// permanent. The rules below are the executable form of
// OHGO_ARCHITECTURE_RULES.md; the manifest table is the executable form of
// OHGO_ARCHITECTURE_MAP.md. Keeping them as ordinary Go tests (rather than
// prose or a linter plugin) means they need no extra tooling, run everywhere
// the suite runs, and fail with the importer, the import, and the rule named.
package architecture
