package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/ai"
	"github.com/PizenLabs/ohgo/prompt"
)

// promptBlocks renders the system prompt into provider blocks.
func promptBlocks(opts options, extra string) []ai.SystemPromptBlock {
	blocks := prompt.Build(opts.root, extra)
	out := make([]ai.SystemPromptBlock, 0, len(blocks))
	for i, text := range blocks {
		out = append(out, ai.SystemPromptBlock{
			Text:  text,
			Cache: i == len(blocks)-1,
		})
	}
	return out
}

// printTurn streams a completed turn to the output.
func printTurn(out io.Writer, turn agent.Turn) {
	for _, block := range turn.Assistant.Content {
		switch b := block.(type) {
		case ai.TextContent:
			if b.Text != "" {
				_, _ = fmt.Fprintln(out, b.Text)
			}
		}
	}
	for _, outcome := range turn.Results {
		status := "ok"
		if outcome.Result.IsError {
			status = "error"
		}
		_, _ = fmt.Fprintf(out, "  %s %s [%s]\n", status, outcome.ToolName, outcome.CallID)
	}
}

// approveToolCall asks the user to permit a tool call.
func approveToolCall(out io.Writer, opts options, call ai.ToolCall, approval agent.Approval) (bool, string) {
	// Policy from settings wins, so a user who denied a tool is never asked
	// again for it.
	if policy, ok := opts.settings.ApprovalFor(call.Name); ok {
		switch policy {
		case "allow":
			return true, ""
		case "deny":
			return false, "blocked by settings"
		}
	}
	if approval.Reason != "" {
		_, _ = fmt.Fprintf(out, "%s (%s)\n", call.Name, approval.Reason)
	} else {
		_, _ = fmt.Fprintln(out, call.Name)
	}
	_, _ = fmt.Fprint(out, "Run this tool? [y/N] ")
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return false, "could not read approval"
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	if answer == "y" || answer == "yes" {
		return true, ""
	}
	return false, "denied by user"
}
