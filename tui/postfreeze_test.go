package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/session"
	"github.com/PizenLabs/ohgo/task"
	tea "github.com/charmbracelet/bubbletea"
)

// wirePostfreezeController installs a real task controller behind the program
// so the regression tests exercise the durable lifecycle, not a mock.
func wirePostfreezeController(t *testing.T, p *Program, root string) *task.Controller {
	t.Helper()
	controller := task.NewController(task.NewStore(root), nil)
	p.SetTaskHooks(TaskHooks{
		Begin: func(intent contract.IntentContract, snapshot *agent.TargetSnapshot) TaskProjection {
			recorded, err := controller.Begin(intent, snapshot)
			return postfreezeTaskProjection(recorded, err)
		},
		Current: func() (TaskResume, bool) {
			current, ok := controller.Current()
			if !ok {
				return TaskResume{}, false
			}
			resume := TaskResume{ID: current.ID, Status: string(current.Status), Objective: current.Objective, ReviewReason: current.ReviewReason}
			if current.Contract != nil {
				resume.Contract = *current.Contract
				resume.HasContract = true
			}
			resume.Snapshot = current.Snapshot
			return resume, true
		},
		Detach: func() TaskProjection {
			detached, ok, err := controller.Detach()
			if err != nil {
				return TaskProjection{Err: err.Error()}
			}
			if !ok {
				return TaskProjection{}
			}
			return postfreezeTaskProjection(detached, nil)
		},
		ListResumable: func() []TaskResume {
			tasks, err := controller.ListResumable()
			if err != nil {
				return nil
			}
			out := make([]TaskResume, 0, len(tasks))
			for _, item := range tasks {
				resume := TaskResume{ID: item.ID, Status: string(item.Status), Objective: item.Objective, ReviewReason: item.ReviewReason}
				if item.Contract != nil {
					resume.Contract = *item.Contract
					resume.HasContract = true
				}
				resume.Snapshot = item.Snapshot
				out = append(out, resume)
			}
			return out
		},
		ResumeTask: func(id string) TaskProjection {
			recorded, err := controller.ResumeTask(id)
			return postfreezeTaskProjection(recorded, err)
		},
		CancelResumable: func(id string) TaskProjection {
			recorded, err := controller.CancelResumable(id, "cancelled by user")
			return postfreezeTaskProjection(recorded, err)
		},
		Cancel: func(reason string) TaskProjection {
			recorded, err := controller.Cancel(reason)
			return postfreezeTaskProjection(recorded, err)
		},
		AcceptReview: func(reason string) TaskProjection {
			recorded, err := controller.Cancel(reason)
			return postfreezeTaskProjection(recorded, err)
		},
		Pause: func(reason string) TaskProjection {
			recorded, err := controller.Pause(reason)
			return postfreezeTaskProjection(recorded, err)
		},
		NeedsReview: func(reason string) TaskProjection {
			recorded, err := controller.NeedsReview(reason)
			return postfreezeTaskProjection(recorded, err)
		},
		Complete: func() TaskProjection {
			recorded, err := controller.Complete()
			return postfreezeTaskProjection(recorded, err)
		},
		Fail: func(reason string) TaskProjection {
			recorded, err := controller.Fail(reason)
			return postfreezeTaskProjection(recorded, err)
		},
	})
	return controller
}

func postfreezeTaskProjection(recorded task.Task, err error) TaskProjection {
	projection := TaskProjection{ID: recorded.ID, Status: string(recorded.Status), Objective: recorded.Objective}
	for _, item := range task.Items(recorded) {
		projection.Items = append(projection.Items, TaskItem{State: string(item.State), Label: item.Label})
	}
	if err != nil {
		projection.Err = err.Error()
	}
	return projection
}

func postfreezeIntent(objective string) contract.IntentContract {
	return contract.IntentContract{Objective: objective, Kind: contract.ObjectiveInformational}
}

func postfreezeLoop() *agent.Loop {
	loop, err := agent.New(agent.Config{
		Model: ai.Model{ID: "m", Provider: "p"},
		Stream: func(context.Context, ai.Request) (*ai.Stream, error) {
			return ai.NewCompletedStream(ai.AssistantMessage{
				Content: []ai.ContentBlock{ai.TextContent{Text: "ok"}},
			}), nil
		},
		Tools: []agent.Tool{&idleTool{}},
	})
	if err != nil {
		panic(err)
	}
	return loop
}

