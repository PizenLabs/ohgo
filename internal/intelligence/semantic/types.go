// Package semantic implements Language Server Protocol observations.
//
// It is the semantic intelligence layer: it asks an editor-grade language
// service what a declaration actually resolves to, what references it, and
// what diagnostics the service reports. It is not a source parser (that is
// intelligence/ast), it does not own symbol identity, and it never applies
// edits: rename, code actions, workspace/applyEdit, and
// workspace/executeCommand are deliberately unsupported here. A semantic
// observation is evidence, not authority, and BUILD remains the only mutation
// path.
package semantic

import (
	"encoding/json"
	"net/url"
	"path/filepath"
	"strings"
)

// Protocol method names used by this client.
const (
	MethodInitialize        = "initialize"
	MethodInitialized       = "initialized"
	MethodShutdown          = "shutdown"
	MethodExit              = "exit"
	MethodCancelRequest     = "$/cancelRequest"
	MethodDidOpen           = "textDocument/didOpen"
	MethodDidChange         = "textDocument/didChange"
	MethodDidClose          = "textDocument/didClose"
	MethodDidSave           = "textDocument/didSave"
	MethodDefinition        = "textDocument/definition"
	MethodReferences        = "textDocument/references"
	MethodDocumentSymbol    = "textDocument/documentSymbol"
	MethodWorkspaceSymbol   = "workspace/symbol"
	MethodHover             = "textDocument/hover"
	MethodPrepareCallHier   = "textDocument/prepareCallHierarchy"
	MethodIncomingCalls     = "callHierarchy/incomingCalls"
	MethodOutgoingCalls     = "callHierarchy/outgoingCalls"
	MethodDiagnostic        = "textDocument/diagnostic"
	MethodPublishDiagnostic = "textDocument/publishDiagnostics"
	MethodApplyEdit         = "workspace/applyEdit"
	MethodConfiguration     = "workspace/configuration"
	MethodRegisterCapabilty = "client/registerCapability"
)

// Position is a zero-based LSP position. Character is UTF-16 code units.
type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// Range is one LSP range.
type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

// Location is one resolved source location.
type Location struct {
	URI   string `json:"uri"`
	Range Range  `json:"range"`
}

// SymbolKind is the LSP declaration kind.
type SymbolKind int

// Symbol is one normalized document or workspace symbol.
type Symbol struct {
	Name      string
	Detail    string
	Kind      SymbolKind
	Container string
	// Location is the declaring range. URI is empty for a workspace symbol
	// only when the server omitted it, which is reported as an issue.
	URI   string
	Range Range
}

// TextDocumentIdentifier is one document reference.
type TextDocumentIdentifier struct {
	URI string `json:"uri"`
}

// VersionedTextDocumentIdentifier adds the document version used by
// synchronization.
type VersionedTextDocumentIdentifier struct {
	URI     string `json:"uri"`
	Version int    `json:"version"`
}

// DocumentItem is one opened document.
type DocumentItem struct {
	URI        string `json:"uri"`
	LanguageID string `json:"languageId"`
	Version    int    `json:"version"`
	Text       string `json:"text"`
}

// PositionParams is a position request body.
type PositionParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
}

// ReferenceParams adds the include-declaration flag.
type ReferenceParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
	Context      struct {
		IncludeDeclaration bool `json:"includeDeclaration"`
	} `json:"context"`
}

// Hover is one hover answer. Contents is server-defined markup and is returned
// as bounded text by the caller.
type Hover struct {
	Contents json.RawMessage `json:"contents"`
	Range    *Range          `json:"range,omitempty"`
}

// CallHierarchyItem is one callable declaration.
type CallHierarchyItem struct {
	Name           string          `json:"name"`
	Kind           int             `json:"kind"`
	Detail         string          `json:"detail,omitempty"`
	URI            string          `json:"uri"`
	Range          Range           `json:"range"`
	SelectionRange Range           `json:"selectionRange"`
	Data           json.RawMessage `json:"data,omitempty"`
}

// IncomingCall is one caller of a call-hierarchy item.
type IncomingCall struct {
	From       CallHierarchyItem `json:"from"`
	FromRanges []Range           `json:"fromRanges"`
}

