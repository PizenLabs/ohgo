package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/ai"
	"github.com/PizenLabs/ohgo/commit"
	"github.com/PizenLabs/ohgo/session"
	"github.com/PizenLabs/ohgo/state"
	"github.com/PizenLabs/ohgo/tui"
	"github.com/PizenLabs/ohgo/update"
)

// History is a parameter so a resumed session can be seeded from a stored
// transcript. It is kept in the caller-owned slice rather than in the loop
// because each Run is a turn, not a conversation: handing Run a single message
// would make every turn start from nothing, which is precisely the bug this
// shape is designed to prevent.
//
// Commands dispatch through the same harness the full-screen UI uses, so both
// interfaces agree on what each command means.
func repl(h *harness, out io.Writer, in io.Reader, history []agent.Message) error {
	reader := bufio.NewReader(in)
	pending := ""
	for {
		// A background release observation only ever becomes visible at a
		// prompt boundary, never in the middle of a line the user is typing.
		if h.updateNotice != nil {
			h.updateNotice(out)
		}
		var text string
		if pending != "" {
			text, pending = pending, ""
		} else {
			_, _ = fmt.Fprint(out, "\n> ")
			line, err := reader.ReadString('\n')
			if err == io.EOF {
				_, _ = fmt.Fprintln(out)
				return nil
			}
			if err != nil {
				return fmt.Errorf("read input: %w", err)
			}
			text = strings.TrimSpace(line)
			if text == "" {
				continue
			}
		}

		// Commands are recognised before anything is sent to the model: a typo
		// like "/modle" would otherwise cost a turn and produce a confused
		// answer.
		if command, ok := tui.ParseCommand(text); ok {
			switch command.Name {
			case "quit", "exit", "q":
				return nil
			case "help":
				_, _ = fmt.Fprintln(out, tui.HelpText())
				continue
			case "clear":
				history = nil
				_, _ = fmt.Fprintln(out, "Transcript cleared.")
				continue
			case "model":
				// The plain REPL switches models the same way the full-screen
				// UI does, including live discovery for ids the bundle never
				// saw — one resolver serves both.
				if len(command.Args) == 0 {
					_, _ = fmt.Fprintln(out, "usage: /model provider/id — e.g. /model groq/llama-3.1-8b-instant")
					continue
				}
				model, err := h.SetModel(command.Args[0])
				if err != nil {
					_, _ = fmt.Fprintf(out, "unknown model %q: %v\nrun `ohgo models` to list them\n", command.Args[0], err)
					continue
				}
				_, _ = fmt.Fprintf(out, "Switched to %s/%s.\n", model.Provider, model.ID)
				continue
			case "models":
				for _, id := range h.ModelList() {
					_, _ = fmt.Fprintln(out, "  "+id)
				}
				continue
			case "status":
				_, _ = fmt.Fprintln(out, h.StatusReport(history, 0, "full-screen UI only", false))
				continue
			case "update":
				// The plain REPL presents the same authority as `ohgo update`
				// and the full-screen /update; only the rendering differs.
				var last update.Stage
				svc := update.NewService(update.Config{
					CurrentVersion: version,
					Progress: func(stage update.Stage) {
						if stage == last {
							return
						}
						last = stage
						_, _ = fmt.Fprintln(out, stageLine(stage))
					},
				})
				if err := executeUpdateTo(context.Background(), out, svc, false); err != nil {
					var reported *reportedExit
					if !errors.As(err, &reported) {
						_, _ = fmt.Fprintln(out, err)
					}
				}
				continue
			case "usage":
				_, _ = fmt.Fprintln(out, h.UsageReport())
				continue
			case "doctor":
				for _, line := range h.DoctorLines() {
					status := "ok"
					if !line.OK {
						status = "FAIL"
					}
					_, _ = fmt.Fprintf(out, "%-10s %-12s %s\n", status, line.Name, line.Detail)
				}
				continue
			case "mcp":
				_, _ = fmt.Fprintln(out, replMCP(h, command.Args))
				continue
			case "retry":
				last, ok := agent.LastUserText(history)
				if !ok {
					_, _ = fmt.Fprintln(out, "nothing to retry: no prompt yet")
					continue
				}
				pending = last
				continue
			case "compact":
				compacted, summary, err := h.CompactHistory(context.Background(), history)
				if err != nil {
					_, _ = fmt.Fprintf(out, "compact: %v\n", err)
					continue
				}
				history = compacted
				_, _ = fmt.Fprintf(out, "Compacted: %s.\n", summary)
				continue
			case "connect":
				if err := h.ConnectREPL(out, reader); err != nil {
					_, _ = fmt.Fprintf(out, "connect: %v\n", err)
				}
				continue
			case "disconnect":
				provider := h.model.Provider
				if len(command.Args) > 0 {
					provider = command.Args[0]
				}
				outcome, err := h.Disconnect(provider)
				if err != nil {
					_, _ = fmt.Fprintf(out, "disconnect: %v\n", err)
					continue
				}
				_, _ = fmt.Fprintln(out, outcome)
				continue
			case "sessions":
				h.ListSessions(out)
				continue
			case "new":
				// /new is explicit in both interfaces: a fresh session with no
				// inherited transcript. /sessions stays the manager/lister.
				if h.store == nil {
					_, _ = fmt.Fprintln(out, "no session store attached")
					continue
				}
				sess, err := h.store.CreateWith(session.CreateOptions{
					Cwd:       h.root,
					Model:     h.model.Provider + "/" + h.model.ID,
					Workspace: h.activeWorkspace().String(),
					Project:   h.projectID,
				})
				if err != nil {
					_, _ = fmt.Fprintf(out, "new session: %v\n", err)
					continue
				}
				title := strings.TrimSpace(strings.Join(command.Args, " "))
				if title != "" {
					_ = h.store.SetTitle(sess.ID, title)
				}
				history = nil
				h.sess = &sess
				_, _ = fmt.Fprintf(out, "started new session %d\n", sess.ID)
				continue
			case "resume":
				if len(command.Args) == 0 {
					_, _ = fmt.Fprintln(out, "usage: /resume <id>")
					continue
				}
				var id int64
				if _, err := fmt.Sscanf(command.Args[0], "%d", &id); err != nil {
					_, _ = fmt.Fprintf(out, "invalid session id %q\n", command.Args[0])
					continue
				}
				relayed, notice, err := h.ResumeSession(id)
				if err != nil {
					_, _ = fmt.Fprintf(out, "resume: %v\n", err)
					continue
				}
				history = relayed
				_, _ = fmt.Fprintln(out, notice)
				continue
			case "undo":
				_, _ = fmt.Fprintln(out, formatRecoveryResult(h.Undo()))
				continue
			case "redo":
				_, _ = fmt.Fprintln(out, formatRecoveryResult(h.Redo()))
				continue
			case "commit":
				if err := h.commitREPL(out, reader); err != nil {
					_, _ = fmt.Fprintln(out, commit.AbortText(err))
				}
				continue
			case "jobs":
				_, _ = fmt.Fprintln(out, h.JobsReport())
				continue
			case "task":
				_, _ = fmt.Fprintln(out, "task continuity is available in the full-screen UI; the plain REPL is stateless")
				continue
			case "audit":
				_, _ = fmt.Fprintln(out, "the context audit is available in the full-screen UI; set OHGO_DEBUG_CONTEXT=1 to print projection decisions in stateless runs")
				continue
			case "ask", "fuzz", "build":
				ws, err := h.SwitchWorkspace(command.Name)
				if err != nil {
					_, _ = fmt.Fprintf(out, "workspace: %v\n", err)
					continue
				}
				_, _ = fmt.Fprintf(out, "switched to %s.\n", ws.Label())
				if len(command.Args) > 0 {
					pending = strings.Join(command.Args, " ")
				}
				continue
			case "theme":
				_, _ = fmt.Fprintln(out, "theme applies to the full-screen UI (dark or light via /theme there)")
				continue
			case "commands":
				_, _ = fmt.Fprintln(out, tui.HelpText())
				continue
			default:
				text := fmt.Sprintf("unknown command /%s — try /help", command.Name)
				if near, ok := tui.SuggestCommand(command.Name); ok {
					text = fmt.Sprintf("unknown command /%s — did you mean /%s?", command.Name, near)
				}
				_, _ = fmt.Fprintln(out, text)
				continue
			}
		}

		// $sup opens the ephemeral support surface: an isolated conversation
		// that is never appended to the main history and never persisted
		// (§10.2, I17). A bare "$sup" closes it.
		if question, ok := tui.ParseSupport(text); ok {
			if strings.TrimSpace(question) == "" {
				h.CloseSupport()
				_, _ = fmt.Fprintln(out, "support closed.")
				continue
			}
			answer, err := h.Support(context.Background(), question)
			if err != nil {
				_, _ = fmt.Fprintf(out, "$sup: %v\n", err)
				continue
			}
			_, _ = fmt.Fprintln(out, answer)
			continue
		}

		// The control plane refuses a request that does not match the active
		// workspace before the model is asked. It never changes workspace here;
		// the user must switch explicitly (§3, §11).
		authority := ""
		if h.loop != nil {
			route := h.RouteTurn(text)
			if !route.Allowed() {
				_ = refuseHeadless(out, route)
				continue
			}
			authority = route.Active.String()
		}

		// The prompt is appended to a history file best-effort, so a plain-REPL
		// user keeps an audit trail across runs. A failure here never fails the
		// turn: the conversation in memory is what matters.
		recordReplHistory(text)

		// The user's own turn is persisted alongside the assistant's, or a
		// replay would start with the answer to a question it never saw.
		if h.store != nil && h.sess != nil {
			if stored, err := session.FromAgentMessage(agent.UserMessage{Text: text}); err == nil && stored.Role != "" {
				if err := h.store.Append(h.sess.ID, stored); err != nil {
					fmt.Fprintf(os.Stderr, "ohgo: could not save turn: %v\n", err)
				}
			}
		}

		user := agent.UserMessage{Text: text}
		request := append(append([]agent.Message(nil), history...), user)

		if h.newBudget != nil {
			h.loop.SetBudget(h.newBudget())
		}
		// The plain REPL records the same edit boundary as the full-screen UI:
		// one explicit turn, one recovery point. The journal is bound to the
		// session this turn belongs to and cleared afterwards.
		h.loop.SetJournal(h.journal())
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		_, turns := runHeadlessTurnAs(ctx, h.loop, h.root, out, text, history, authority)
		h.loop.SetJournal(nil)
		stop()
		if h.usage != nil {
			h.usage.Add(turns)
		}
		// Whatever the loop actually sent replaces the accumulator, including
		// any compaction it applied, so the next turn starts from the real
		// context rather than a stale copy. A reviewable or paused turn is
		// still real conversation: keeping it lets the next prompt continue
		// from what the model actually saw.
		history = rebaseHistory(request, turns)
	}
}

