package semantic

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/PizenLabs/ohgo/internal/intelligence/wire"
)

// ErrTransport reports malformed or unterminated JSON-RPC framing. The
// framing parser is shared with the debug adapter transport.
var ErrTransport = wire.ErrProtocol

// message is one JSON-RPC 2.0 envelope.
type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// rpcError is the JSON-RPC error object.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("lsp error %d: %s", e.Code, e.Message) }

// writeMessage writes one Content-Length framed payload.
func writeMessage(w io.Writer, payload []byte) error {
	return wire.Write(w, payload)
}

// readMessage reads one Content-Length framed payload.
func readMessage(r *bufio.Reader) ([]byte, error) {
	return wire.Read(r)
}

// encodeMessage marshals one message.
func encodeMessage(msg message) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(msg); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buffer.Bytes(), "\n"), nil
}