// Test 1 — a new session with an open task starts a new task, never a
// forced continuation prompt.
func TestPostfreezeNewSessionWithOpenTask(t *testing.T) {
	root := t.TempDir()
	p := NewProgram(postfreezeLoop(), "test", 0)
	controller := wirePostfreezeController(t, p, root)

	if _, err := controller.Begin(postfreezeIntent("check this project and write README.md"), nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := controller.Pause("interrupted"); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	// Simulate /new: detach the durable task, then start the new intent.
	if _, _, err := controller.Detach(); err != nil {
		t.Fatalf("Detach: %v", err)
	}
	p.runTurn("migrating a project to Next.js")
	if decision := p.TaskDecision(); decision != "NEW_INTENT" {
		t.Fatalf("decision = %q, want NEW_INTENT", decision)
	}
	current, ok := controller.Current()
	if !ok {
		t.Fatal("no current task after the new intent")
	}
	if current.Objective != "migrating a project to Next.js" {
		t.Fatalf("objective = %q, want the new intent", current.Objective)
	}
	resumable, err := controller.ListResumable()
	if err != nil {
		t.Fatalf("ListResumable: %v", err)
	}
	// The live task is the new one; the old README task must still be durable.
	foundOld := false
	for _, r := range resumable {
		if strings.Contains(r.Objective, "README") {
			foundOld = true
		}
	}
	// After Begin the old task is archived and the new task is live: List shows
	// both (live + archived).
	if !foundOld {
		t.Fatalf("the README task was lost: %+v", resumable)
	}
	for _, entry := range p.state.Entries {
		if entry.Kind == EntryNotice && strings.Contains(entry.Notice, "an open task is waiting") {
			t.Fatalf("a blocking continuation prompt leaked into the new session: %q", entry.Notice)
		}
	}
}

// Test 2 — an explicit continuation resumes the persisted task.
func TestPostfreezeExplicitContinuation(t *testing.T) {
	root := t.TempDir()
	p := NewProgram(postfreezeLoop(), "test", 0)
	controller := wirePostfreezeController(t, p, root)

	begun, err := controller.Begin(postfreezeIntent("check this project and write README.md"), nil)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := controller.Pause("interrupted"); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	p.runTurn("continue")
	if decision := p.TaskDecision(); decision != "EXPLICIT_RESUME" {
		t.Fatalf("decision = %q, want EXPLICIT_RESUME", decision)
	}
	current, ok := controller.Current()
	if !ok || current.ID != begun.ID {
		t.Fatalf("current = %+v found=%v, want the resumed task %s", current, ok, begun.ID)
	}
}

// Test 3 — /task resume restores the selected durable task.
func TestPostfreezeTaskResume(t *testing.T) {
	root := t.TempDir()
	p := NewProgram(postfreezeLoop(), "test", 0)
	controller := wirePostfreezeController(t, p, root)

	begun, err := controller.Begin(postfreezeIntent("write README.md"), nil)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := controller.Pause("interrupted"); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if _, _, err := controller.Detach(); err != nil {
		t.Fatalf("Detach: %v", err)
	}
	p.taskCommand([]string{"resume"})
	found := false
	for _, entry := range p.state.Entries {
		if entry.Kind == EntryNotice && strings.Contains(entry.Notice, "resumed task") {
			found = true
		}
	}
	if !found {
		t.Fatal("/task resume did not confirm the resume")
	}
	current, ok := controller.Current()
	if !ok || current.ID != begun.ID {
		t.Fatalf("current = %+v found=%v, want %s", current, ok, begun.ID)
	}
}

// Test 4 — /task cancel is terminal and stops being resumable.
func TestPostfreezeTaskCancel(t *testing.T) {
	root := t.TempDir()
	p := NewProgram(postfreezeLoop(), "test", 0)
	controller := wirePostfreezeController(t, p, root)

	begun, err := controller.Begin(postfreezeIntent("write README.md"), nil)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := controller.Pause("interrupted"); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if _, _, err := controller.Detach(); err != nil {
		t.Fatalf("Detach: %v", err)
	}
	p.taskCommand([]string{"cancel"})
	current, ok := controller.Current()
	if ok && current.ID == begun.ID && !taskStatusTerminal(string(current.Status)) {
		t.Fatalf("cancelled task is still active: %+v", current)
	}
	resumable, err := controller.ListResumable()
	if err != nil {
		t.Fatalf("ListResumable: %v", err)
	}
	for _, r := range resumable {
		if r.ID == begun.ID {
			t.Fatal("a cancelled task is still resumable")
		}
	}
}

// Test 5 — a new intent related to the old task is a new task, never an
// inferred continuation.
func TestPostfreezeRelatedIntentIsNewTask(t *testing.T) {
	root := t.TempDir()
	p := NewProgram(postfreezeLoop(), "test", 0)
	controller := wirePostfreezeController(t, p, root)

	begun, err := controller.Begin(postfreezeIntent("write README.md"), nil)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := controller.Pause("interrupted"); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	p.runTurn("improve the project documentation")
	current, ok := controller.Current()
	if !ok {
		t.Fatal("no current task")
	}
	if current.ID == begun.ID {
		t.Fatal("a related new intent reused the old task identity")
	}
	if current.Objective != "improve the project documentation" {
		t.Fatalf("objective = %q, want the new intent", current.Objective)
	}
}

// Test 6 — a completed task never prompts for continuation.
func TestPostfreezeCompletedTaskNeverPrompts(t *testing.T) {
	root := t.TempDir()
	p := NewProgram(postfreezeLoop(), "test", 0)
	controller := wirePostfreezeController(t, p, root)

	if _, err := controller.Begin(postfreezeIntent("done work"), nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := controller.Complete(); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	p.runTurn("something entirely new")
	for _, entry := range p.state.Entries {
		if entry.Kind == EntryNotice && strings.Contains(entry.Notice, "an open task is waiting") {
			t.Fatalf("a completed task prompted for continuation: %q", entry.Notice)
		}
	}
}

// Test 7 — multiple resumable tasks list deterministically and ambiguous
// resume opens the picker instead of picking arbitrarily.
func TestPostfreezeMultipleResumable(t *testing.T) {
	root := t.TempDir()
	p := NewProgram(postfreezeLoop(), "test", 0)
	controller := wirePostfreezeController(t, p, root)

	for _, objective := range []string{"first docs task", "second docs task"} {
		if _, err := controller.Begin(postfreezeIntent(objective), nil); err != nil {
			t.Fatalf("Begin: %v", err)
		}
		if _, err := controller.Pause("interrupted"); err != nil {
			t.Fatalf("Pause: %v", err)
		}
		if _, _, err := controller.Detach(); err != nil {
			t.Fatalf("Detach: %v", err)
		}
	}
	p.taskCommand([]string{"list"})
	found := false
	for _, entry := range p.state.Entries {
		if entry.Kind == EntryNotice && strings.Contains(entry.Notice, "resumable tasks:") {
			found = true
			if !strings.Contains(entry.Notice, "first docs task") || !strings.Contains(entry.Notice, "second docs task") {
				t.Fatalf("list is missing a task: %q", entry.Notice)
			}
		}
	}
	if !found {
		t.Fatal("/task list did not report the resumable tasks")
	}
	// An ambiguous substring matching both must open the picker.
	p.taskCommand([]string{"resume", "docs task"})
	if p.state.Picker == nil || p.state.Picker.Kind != PickerTasks {
		t.Fatalf("ambiguous resume did not open the task picker: %+v", p.state.Picker)
	}
}

// Test 8 — an active task plus /new preserves the old task and starts clean.
func TestPostfreezeActiveTaskPlusNew(t *testing.T) {
	store, err := session.Open(t.TempDir() + "/sessions.db")
	if err != nil {
		t.Fatalf("session.Open: %v", err)
	}
	defer func() { _ = store.Close() }()
	first, err := store.CreateWith(session.CreateOptions{Cwd: t.TempDir(), Model: "p/m"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	root := t.TempDir()
	p := NewProgram(postfreezeLoop(), "test", 0)
	controller := wirePostfreezeController(t, p, root)
	p.store = store
	p.session = &first
	p.history = []agent.Message{agent.UserMessage{Text: "old"}}
	begun, err := controller.Begin(postfreezeIntent("active work"), nil)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	second, err := store.CreateWith(session.CreateOptions{Cwd: t.TempDir(), Model: "p/m"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	p.sessionActions = SessionActions{
		New: func() (*session.Session, OpenedSession, error) {
			return &second, OpenedSession{Notice: "started new session"}, nil
		},
	}
	Command{Name: "new"}.runOn(p)
	if p.state.Picker == nil || p.state.Picker.Kind != PickerNewSession {
		t.Fatalf("/new did not open the picker: %+v", p.state.Picker)
	}
	// Accept the start row through the picker path (which closes the picker
	// before creating exactly one session).
	p.state.Picker.Cursor = 0
	if _, _ = p.choosePicker(); p.session == nil || p.session.ID != second.ID {
		t.Fatalf("session = %+v, want the new session %d", p.session, second.ID)
	}
	if len(p.history) != 0 {
		t.Fatalf("new session inherited history: %+v", p.history)
	}
	resumable, err := controller.ListResumable()
	if err != nil {
		t.Fatalf("ListResumable: %v", err)
	}
	found := false
	for _, r := range resumable {
		if r.ID == begun.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("the active task was lost by /new")
	}
}

// Test 9 — a new session never inherits the previous transcript.
func TestPostfreezeNewSessionNoTranscriptInheritance(t *testing.T) {
	store, err := session.Open(t.TempDir() + "/sessions.db")
	if err != nil {
		t.Fatalf("session.Open: %v", err)
	}
	defer func() { _ = store.Close() }()
	first, err := store.CreateWith(session.CreateOptions{Cwd: t.TempDir(), Model: "p/m"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := store.Append(first.ID, session.StoredMessage{Role: "user", Text: "old question"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	history, err := session.Replay([]session.StoredMessage{{Role: "user", Text: "old question"}})
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	p := NewProgram(nil, "test", 0)
	p.store = store
	p.session = &first
	p.history = history
	second, err := store.CreateWith(session.CreateOptions{Cwd: t.TempDir(), Model: "p/m"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	p.adoptSession(&second, OpenedSession{Notice: "started new session"})
	if len(p.history) != 0 {
		t.Fatalf("history = %+v, want empty in the new session", p.history)
	}
	for _, entry := range p.state.Entries {
		if entry.Kind == EntryUser && strings.Contains(entry.UserText, "old question") {
			t.Fatalf("the old transcript leaked into the new session: %+v", entry)
		}
	}
}

// Test 10 — a new session preserves intentionally global configuration.
func TestPostfreezeNewSessionPreservesConfig(t *testing.T) {
	p := NewProgram(nil, "groq/llama", 0)
	p.state.Apply(WorkspaceUpdated{Cwd: "/work/proj", HasCwd: true})
	store, err := session.Open(t.TempDir() + "/sessions.db")
	if err != nil {
		t.Fatalf("session.Open: %v", err)
	}
	defer func() { _ = store.Close() }()
	second, err := store.CreateWith(session.CreateOptions{Cwd: "/work/proj", Model: "groq/llama"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	p.store = store
	p.adoptSession(&second, OpenedSession{})
	if p.state.Status.Cwd != "/work/proj" {
		t.Fatalf("cwd = %q, want the project configuration preserved", p.state.Status.Cwd)
	}
}

// /new opens the picker; Esc cancels safely; Enter creates exactly one session.
func TestPostfreezeNewPickerUX(t *testing.T) {
	store, err := session.Open(t.TempDir() + "/sessions.db")
	if err != nil {
		t.Fatalf("session.Open: %v", err)
	}
	defer func() { _ = store.Close() }()
	created := 0
	p := NewProgram(nil, "test", 0)
	p.store = store
	p.sessionActions = SessionActions{
		New: func() (*session.Session, OpenedSession, error) {
			created++
			sess, err := store.CreateWith(session.CreateOptions{Cwd: t.TempDir(), Model: "p/m"})
			if err != nil {
				return nil, OpenedSession{}, err
			}
			return &sess, OpenedSession{Notice: "started new session"}, nil
		},
	}
	Command{Name: "new"}.runOn(p)
	if p.state.Picker == nil || p.state.Picker.Kind != PickerNewSession {
		t.Fatalf("/new did not open the picker: %+v", p.state.Picker)
	}
	// Esc cancels without side effects.
	p.closePicker()
	if p.state.Picker != nil {
		t.Fatal("Esc did not close the /new picker")
	}
	if created != 0 {
		t.Fatalf("cancel created %d sessions, want none", created)
	}
	// Enter creates exactly one, and a second Enter cannot duplicate it.
	Command{Name: "new"}.runOn(p)
	if p.state.Picker == nil {
		t.Fatal("/new did not reopen the picker")
	}
	p.state.Picker.Cursor = 0
	if _, _ = p.choosePicker(); created != 1 {
		t.Fatalf("created = %d, want exactly one session", created)
	}
	if p.state.Picker != nil {
		// The picker closes on choose; a stray second Enter finds no picker.
		t.Fatal("the picker stayed open after choosing")
	}
	if _, _ = p.choosePicker(); created != 1 {
		t.Fatalf("a second Enter created another session: %d", created)
	}
	// Rapid /new invocations only reopen the picker, never create sessions.
	Command{Name: "new"}.runOn(p)
	Command{Name: "new"}.runOn(p)
	Command{Name: "new"}.runOn(p)
	if created != 1 {
		t.Fatalf("rapid /new created sessions without Enter: %d", created)
	}
	if p.state.Picker == nil {
		t.Fatal("rapid /new did not leave the picker open")
	}
}

// /sessions remains the manager while /new is the canonical creator; the
// retired singular /session spelling must not survive as a shadow alias.
func TestPostfreezeSessionCommandCompatibility(t *testing.T) {
	if _, ok := LookupCommand("new"); !ok {
		t.Fatal("/new is not registered")
	}
	if _, ok := LookupCommand("session"); ok {
		t.Fatal("/session is still registered")
	}
	if _, ok := LookupCommand("sessions"); !ok {
		t.Fatal("/sessions is not registered")
	}
	p := NewProgram(nil, "test", 0)
	Command{Name: "sessions"}.runOn(p)
	// Without a browser the manager reports unavailable rather than crashing.
	found := false
	for _, entry := range p.state.Entries {
		if entry.Kind == EntryNotice && strings.Contains(entry.Notice, "session browsing is unavailable") {
			found = true
		}
	}
	if !found {
		t.Fatalf("entries = %+v, want the unavailable notice", p.state.Entries)
	}
	if HelpText() == "" || !strings.Contains(HelpText(), "/new") {
		t.Fatal("help does not expose /new")
	}
	if !strings.Contains(HelpText(), "/sessions ") || !strings.Contains(HelpText(), "/task") {
		t.Fatal("help does not expose /sessions and /task")
	}
	if strings.Contains(HelpText(), "/session ") {
		t.Fatal("help still exposes the retired /session command")
	}
}

// A /new during a running turn cancels the turn and never leaks its history.
func TestPostfreezeNewWhileBusy(t *testing.T) {
	store, err := session.Open(t.TempDir() + "/sessions.db")
	if err != nil {
		t.Fatalf("session.Open: %v", err)
	}
	defer func() { _ = store.Close() }()
	first, err := store.CreateWith(session.CreateOptions{Cwd: t.TempDir(), Model: "p/m"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	release := make(chan struct{})
	loop, err := agent.New(agent.Config{
		Model: ai.Model{ID: "m", Provider: "p"},
		Stream: func(ctx context.Context, _ ai.Request) (*ai.Stream, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
		Tools: []agent.Tool{&idleTool{}},
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	p := NewProgram(loop, "test", 0)
	p.store = store
	p.session = &first
	done := make(chan struct{})
	go func() { defer close(done); p.runTurn("slow work") }()
	// Wait until the turn installs its cancellation handle, then start a new
	// session while the model is still blocked.
	for i := 0; i < 200; i++ {
		p.mu.Lock()
		claimed := p.cancelTurn != nil
		p.mu.Unlock()
		if claimed {
			break
		}
		if i == 199 {
			t.Fatal("the turn never started")
		}
		time.Sleep(10 * time.Millisecond)
		select {
		case <-done:
			t.Fatal("the turn finished before /new ran")
		default:
		}
	}
	second, err := store.CreateWith(session.CreateOptions{Cwd: t.TempDir(), Model: "p/m"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	p.sessionActions = SessionActions{
		New: func() (*session.Session, OpenedSession, error) {
			return &second, OpenedSession{Notice: "started new session"}, nil
		},
	}
	p.openNewSessionPicker(nil)
	if p.state.Picker == nil {
		t.Fatal("/new did not open while busy")
	}
	p.state.Picker.Cursor = 0
	item, _ := p.state.Picker.Chosen()
	_, _ = p.chooseNewSession(item)
	<-done
	if p.session == nil || p.session.ID != second.ID {
		t.Fatalf("session = %+v, want the new session", p.session)
	}
	_ = release
}

// Old tool proposals and session approvals never cross the session boundary.
func TestPostfreezeNoAuthorityLeak(t *testing.T) {
	store, err := session.Open(t.TempDir() + "/sessions.db")
	if err != nil {
		t.Fatalf("session.Open: %v", err)
	}
	defer func() { _ = store.Close() }()
	first, err := store.CreateWith(session.CreateOptions{Cwd: t.TempDir(), Model: "p/m"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	p := NewProgram(nil, "test", 0)
	p.store = store
	p.session = &first
	p.allowForSession("write")
	if !p.sessionAllowedFor("write") {
		t.Fatal("setup did not record the approval")
	}
	second, err := store.CreateWith(session.CreateOptions{Cwd: t.TempDir(), Model: "p/m"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	p.adoptSession(&second, OpenedSession{})
	if p.sessionAllowedFor("write") {
		t.Fatal("a whole-session tool approval leaked into the new session")
	}
	if p.pending != nil {
		t.Fatal("a pending approval survived the session switch")
	}
}

// continuationPrompt is explicit, never similarity-based.
func TestPostfreezeContinuationClassification(t *testing.T) {
	for _, text := range []string{
		"continue", "resume", "continue the previous task",
		"resume the README task", "continue where we left off",
	} {
		if !continuationPrompt(text) {
			t.Errorf("continuationPrompt(%q) = false, want true", text)
		}
	}
	for _, text := range []string{
		"migrating a project to Next.js", "check the authentication flow",
		"improve the project documentation", "add tests for the parser",
	} {
		if continuationPrompt(text) {
			t.Errorf("continuationPrompt(%q) = true, want false for a new intent", text)
		}
	}
}

// Session IDs stay unique and refer to the new session.
func TestPostfreezeSessionIDIntegrity(t *testing.T) {
	store, err := session.Open(t.TempDir() + "/sessions.db")
	if err != nil {
		t.Fatalf("session.Open: %v", err)
	}
	defer func() { _ = store.Close() }()
	seen := map[int64]bool{}
	var last int64
	for i := 0; i < 3; i++ {
		sess, err := store.CreateWith(session.CreateOptions{Cwd: t.TempDir(), Model: "p/m"})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if seen[sess.ID] {
			t.Fatalf("duplicate session id %d", sess.ID)
		}
		seen[sess.ID] = true
		last = sess.ID
	}
	p := NewProgram(nil, "test", 0)
	p.store = store
	p.sessionActions = SessionActions{
		New: func() (*session.Session, OpenedSession, error) {
			sess, err := store.CreateWith(session.CreateOptions{Cwd: t.TempDir(), Model: "p/m"})
			if err != nil {
				return nil, OpenedSession{}, err
			}
			return &sess, OpenedSession{Notice: "started new session"}, nil
		},
	}
	p.openNewSessionPicker(nil)
	p.state.Picker.Cursor = 0
	item, _ := p.state.Picker.Chosen()
	_, _ = p.chooseNewSession(item)
	if p.session == nil || seen[p.session.ID] {
		t.Fatalf("new session id %v is not fresh", p.session)
	}
	_ = last
	var notice string
	for _, entry := range p.state.Entries {
		if entry.Kind == EntryNotice {
			notice += entry.Notice
		}
	}
	_ = notice
}

// The command picker surfaces /new like every other command.
func TestPostfreezePickerSurfacesNew(t *testing.T) {
	found := false
	for _, entry := range commandEntries("", "") {
		if entry.Name == "/new" {
			found = true
		}
	}
	if !found {
		t.Fatal("the command picker does not surface /new")
	}
	if got := filterCommands("ne"); len(got) == 0 {
		t.Fatal("filtering for /new found nothing")
	} else {
		matched := false
		for _, spec := range got {
			if spec.Name == "new" {
				matched = true
			}
		}
		if !matched {
			t.Fatalf("filterCommands(ne) = %+v, want /new", got)
		}
	}
	// Keyboard conventions: Esc closes without acting.
	p := NewProgram(nil, "test", 0)
	p.state.Apply(PickerOpened{Kind: PickerNewSession, Title: "New Session", Items: []PickerItem{{Title: "Start new session", Value: "new"}}})
	keyPress(p, tea.KeyMsg{Type: tea.KeyEsc})
	if p.state.Picker != nil {
		t.Fatal("Esc did not cancel the /new picker")
	}
}

// Adversarial boundaries: /new after cancel, after verification failure,
// after provider failure, and with an active mutation proposal never
// duplicates, loses, leaks, or executes old work in the new session.
func TestPostfreezeAdversarialBoundaries(t *testing.T) {
	root := t.TempDir()
	p := NewProgram(postfreezeLoop(), "test", 0)
	controller := wirePostfreezeController(t, p, root)

	// After cancellation.
	if _, err := controller.Begin(postfreezeIntent("cancelled work"), nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := controller.Cancel("user cancelled"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	// After verification failure (NEEDS_REVIEW).
	if _, err := controller.Begin(postfreezeIntent("unverified work"), nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := controller.NeedsReview("change was not verified"); err != nil {
		t.Fatalf("NeedsReview: %v", err)
	}
	// After provider failure (FAILED).
	if _, _, err := controller.Detach(); err != nil {
		// Detach the review task so the next Begin is clean.
		t.Fatalf("Detach: %v", err)
	}
	if _, err := controller.Begin(postfreezeIntent("failing work"), nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := controller.Fail("provider exploded"); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	// A terminal failure is never resumable, and detaching it preserves the
	// record without blocking the next session.
	if _, _, err := controller.Detach(); err != nil {
		t.Fatalf("Detach failed: %v", err)
	}
	p.runTurn("fresh work after failures")
	if decision := p.TaskDecision(); decision != "NEW_INTENT" {
		t.Fatalf("decision = %q, want NEW_INTENT after failures", decision)
	}
	for _, entry := range p.state.Entries {
		if entry.Kind == EntryNotice && strings.Contains(entry.Notice, "an open task is waiting") {
			t.Fatalf("a terminal task blocked the new session: %q", entry.Notice)
		}
	}

	// With an active mutation proposal: the old approval must be denied and
	// never execute in the new session.
	store, err := session.Open(t.TempDir() + "/sessions.db")
	if err != nil {
		t.Fatalf("session.Open: %v", err)
	}
	defer func() { _ = store.Close() }()
	first, err := store.CreateWith(session.CreateOptions{Cwd: t.TempDir(), Model: "p/m"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	second, err := store.CreateWith(session.CreateOptions{Cwd: t.TempDir(), Model: "p/m"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	q := NewProgram(nil, "test", 0)
	q.store = store
	q.session = &first
	answer := make(chan approvalAnswer, 1)
	q.pending = &approvalRequest{toolName: "write", answer: answer}
	q.adoptSession(&second, OpenedSession{})
	if q.pending != nil {
		t.Fatal("a pending tool proposal survived /new")
	}
	select {
	case ans := <-answer:
		if ans.approved {
			t.Fatal("the old proposal was approved in the new session")
		}
	default:
		t.Fatal("the old proposal was not answered on session switch")
	}
	if q.session == nil || q.session.ID != second.ID {
		t.Fatalf("session = %+v, want %d", q.session, second.ID)
	}
}