// OutgoingCall is one callee of a call-hierarchy item.
type OutgoingCall struct {
	To         CallHierarchyItem `json:"to"`
	FromRanges []Range           `json:"fromRanges"`
}

// Diagnostic is one language-service diagnostic.
type Diagnostic struct {
	Range    Range           `json:"range"`
	Severity int             `json:"severity,omitempty"`
	Code     json.RawMessage `json:"code,omitempty"`
	Source   string          `json:"source,omitempty"`
	Message  string          `json:"message"`
}

// PublishDiagnostics is the push-diagnostics payload.
type PublishDiagnostics struct {
	URI         string       `json:"uri"`
	Version     int          `json:"version,omitempty"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}

// DocumentSymbol is the structured document-symbol result.
type DocumentSymbol struct {
	Name           string           `json:"name"`
	Detail         string           `json:"detail,omitempty"`
	Kind           SymbolKind       `json:"kind"`
	Range          Range            `json:"range"`
	SelectionRange Range            `json:"selectionRange"`
	Children       []DocumentSymbol `json:"children,omitempty"`
}

// SymbolInformation is the flat workspace-symbol result.
type SymbolInformation struct {
	Name          string     `json:"name"`
	Kind          SymbolKind `json:"kind"`
	Location      Location   `json:"location"`
	ContainerName string     `json:"containerName,omitempty"`
}

// serverCapabilities is the subset of initialize.result.capabilities this
// client negotiates. Providers are raw because LSP allows true or an options
// object.
type serverCapabilities struct {
	TextDocumentSync   json.RawMessage `json:"textDocumentSync"`
	DefinitionProvider json.RawMessage `json:"definitionProvider"`
	ReferencesProvider json.RawMessage `json:"referencesProvider"`
	DocumentSymbol     json.RawMessage `json:"documentSymbolProvider"`
	WorkspaceSymbol    json.RawMessage `json:"workspaceSymbolProvider"`
	HoverProvider      json.RawMessage `json:"hoverProvider"`
	CallHierarchy      json.RawMessage `json:"callHierarchyProvider"`
	TypeHierarchy      json.RawMessage `json:"typeHierarchyProvider"`
	DiagnosticProvider json.RawMessage `json:"diagnosticProvider"`
}

// Capabilities is the negotiated, client-facing capability set.
type Capabilities struct {
	Definition       bool
	References       bool
	DocumentSymbols  bool
	WorkspaceSymbols bool
	Hover            bool
	CallHierarchy    bool
	TypeHierarchy    bool
	PullDiagnostics  bool
	// SyncKind is 0 (none), 1 (full), or 2 (incremental).
	SyncKind int
}

// Supports reports whether a method may be requested.
func (c Capabilities) Supports(method string) bool {
	switch method {
	case MethodDefinition:
		return c.Definition
	case MethodReferences:
		return c.References
	case MethodDocumentSymbol:
		return c.DocumentSymbols
	case MethodWorkspaceSymbol:
		return c.WorkspaceSymbols
	case MethodHover:
		return c.Hover
	case MethodPrepareCallHier, MethodIncomingCalls, MethodOutgoingCalls:
		return c.CallHierarchy
	case MethodDiagnostic:
		return c.PullDiagnostics
	}
	return false
}

// enabled reports whether an LSP provider field is active. LSP uses true or an
// options object; null and false are unavailable.
func enabled(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return trimmed != "" && trimmed != "null" && trimmed != "false"
}

// syncKind decodes textDocumentSync, which may be a number or an object.
func syncKind(raw json.RawMessage) int {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return 0
	}
	var number int
	if err := json.Unmarshal(raw, &number); err == nil {
		return number
	}
	var options struct {
		Change int `json:"change"`
	}
	if err := json.Unmarshal(raw, &options); err == nil {
		return options.Change
	}
	return 0
}

// FileURI converts an absolute path to a file:// URI.
func FileURI(path string) string {
	absolute, err := filepath.Abs(path)
	if err != nil {
		absolute = path
	}
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(absolute)}).String()
}

// PathFromURI converts a file:// URI to a filesystem path. It returns false for
// a non-file scheme rather than guessing.
func PathFromURI(uri string) (string, bool) {
	parsed, err := url.Parse(uri)
	if err != nil || parsed.Scheme != "file" {
		return "", false
	}
	return filepath.FromSlash(parsed.Path), true
}
