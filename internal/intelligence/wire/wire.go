// Package wire implements the Content-Length framed JSON transport shared by
// the Language Server Protocol and the Debug Adapter Protocol.
//
// Framing is identical in both protocols; keeping one parser means a malformed
// header or an oversized payload is rejected the same way for every provider.
// The payload is returned as bytes: protocol-specific decoding stays with the
// provider, so this package owns no schema.
package wire

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// MaxMessageBytes bounds one framed payload. A provider that exceeds it is
// misbehaving; the reader fails rather than growing without limit.
const MaxMessageBytes = 8 << 20

// ErrProtocol reports malformed, unterminated, or oversized framing.
var ErrProtocol = errors.New("wire: malformed message framing")

// Read reads one Content-Length framed payload.
//
// Extra headers (Content-Type) are accepted. io.EOF is returned unchanged when
// the stream ended before a message, so a caller can distinguish a clean close
// from a malformed frame.
func Read(reader *bufio.Reader) ([]byte, error) {
	length := -1
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) && line == "" {
				return nil, io.EOF
			}
			return nil, fmt.Errorf("%w: read header: %v", ErrProtocol, err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			return nil, fmt.Errorf("%w: malformed header %q", ErrProtocol, line)
		}
		if strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			parsed, parseErr := strconv.Atoi(strings.TrimSpace(value))
			if parseErr != nil || parsed < 0 {
				return nil, fmt.Errorf("%w: invalid Content-Length %q", ErrProtocol, value)
			}
			length = parsed
		}
	}
	if length < 0 {
		return nil, fmt.Errorf("%w: missing Content-Length", ErrProtocol)
	}
	if length > MaxMessageBytes {
		return nil, fmt.Errorf("%w: payload %d exceeds %d bytes", ErrProtocol, length, MaxMessageBytes)
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return nil, fmt.Errorf("%w: read payload: %v", ErrProtocol, err)
	}
	return payload, nil
}

// Write writes one Content-Length framed payload.
func Write(writer io.Writer, payload []byte) error {
	if len(payload) > MaxMessageBytes {
		return fmt.Errorf("%w: payload %d exceeds %d bytes", ErrProtocol, len(payload), MaxMessageBytes)
	}
	if _, err := fmt.Fprintf(writer, "Content-Length: %d\r\n\r\n", len(payload)); err != nil {
		return err
	}
	_, err := writer.Write(payload)
	return err
}
