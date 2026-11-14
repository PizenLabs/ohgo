// Command fakeadapter is a deterministic DAP adapter that dials a client.
//
// It exists only for integration tests: it validates the dial-client transport
// and the runtime evidence path without requiring a debugger the host may not
// permit. It serves one session and exits.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"net"
	"os"

	"github.com/PizenLabs/ohgo/internal/intelligence/wire"
)

func main() {
	addr := flag.String("addr", "", "DAP client address")
	flag.Parse()
	conn, err := net.Dial("tcp", *addr)
	if err != nil {
		os.Exit(2)
	}
	defer func() { _ = conn.Close() }()
	reader := bufio.NewReader(conn)
	seq := 0
	event := func(name string) {
		seq++
		payload, _ := json.Marshal(map[string]any{"seq": seq, "type": "event", "event": name, "body": map[string]any{"threadId": 1}})
		_ = wire.Write(conn, payload)
	}
	reply := func(requestSeq int, command string, body any) {
		seq++
		message := map[string]any{"seq": seq, "type": "response", "request_seq": requestSeq, "command": command, "success": true}
		if body != nil {
			message["body"] = body
		}
		payload, _ := json.Marshal(message)
		_ = wire.Write(conn, payload)
	}
	for {
		payload, err := wire.Read(reader)
		if err != nil {
			return
		}
		var request struct {
			Seq     int    `json:"seq"`
			Type    string `json:"type"`
			Command string `json:"command"`
		}
		if json.Unmarshal(payload, &request) != nil || request.Type != "request" {
			continue
		}
		switch request.Command {
		case "initialize":
			reply(request.Seq, "initialize", map[string]any{"capabilities": map[string]any{"supportsConfigurationDoneRequest": true}})
		case "launch", "attach":
			event("initialized")
			reply(request.Seq, request.Command, nil)
		case "setBreakpoints":
			reply(request.Seq, "setBreakpoints", map[string]any{"breakpoints": []map[string]any{{"verified": true, "line": 11}}})
		case "configurationDone":
			reply(request.Seq, "configurationDone", nil)
			event("stopped")
		case "threads":
			reply(request.Seq, "threads", map[string]any{"threads": []map[string]any{{"id": 1, "name": "main"}}})
		case "stackTrace":
			reply(request.Seq, "stackTrace", map[string]any{"stackFrames": []map[string]any{{
				"id": 7, "name": "main.add", "line": 11, "column": 1,
				"source": map[string]any{"name": "main.go", "path": "/workspace/main.go"},
			}}})
		case "scopes":
			reply(request.Seq, "scopes", map[string]any{"scopes": []map[string]any{{"name": "Locals", "variablesReference": 100}}})
		case "variables":
			reply(request.Seq, "variables", map[string]any{"variables": []map[string]any{{"name": "a", "value": "2", "type": "int"}}})
		case "continue":
			reply(request.Seq, "continue", map[string]any{"allThreadsContinued": true})
			event("exited")
		case "disconnect", "terminate":
			reply(request.Seq, request.Command, nil)
			event("terminated")
			return
		default:
			reply(request.Seq, request.Command, map[string]any{})
		}
	}
}
