package semantic

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"testing"
	"time"
)

// TestLSPHelperServer is the fake language server child process. It is a test,
// not a product surface: the parent test launches this same binary with
// OHGO_LSP_HELPER=1.
func TestLSPHelperServer(t *testing.T) {
	if os.Getenv("OHGO_LSP_HELPER") != "1" {
		t.Skip("helper process entry point")
	}
	mode := os.Getenv("OHGO_LSP_MODE")
	serve(bufio.NewReader(os.Stdin), os.Stdout, mode)
}

// serve implements the protocol subset the client tests exercise.
func serve(reader *bufio.Reader, writer io.Writer, mode string) {
	for {
		payload, err := readMessage(reader)
		if err != nil {
			return
		}
		var msg message
		if err := json.Unmarshal(payload, &msg); err != nil {
			return
		}
		switch msg.Method {
		case MethodInitialize:
			capabilities := map[string]any{
				"definitionProvider":     true,
				"referencesProvider":     true,
				"documentSymbolProvider": true,
				"hoverProvider":          false,
				"callHierarchyProvider":  true,
				"textDocumentSync":       1,
			}
			if mode == "noCapabilities" {
				capabilities = map[string]any{"hoverProvider": false}
			}
			writeResult(writer, msg.ID, map[string]any{
				"capabilities": capabilities,
				"serverInfo":   map[string]any{"name": "fake-lsp", "version": "1"},
			})
			if mode == "crash" {
				os.Exit(3)
			}
		case MethodInitialized:
			if mode == "applyEdit" {
				applyEditID := json.RawMessage("9001")
				writeRequest(writer, applyEditID, MethodApplyEdit, map[string]any{
					"edit": map[string]any{"changes": map[string]any{}},
				})
				response, ok := readUntilResponse(reader, applyEditID)
				if !ok {
					os.Exit(7)
				}
				if response.Error == nil {
					os.Exit(9) // the client applied or accepted an edit
				}
				os.Exit(0)
			}
		case MethodShutdown:
			writeResult(writer, msg.ID, nil)
		case MethodExit:
			return
		case MethodDefinition:
			if mode == "slow" {
				time.Sleep(2 * time.Second)
			}
			writeResult(writer, msg.ID, []Location{{
				URI:   "file:///def.go",
				Range: Range{Start: Position{Line: 3, Character: 4}, End: Position{Line: 3, Character: 9}},
			}})
		case MethodReferences:
			writeResult(writer, msg.ID, []Location{{
				URI:   "file:///ref.go",
				Range: Range{Start: Position{Line: 1, Character: 0}, End: Position{Line: 1, Character: 5}},
			}})
		case MethodDocumentSymbol:
			writeResult(writer, msg.ID, []DocumentSymbol{{
				Name:           "Handler",
				Kind:           12,
				Range:          Range{Start: Position{Line: 0}, End: Position{Line: 2}},
				SelectionRange: Range{Start: Position{Line: 1, Character: 5}, End: Position{Line: 1, Character: 12}},
				Children: []DocumentSymbol{{
					Name:           "Serve",
					Kind:           6,
					Range:          Range{Start: Position{Line: 2}, End: Position{Line: 2}},
					SelectionRange: Range{Start: Position{Line: 2, Character: 6}, End: Position{Line: 2, Character: 11}},
				}},
			}})
		case MethodHover:
			writeResult(writer, msg.ID, map[string]any{"contents": "should not be called"})
		case MethodPrepareCallHier:
			writeResult(writer, msg.ID, []CallHierarchyItem{{
				Name:           "Handler",
				Kind:           12,
				URI:            "file:///handler.go",
				Range:          Range{Start: Position{Line: 1}},
				SelectionRange: Range{Start: Position{Line: 1}},
			}})
		case MethodIncomingCalls:
			writeResult(writer, msg.ID, []IncomingCall{{
				From: CallHierarchyItem{Name: "main", URI: "file:///main.go"},
				FromRanges: []Range{{
					Start: Position{Line: 5}, End: Position{Line: 6},
				}},
			}})
		case MethodDidOpen:
			var params struct {
				TextDocument DocumentItem `json:"textDocument"`
			}
			_ = json.Unmarshal(msg.Params, &params)
			writeNotification(writer, MethodPublishDiagnostic, PublishDiagnostics{
				URI: params.TextDocument.URI,
				Diagnostics: []Diagnostic{{
					Range:    Range{Start: Position{Line: 0}, End: Position{Line: 1}},
					Severity: 2,
					Message:  "fake diagnostic",
				}},
			})
		default:
			if len(msg.ID) > 0 {
				writeError(writer, msg.ID, -32601, fmt.Sprintf("unsupported %s", msg.Method))
			}
		}
	}
}

// readUntilResponse consumes messages until the response with id arrives.
func readUntilResponse(reader *bufio.Reader, id json.RawMessage) (message, bool) {
	want := idKey(id)
	for {
		payload, err := readMessage(reader)
		if err != nil {
			return message{}, false
		}
		var msg message
		if err := json.Unmarshal(payload, &msg); err != nil {
			return message{}, false
		}
		if msg.Method == "" && idKey(msg.ID) == want {
			return msg, true
		}
	}
}

func writeResult(writer io.Writer, id json.RawMessage, result any) {
	encoded, err := json.Marshal(result)
	if err != nil {
		return
	}
	_ = writeEncoded(writer, message{JSONRPC: "2.0", ID: id, Result: encoded})
}

func writeError(writer io.Writer, id json.RawMessage, code int, text string) {
	_ = writeEncoded(writer, message{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: text}})
}

func writeNotification(writer io.Writer, method string, params any) {
	encoded, err := json.Marshal(params)
	if err != nil {
		return
	}
	_ = writeEncoded(writer, message{JSONRPC: "2.0", Method: method, Params: encoded})
}

func writeRequest(writer io.Writer, id json.RawMessage, method string, params any) {
	encoded, err := json.Marshal(params)
	if err != nil {
		return
	}
	_ = writeEncoded(writer, message{JSONRPC: "2.0", ID: id, Method: method, Params: encoded})
}

func writeEncoded(writer io.Writer, msg message) error {
	payload, err := encodeMessage(msg)
	if err != nil {
		return err
	}
	return writeMessage(writer, payload)
}