// replHistoryMaxLines caps the prompt history file, so it stays an audit
// trail rather than growing without bound.
const replHistoryMaxLines = 1000

// replHistoryPath resolves where plain-REPL prompts are recorded.
//
// OHGO_HISTORY_FILE overrides the default; setting it to the empty string
// disables recording. The default is ~/.ohgo/history, next to the session
// store. Tests set the variable to a temp file (or empty) so a scripted REPL
// never touches the real home directory.
func replHistoryPath() string {
	if value, ok := os.LookupEnv("OHGO_HISTORY_FILE"); ok {
		return strings.TrimSpace(value)
	}
	path, err := state.Path("history")
	if err != nil {
		return ""
	}
	return path
}

// recordReplHistory appends one user line to the history file, best-effort.
//
// It never returns an error to the caller: losing an audit line is acceptable,
// interrupting a working turn over it is not. Failures are reported to stderr
// once, at the point they happen, so a broken path does not fail silently on
// every turn without anyone noticing.
func recordReplHistory(text string) {
	path := replHistoryPath()
	if path == "" || strings.TrimSpace(text) == "" {
		return
	}
	if err := appendReplHistoryLine(path, text); err != nil {
		fmt.Fprintf(os.Stderr, "ohgo: could not record history: %v\n", err)
	}
}

