package runtime

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/PizenLabs/ohgo/internal/intelligence/wire"
)

// TestDAPHelperAdapter is the fake debug adapter child process used by the
// runtime tests. It implements the protocol subset the manager exercises.
func TestDAPHelperAdapter(t *testing.T) {
	if os.Getenv("OHGO_DAP_HELPER") != "1" {
		t.Skip("helper process entry point")
	}
	mode := os.Getenv("OHGO_DAP_MODE")
	serveAdapter(bufio.NewReader(os.Stdin), os.Stdout, mode)
}

func serveAdapter(reader *bufio.Reader, writer *os.File, mode string) {
	seq := 0
	event := func(name string, body any) {
		seq++
		payload, _ := json.Marshal(map[string]any{
			"seq": seq, "type": "event", "event": name, "body": body,
		})
		_ = wire.Write(writer, payload)
	}
	reply := func(requestSeq int, command string, success bool, body any, text string) {
		seq++
		message := map[string]any{
			"seq": seq, "type": "response", "request_seq": requestSeq,
			"command": command, "success": success,
		}
		if body != nil {
			message["body"] = body
		}
		if text != "" {
			message["message"] = text
		}
		payload, _ := json.Marshal(message)
		_ = wire.Write(writer, payload)
	}

	for {
		payload, err := wire.Read(reader)
		if err != nil {
			return
		}
		var request struct {
			Seq       int             `json:"seq"`
			Type      string          `json:"type"`
			Command   string          `json:"command"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(payload, &request); err != nil || request.Type != "request" {
			continue
		}
		switch request.Command {
		case "initialize":
			reply(request.Seq, "initialize", true, map[string]any{
				"capabilities": Capabilities{
					SupportsTerminateRequest:     true,
					SupportsExceptionInfoRequest: true,
					SupportsEvaluateForHovers:    true,
				},
			}, "")
			if mode == "crash" {
				os.Exit(3)
			}
		case "launch", "attach":
			reply(request.Seq, request.Command, true, nil, "")
			if mode != "noInitialized" {
				event("initialized", map[string]any{})
			}
		case "configurationDone", "next", "stepIn", "stepOut", "pause":
			reply(request.Seq, request.Command, true, nil, "")
			event("stopped", map[string]any{"reason": "entry", "threadId": 1})
		case "setBreakpoints":
			var arguments struct {
				Breakpoints []SourceBreakpoint `json:"breakpoints"`
			}
			_ = json.Unmarshal(request.Arguments, &arguments)
			points := make([]Breakpoint, 0, len(arguments.Breakpoints))
			for _, point := range arguments.Breakpoints {
				points = append(points, Breakpoint{Verified: true, Line: point.Line})
			}
			reply(request.Seq, "setBreakpoints", true, map[string]any{"breakpoints": points}, "")
		case "threads":
			reply(request.Seq, "threads", true, map[string]any{
				"threads": []Thread{{ID: 1, Name: "main"}},
			}, "")
		case "stackTrace":
			reply(request.Seq, "stackTrace", true, map[string]any{
				"stackFrames": []Frame{{
					ID: 100, Name: "main",
					Source: &Source{Name: "main.go", Path: "/workspace/main.go"},
					Line:   3, Column: 1,
				}},
			}, "")
		case "scopes":
			reply(request.Seq, "scopes", true, map[string]any{
				"scopes": []Scope{{Name: "Locals", VariablesReference: 1000}},
			}, "")
		case "variables":
			reply(request.Seq, "variables", true, map[string]any{
				"variables": []Variable{{Name: "answer", Value: "42", Type: "int"}},
			}, "")
		case "evaluate":
			if mode == "evaluateGuard" {
				os.Exit(9) // the client sent an unauthorized evaluation
			}
			reply(request.Seq, "evaluate", true, map[string]any{
				"result": "42", "type": "int", "variablesReference": 0,
			}, "")
		case "continue":
			reply(request.Seq, "continue", true, map[string]any{"allThreadsContinued": true}, "")
			event("continued", map[string]any{"threadId": 1})
			event("stopped", map[string]any{"reason": "breakpoint", "threadId": 1})
		case "exceptionInfo":
			reply(request.Seq, "exceptionInfo", true, map[string]any{
				"exceptionId": "boom", "description": "fake exception",
			}, "")
		case "terminate":
			reply(request.Seq, "terminate", true, nil, "")
			event("terminated", map[string]any{})
		case "disconnect":
			reply(request.Seq, "disconnect", true, nil, "")
			event("terminated", map[string]any{})
			if mode == "ignoreDisconnect" {
				// Ignore both disconnect and stdin EOF: the client must reap
				// the process group rather than trust a graceful exit.
				select {}
			}
			return
		default:
			reply(request.Seq, request.Command, false, nil, fmt.Sprintf("unsupported %s", request.Command))
		}
	}
}
