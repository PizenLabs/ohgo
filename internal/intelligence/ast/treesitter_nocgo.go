//go:build !cgo

package ast

// treeSitterAdapters is empty without cgo.
//
// The runtime keeps the Go adapter and reports every other supported extension
// as unsupported in this build. It never silently falls back to a lexical
// approximation and calls it structure.
func treeSitterAdapters() []Adapter { return nil }