// appendReplHistoryLine appends a line and rotates the file to the tail.
func appendReplHistoryLine(path, text string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, writeErr := fmt.Fprintln(f, text)
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	return rotateReplHistory(path)
}

// rotateReplHistory keeps only the last replHistoryMaxLines lines.
func rotateReplHistory(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(data), "\n")
	// A trailing newline produces one empty final element; it is not a line.
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) <= replHistoryMaxLines {
		return nil
	}
	tail := strings.Join(lines[len(lines)-replHistoryMaxLines:], "\n") + "\n"
	return os.WriteFile(path, []byte(tail), 0o644)
}

// rebaseHistory rebuilds the running history after one turn.
//
// Compaction rewrites the context, so appending the new messages to the old
// list would silently reintroduce everything the summary just removed. Only
// the user turn plus whatever the assistant produced is kept.
func rebaseHistory(request []agent.Message, turns []agent.Turn) []agent.Message {
	out := append([]agent.Message(nil), request...)
	for _, turn := range turns {
		out = append(out, agent.AssistantMessage{Message: turn.Assistant})
		for _, outcome := range turn.Results {
			out = append(out, agent.ToolResultMessage{
				ToolCallID: outcome.CallID,
				ToolName:   outcome.ToolName,
				Content:    []ai.ContentBlock{ai.TextContent{Text: outcome.Result.Text}},
				IsError:    outcome.Result.IsError,
			})
		}
	}
	return out
}
