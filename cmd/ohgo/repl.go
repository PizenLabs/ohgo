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

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/commit"
	"github.com/PizenLabs/ohgo/internal/continuity/session"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
	"github.com/PizenLabs/ohgo/internal/state"
	"github.com/PizenLabs/ohgo/internal/tui"
	"github.com/PizenLabs/ohgo/internal/update"
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
	// lastEditDiff is the canonical diff from the previous turn's mutation
	// tools. It is the fallback /copy uses when an edit turn's prose does not
	// repeat the change, and it is replaced at every turn boundary so a later
	// answer never inherits an earlier edit.
	lastEditDiff := ""
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
				lastEditDiff = ""
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
			case "copy":
				copyREPL(out, history, lastEditDiff, command.Args)
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
					Model:     h.model.Ref(),
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
				h.bindSession(&sess)
				lastEditDiff = ""
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
				lastEditDiff = ""
				_, _ = fmt.Fprintln(out, notice)
				continue
			case "undo":
				_, _ = fmt.Fprintln(out, formatRecoveryResult(h.Undo()))
				continue
			case "redo":
				_, _ = fmt.Fprintln(out, formatRecoveryResult(h.Redo()))
				continue
			case "commit":
				if err := h.commitREPL(out, reader, command.Args...); err != nil {
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
				_, _ = fmt.Fprintln(out, "theme applies to the full-screen UI (auto, dark, or light via /theme there)")
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

		// The control plane decides the turn's workspace before the model is
		// asked. BUILD <-> FUZZ cooperation is automatic; crossing ASK is a
		// human decision, and the line-oriented REPL has no decision surface,
		// so leaving ASK remains an explicit /ask, /fuzz, or /build command.
		authority := ""
		if h.loop != nil {
			decision := workspace.DecideTransition(h.activeWorkspace(), workspace.AnalyzeRequest(text))
			switch {
			case decision.Automatic():
				from := h.activeWorkspace()
				if _, err := h.SwitchWorkspace(decision.Target.String()); err != nil {
					_, _ = fmt.Fprintf(out, "workspace: %v\n", err)
					continue
				}
				_, _ = fmt.Fprintf(out, "workspace %s -> %s: %s\n",
					from.Label(), decision.Target.Label(), decision.Reason)
			case decision.NeedsConfirmation():
				// The line-oriented REPL has no confirmation surface. Crossing
				// ASK therefore remains an explicit command: telling the user
				// which one is truthful, while silently running under the wrong
				// authority would not be.
				_, _ = fmt.Fprintf(out, "this request needs %s authority; switch with /%s and resend it\n",
					decision.Target.Label(), decision.Target.String())
				continue
			}
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

		// The first submitted prompt names the session immediately, locally,
		// before any provider request can compete with the turn.
		applyLocalSessionTitle(h.store, h.sess, text)

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
		lastEditDiff = toolDiffs(turns)
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

// copyREPL serves /copy in the plain REPL through the same extraction and the
// same OSC 52 transport as the full-screen command.
//
// The REPL has no transcript entries, so the latest answer is derived from
// history. A missing transport is reported rather than printing the payload:
// /copy promises the clipboard, and a terminal that cannot accept OSC 52 must
// not look like it did.
func copyREPL(out io.Writer, history []agent.Message, editDiff string, args []string) {
	mode, err := tui.ParseCopyMode(args)
	if err != nil {
		_, _ = fmt.Fprintln(out, err)
		return
	}

	var payload string
	var kind tui.CopyKind
	if mode == tui.CopyModeContext {
		payload, kind = tui.ContextTranscript(history), tui.CopyKindContext
		if payload == "" {
			_, _ = fmt.Fprintln(out, "nothing to copy: the session context is empty")
			return
		}
	} else {
		response, ok := tui.LatestResponse(history)
		if !ok && editDiff == "" {
			_, _ = fmt.Fprintln(out, "nothing to copy: no response yet")
			return
		}
		payload, kind, ok = tui.CopyPayload(mode, response, editDiff)
		if !ok {
			_, _ = fmt.Fprintln(out, "nothing to copy: the response is empty")
			return
		}
	}

	encode := tui.ClipboardForEnv(os.Getenv("TERM"), os.Getenv("TMUX"), os.Getenv("OHGO_CLIPBOARD"))
	if encode == nil {
		_, _ = fmt.Fprintln(out, "clipboard is unavailable: OSC 52 needs a terminal (OHGO_CLIPBOARD=off disables it)")
		return
	}
	sequence := encode(payload)
	if sequence == "" {
		_, _ = fmt.Fprintln(out, "copy: the terminal did not accept the clipboard payload")
		return
	}
	if _, err := io.WriteString(out, sequence); err != nil {
		_, _ = fmt.Fprintf(out, "copy: %v\n", err)
		return
	}
	_, _ = fmt.Fprintf(out, "Copied %s.\n", kind)
}

// toolDiffs joins the canonical mutation diffs from one turn. A duplicate is
// dropped so a retried identical edit does not paste the same hunk twice.
func toolDiffs(turns []agent.Turn) string {
	var diffs []string
	seen := map[string]bool{}
	for _, turn := range turns {
		for _, outcome := range turn.Results {
			diff := strings.TrimSpace(tui.MutationDiff(outcome.Result.Details))
			if diff == "" || seen[diff] {
				continue
			}
			seen[diff] = true
			diffs = append(diffs, diff)
		}
	}
	return strings.Join(diffs, "\n")
}

// rebaseHistory rebuilds the running history after one turn.
//
// Compaction rewrites the context, so appending the new messages to the old
// list would silently reintroduce everything the summary just removed. Only
// the user turn plus whatever the assistant produced is kept.
func rebaseHistory(request []agent.Message, turns []agent.Turn) []agent.Message {
	out := append([]agent.Message(nil), request...)
	return append(out, agent.ConversationMessages(turns)...)
}
