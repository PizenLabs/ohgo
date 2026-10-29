// InitWizardModel is the in-canvas onboarding flow for uninitialized
// projects. It replaces the pre-TUI stdin prompts (which scrolled the
// terminal and then jumped into the alt-screen) with a modal dialog that
// lives inside the Bubble Tea Update/View loop, so the header and footer
// stay pinned while the wizard owns the middle of the screen.
//
// The step order is the startup gate's contract and must not change:
// (1) Git repository check/init prompt, (2) runtime-state ignore prompt,
// (3) worker identity prompt, (4) project name prompt. The Git and ignore
// steps are skipped when they are already satisfied. Decisions are collected
// first and committed only through InitWizardHooks.Complete, mirroring
// startup.Ensure's decisions-first-mutations-last rule: cancelling at any
// step leaves nothing created behind.
//
// The model never touches os.Stdin or fmt.Scan: keys arrive as tea.KeyMsg
// and text editing is delegated to bubbles/textinput.
package tui

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Wizard palette tokens. These are onboarding-chrome accents, kept local to
// the dialog so the global 256-colour theme in style.go is untouched.
const (
	// wizardTeal is the primary border and focus accent.
	wizardTeal = "#5FB3A1"
	// wizardAmber marks warnings and validation errors.
	wizardAmber = "#C9A86A"
	// wizardGreen is success and active-text.
	wizardGreen = "#8FB08D"
	// wizardBlue and wizardGrey are secondary labels and hints.
	wizardBlue = "#7AA8C9"
	wizardGrey = "#C9C5BE"
	// wizardBG is the dialog surface.
	wizardBG = "#0E1113"
)

// WizardStep is one onboarding question. The numeric order is the prompt
// order; Done is terminal.
type WizardStep int

const (
	// WizardStepGit asks whether to create a Git repository. Skipped when
	// the directory already sits inside one.
	WizardStepGit WizardStep = iota
	// WizardStepIgnore asks whether to keep .ohgo/ out of Git. Skipped when
	// the existing .gitignore already covers it.
	WizardStepIgnore
	// WizardStepIdentity asks for the project-local worker name.
	WizardStepIdentity
	// WizardStepProject asks for the human-readable project name.
	WizardStepProject
	// WizardStepDone marks collected decisions ready to commit.
	WizardStepDone
)

// Git choice indexes, in button order.
const (
	wizardGitYes = iota
	wizardGitNo
	wizardGitQuit
)

// Ignore choice indexes, in button order. Add is the default: runtime state
// is not project source.
const (
	wizardIgnoreYes = iota
	wizardIgnoreNo
)

// wizardInputLimit bounds a name field. Names are labels, not prose; the
// cap keeps one decision on one row of the card.
const wizardInputLimit = 80

// InitWizardDecision is the collected onboarding answers handed to
// InitWizardHooks.Complete. It carries the same four decisions
// startup.Ensure collects from stdin, in the same order.
type InitWizardDecision struct {
	// WantGit reports the user chose to create a Git repository.
	WantGit bool
	// IgnoreOhgo reports the user chose to add .ohgo/ to .gitignore. It is
	// true when the step was skipped because the rule was already satisfied.
	IgnoreOhgo bool
	// Worker is the confirmed project-local worker name.
	Worker string
	// Project is the confirmed human-readable project name.
	Project string
}

// InitWizardHooks binds the wizard's terminal transitions to its owner.
// Both run on the Bubble Tea update goroutine, where the wizard lives,
// so Complete may call Program methods (AttachSession, PublishWorkspace)
// directly: it is the same goroutine that owns the state.
type InitWizardHooks struct {
	// Complete commits the decisions (git init, project.yml, session) and
	// returns the identity facts plus a notice to publish. A non-nil error
	// keeps the wizard open on its final step with the error shown, so a
	// failed commit never strands the user on an empty canvas.
	Complete func(InitWizardDecision) (facts IdentityUpdated, notice string, err error)
	// Cancel runs when the user quits onboarding. Nil means tea.Quit:
	// nothing was committed, so exiting is the clean outcome.
	Cancel func() tea.Cmd
}

// InitWizardModel owns one onboarding run. The zero value is unusable;
// construct with NewInitWizard.
type InitWizardModel struct {
	step         WizardStep
	askGit       bool
	askIgnore    bool
	gitCursor    int
	ignoreCursor int
	wantGit      bool
	wantIgnore   bool
	identity     textinput.Model
	project      textinput.Model
	workerDef    string
	projectDef   string
	errMsg       string
	done         bool
	canceled     bool
}

// NewInitWizard returns a wizard starting at the first step the project still
// needs: Git when askGit, else the ignore choice when askIgnore, else the
// identity prompt. Defaults seed the inputs exactly like the stdin prompts: an
// untouched field confirms its default, and the ignore choice starts on Add.
func NewInitWizard(askGit, askIgnore bool, workerDef, projectDef string) *InitWizardModel {
	identity := textinput.New()
	identity.Prompt = ""
	identity.Placeholder = "Your name"
	identity.CharLimit = wizardInputLimit
	identity.SetValue(workerDef)
	identity.CursorEnd()

	project := textinput.New()
	project.Prompt = ""
	project.Placeholder = "Project name"
	project.CharLimit = wizardInputLimit
	project.SetValue(projectDef)
	project.CursorEnd()

	w := &InitWizardModel{
		askGit:     askGit,
		askIgnore:  askIgnore,
		wantIgnore: true,
		identity:   identity,
		project:    project,
		workerDef:  strings.TrimSpace(workerDef),
		projectDef: strings.TrimSpace(projectDef),
	}
	switch {
	case askGit:
		w.step = WizardStepGit
	case askIgnore:
		w.step = WizardStepIgnore
	default:
		w.step = WizardStepIdentity
		// The returned blink command belongs to a runtime that does not
		// exist yet; dropping it leaves a steady cursor, which still
		// marks focus through the input's focus ring.
		_ = w.identity.Focus()
	}
	return w
}

// Step reports the current onboarding question.
func (w *InitWizardModel) Step() WizardStep { return w.step }

// Done reports the decisions are collected and ready to commit.
func (w *InitWizardModel) Done() bool { return w.done }

// Canceled reports the user quit before committing anything.
func (w *InitWizardModel) Canceled() bool { return w.canceled }

// Decision returns the collected answers. It is meaningful only when Done.
func (w *InitWizardModel) Decision() InitWizardDecision {
	return InitWizardDecision{
		WantGit:    w.wantGit,
		IgnoreOhgo: w.wantIgnore,
		Worker:     strings.TrimSpace(w.identity.Value()),
		Project:    strings.TrimSpace(w.project.Value()),
	}
}

// GitCursor reports the highlighted Git choice index (Yes/No/Quit order).
func (w *InitWizardModel) GitCursor() int { return w.gitCursor }

// IgnoreCursor reports the highlighted ignore choice index (Add/Leave order).
func (w *InitWizardModel) IgnoreCursor() int { return w.ignoreCursor }

// MoveGit shifts the Git highlight, stopping at either end like the
// approval dialog: the affirmative option is never reached by wrap-around.
func (w *InitWizardModel) MoveGit(delta int) {
	next := w.gitCursor + delta
	if next < wizardGitYes {
		next = wizardGitYes
	}
	if next > wizardGitQuit {
		next = wizardGitQuit
	}
	w.gitCursor = next
}

// MoveIgnore shifts the ignore highlight, clamped like MoveGit.
func (w *InitWizardModel) MoveIgnore(delta int) {
	next := w.ignoreCursor + delta
	if next < wizardIgnoreYes {
		next = wizardIgnoreYes
	}
	if next > wizardIgnoreNo {
		next = wizardIgnoreNo
	}
	w.ignoreCursor = next
}

// SetError shows a commit failure on the current step without losing the
// typed values, so a retry keeps what was entered.
func (w *InitWizardModel) SetError(text string) { w.errMsg = text }

// Reopen returns a finished wizard to its final step after a failed commit,
// so the user can retry, step back, or quit instead of stranding on a done
// screen that swallowed its keys.
func (w *InitWizardModel) Reopen() tea.Cmd {
	w.done = false
	w.step = WizardStepProject
	return w.project.Focus()
}

// Update routes one message to the wizard. Keys drive the step machine;
// cursor blinks go to the focused input. Anything else is ignored.
func (w *InitWizardModel) Update(msg tea.Msg) (*InitWizardModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return w.handleKey(msg)
	case cursor.BlinkMsg:
		return w.updateInputs(msg)
	}
	return w, nil
}

func (w *InitWizardModel) handleKey(msg tea.KeyMsg) (*InitWizardModel, tea.Cmd) {
	if w.done || w.canceled {
		return w, nil
	}
	if msg.String() == "ctrl+c" {
		w.canceled = true
		return w, nil
	}
	if w.step == WizardStepGit {
		return w.handleGitKey(msg)
	}
	if w.step == WizardStepIgnore {
		return w.handleIgnoreKey(msg)
	}
	return w.handleTextKey(msg)
}

// handleGitKey drives the repository choice: arrows move, enter confirms
// the highlight, y/n/q answer directly.
func (w *InitWizardModel) handleGitKey(msg tea.KeyMsg) (*InitWizardModel, tea.Cmd) {
	switch msg.String() {
	case "left", "h", "up", "shift+tab":
		w.MoveGit(-1)
		return w, nil
	case "right", "l", "down", "tab":
		w.MoveGit(1)
		return w, nil
	case "y", "Y":
		w.chooseGit(wizardGitYes)
		return w, w.advance()
	case "n", "N":
		w.chooseGit(wizardGitNo)
		return w, w.advance()
	case "q", "Q", "esc":
		w.canceled = true
		return w, nil
	case "enter", " ":
		w.chooseGit(w.gitCursor)
		return w, w.advance()
	}
	return w, nil
}

// chooseGit records the decision without performing it: git init runs in
// Complete, after every decision is collected.
func (w *InitWizardModel) chooseGit(choice int) {
	switch choice {
	case wizardGitYes:
		w.wantGit = true
	case wizardGitNo:
		w.wantGit = false
	default:
		w.canceled = true
	}
}

// handleIgnoreKey drives the runtime-state choice. Esc steps back (to Git when
// that step exists, otherwise out of onboarding); q quits outright.
func (w *InitWizardModel) handleIgnoreKey(msg tea.KeyMsg) (*InitWizardModel, tea.Cmd) {
	switch msg.String() {
	case "left", "h", "up", "shift+tab":
		w.MoveIgnore(-1)
		return w, nil
	case "right", "l", "down", "tab":
		w.MoveIgnore(1)
		return w, nil
	case "y", "Y":
		w.chooseIgnore(wizardIgnoreYes)
		return w, w.advance()
	case "n", "N":
		w.chooseIgnore(wizardIgnoreNo)
		return w, w.advance()
	case "q", "Q":
		w.canceled = true
		return w, nil
	case "esc":
		return w, w.back()
	case "enter", " ":
		w.chooseIgnore(w.ignoreCursor)
		return w, w.advance()
	}
	return w, nil
}

// chooseIgnore records the ignore decision without touching .gitignore.
func (w *InitWizardModel) chooseIgnore(choice int) {
	w.wantIgnore = choice == wizardIgnoreYes
}

// handleTextKey confirms or edits the identity/project inputs. Enter with
// an emptied field confirms the default, mirroring the stdin prompt; esc
// steps back without losing typed values.
func (w *InitWizardModel) handleTextKey(msg tea.KeyMsg) (*InitWizardModel, tea.Cmd) {
	switch msg.String() {
	case "enter":
		return w.confirmText()
	case "esc":
		return w, w.back()
	}
	return w.updateInputs(msg)
}

// confirmText validates the current input and advances. An emptied field
// falls back to its default; an empty result is a validation error, not a
// commit, so the step stays open.
func (w *InitWizardModel) confirmText() (*InitWizardModel, tea.Cmd) {
	input := w.focusedInput()
	if strings.TrimSpace(input.Value()) == "" {
		def := w.workerDef
		if w.step == WizardStepProject {
			def = w.projectDef
		}
		input.SetValue(def)
		w.storeFocusedInput(*input)
	}
	if strings.TrimSpace(input.Value()) == "" {
		if w.step == WizardStepIdentity {
			w.errMsg = "Enter a name so OHGO knows what to call you."
		} else {
			w.errMsg = "Enter a project name — it labels this project."
		}
		return w, nil
	}
	w.errMsg = ""
	return w, w.advance()
}

// advance moves to the next step, parking focus on its input. The final
// advance lands on Done with every input blurred.
func (w *InitWizardModel) advance() tea.Cmd {
	w.errMsg = ""
	switch w.step {
	case WizardStepGit:
		if w.askIgnore {
			w.step = WizardStepIgnore
			return nil
		}
		w.step = WizardStepIdentity
		return w.identity.Focus()
	case WizardStepIgnore:
		w.step = WizardStepIdentity
		return w.identity.Focus()
	case WizardStepIdentity:
		w.step = WizardStepProject
		w.identity.Blur()
		return w.project.Focus()
	case WizardStepProject:
		w.step = WizardStepDone
		w.project.Blur()
		w.done = true
		return nil
	}
	return nil
}

// back returns to the previous step, keeping typed values. Backing off the
// first step cancels: there is nowhere earlier to go, and nothing has been
// committed yet.
func (w *InitWizardModel) back() tea.Cmd {
	w.errMsg = ""
	switch w.step {
	case WizardStepProject:
		w.step = WizardStepIdentity
		w.project.Blur()
		return w.identity.Focus()
	case WizardStepIdentity:
		w.identity.Blur()
		if w.askIgnore {
			w.step = WizardStepIgnore
			return nil
		}
		if w.askGit {
			w.step = WizardStepGit
			return nil
		}
		w.canceled = true
		return nil
	case WizardStepIgnore:
		if w.askGit {
			w.step = WizardStepGit
			return nil
		}
		w.canceled = true
		return nil
	case WizardStepGit:
		w.canceled = true
		return nil
	}
	return nil
}

// focusedInput returns the input owning the keyboard on a text step.
func (w *InitWizardModel) focusedInput() *textinput.Model {
	if w.step == WizardStepProject {
		return &w.project
	}
	return &w.identity
}

// storeFocusedInput writes back an input mutated through a pointer.
func (w *InitWizardModel) storeFocusedInput(input textinput.Model) {
	if w.step == WizardStepProject {
		w.project = input
	} else {
		w.identity = input
	}
}

// updateInputs forwards a message to the focused input.
func (w *InitWizardModel) updateInputs(msg tea.Msg) (*InitWizardModel, tea.Cmd) {
	if w.step != WizardStepIdentity && w.step != WizardStepProject {
		return w, nil
	}
	input := w.focusedInput()
	updated, cmd := input.Update(msg)
	w.storeFocusedInput(updated)
	return w, cmd
}

// stepPosition reports the "Step N of M" indicator for the current step.
func (w *InitWizardModel) stepPosition() (index, total int) {
	steps := []WizardStep{}
	if w.askGit {
		steps = append(steps, WizardStepGit)
	}
	if w.askIgnore {
		steps = append(steps, WizardStepIgnore)
	}
	steps = append(steps, WizardStepIdentity, WizardStepProject)
	for i, s := range steps {
		if s == w.step {
			return i + 1, len(steps)
		}
	}
	return len(steps), len(steps)
}

// View renders the centered dialog card. Width is the terminal width; the
// caller (Program.View) owns the header, the vertical centering, and the
// footer, so the card never covers the chrome.
func (w *InitWizardModel) View(width int) string {
	cardW := 60
	if width > 0 {
		cardW = width - 4
		if cardW > 60 {
			cardW = 60
		}
		if cardW < 30 {
			cardW = 30
		}
	}
	index, total := w.stepPosition()
	title := lipgloss.NewStyle().
		Foreground(lipgloss.Color(wizardTeal)).Bold(true).
		Render("Project setup")
	steps := lipgloss.NewStyle().
		Foreground(lipgloss.Color(wizardGreen)).
		Render(stepLabel(w.step, index, total))

	var body string
	switch w.step {
	case WizardStepGit:
		body = w.gitBody()
	case WizardStepIgnore:
		body = w.ignoreBody()
	case WizardStepIdentity:
		backHint := "esc quit"
		if w.askGit || w.askIgnore {
			backHint = "esc back"
		}
		body = w.textBody("What should OHGO call you for this project?", "Name", w.identity, cardW,
			"enter confirm · "+backHint)
	default:
		body = w.textBody("What is this project called?", "Project", w.project, cardW,
			"enter confirm · esc back")
	}

	inner := lipgloss.JoinVertical(lipgloss.Left, title, steps, "", body)
	if w.errMsg != "" {
		errLine := lipgloss.NewStyle().
			Foreground(lipgloss.Color(wizardAmber)).
			Render(sanitizeInline(w.errMsg))
		inner = lipgloss.JoinVertical(lipgloss.Left, inner, "", errLine)
	}
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(wizardTeal)).
		Background(lipgloss.Color(wizardBG)).
		Padding(1, 2).
		Width(cardW).
		Render(inner)
}

// stepLabel names the current step for the "Step N of M" indicator.
func stepLabel(step WizardStep, index, total int) string {
	name := "Project"
	switch step {
	case WizardStepGit:
		name = "Git"
	case WizardStepIgnore:
		name = "Runtime"
	case WizardStepIdentity:
		name = "Identity"
	}
	return "Step " + strconv.Itoa(index) + " of " + strconv.Itoa(total) + " · " + name
}

// gitBody renders the repository question with badge toggle buttons.
func (w *InitWizardModel) gitBody() string {
	alert := lipgloss.NewStyle().
		Foreground(lipgloss.Color(wizardAmber)).
		Render("This project is not a Git repository.")
	question := lipgloss.NewStyle().
		Foreground(lipgloss.Color(wizardGrey)).
		Render("Initialize Git on main?")
	buttons := lipgloss.JoinHorizontal(lipgloss.Top,
		gitButton("Yes", w.gitCursor == wizardGitYes),
		"   ",
		gitButton("No", w.gitCursor == wizardGitNo),
		"   ",
		gitButton("Quit", w.gitCursor == wizardGitQuit),
	)
	hints := lipgloss.NewStyle().
		Foreground(lipgloss.Color(wizardGrey)).
		Render("←→ move · enter confirm · y/n/q shortcut")
	return lipgloss.JoinVertical(lipgloss.Left, alert, question, "", buttons, "", hints)
}

// ignoreBody renders the runtime-state question with the same badge toggles as
// the Git step, so onboarding keeps one interaction model.
func (w *InitWizardModel) ignoreBody() string {
	lead := lipgloss.NewStyle().
		Foreground(lipgloss.Color(wizardGrey)).
		Render("ohgo stores local runtime state in .ohgo/.")
	question := lipgloss.NewStyle().
		Foreground(lipgloss.Color(wizardGrey)).
		Render("Keep this state out of Git?")
	buttons := lipgloss.JoinHorizontal(lipgloss.Top,
		gitButton("Add .ohgo/ to .gitignore", w.ignoreCursor == wizardIgnoreYes),
		"   ",
		gitButton("Leave unchanged", w.ignoreCursor == wizardIgnoreNo),
	)
	hints := lipgloss.NewStyle().
		Foreground(lipgloss.Color(wizardGrey)).
		Render("←→ move · enter confirm · y/n shortcut · esc back")
	return lipgloss.JoinVertical(lipgloss.Left, lead, question, "", buttons, "", hints)
}

// textBody renders one labeled input with its focus ring.
func (w *InitWizardModel) textBody(question, label string, input textinput.Model, cardW int, hints string) string {
	q := lipgloss.NewStyle().
		Foreground(lipgloss.Color(wizardGrey)).
		Render(question)
	input.PromptStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(wizardBlue))
	input.TextStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(wizardGrey))
	input.PlaceholderStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(wizardBlue))
	input.Cursor.Style = lipgloss.NewStyle().Foreground(lipgloss.Color(wizardTeal))
	viewWidth := cardW - 12 - len([]rune(label))
	if viewWidth < 10 {
		viewWidth = 10
	}
	if viewWidth > 40 {
		viewWidth = 40
	}
	input.Width = viewWidth
	row := lipgloss.NewStyle().
		Foreground(lipgloss.Color(wizardBlue)).
		Render(label+" › ") + input.View()
	frame := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(wizardTeal)).
		Padding(0, 1).
		Render(row)
	hintLine := lipgloss.NewStyle().
		Foreground(lipgloss.Color(wizardGrey)).
		Render(hints)
	return lipgloss.JoinVertical(lipgloss.Left, q, "", frame, "", hintLine)
}

// gitButton renders one toggle badge: a solid teal badge when active, a
// quiet grey label otherwise, so the choice never depends on hue alone —
// the brackets and the highlight both mark it.
func gitButton(label string, active bool) string {
	text := "[ " + label + " ]"
	if active {
		return lipgloss.NewStyle().
			Foreground(lipgloss.Color(wizardBG)).
			Background(lipgloss.Color(wizardTeal)).
			Bold(true).
			Render(text)
	}
	return lipgloss.NewStyle().
		Foreground(lipgloss.Color(wizardGrey)).
		Render(text)
}

// SetInitWizard attaches onboarding to a Program. The wizard owns the
// keyboard until it completes or is cancelled; header and footer keep
// rendering from state throughout.
func (p *Program) SetInitWizard(w *InitWizardModel, hooks InitWizardHooks) {
	p.wizard = w
	p.wizardHooks = hooks
}

// handleWizardKey routes one key to the wizard and folds its terminal
// transitions: cancel quits without committing, completion commits through
// the owner's hook and then drops the modal, landing on the idle canvas in
// the same frame — no screen clear, no re-initialization.
func (p *Program) handleWizardKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	updated, cmd := p.wizard.Update(msg)
	p.wizard = updated
	if p.wizard.Canceled() {
		p.wizard = nil
		if p.wizardHooks.Cancel != nil {
			return p, p.wizardHooks.Cancel()
		}
		return p, tea.Quit
	}
	if p.wizard.Done() {
		decision := p.wizard.Decision()
		if p.wizardHooks.Complete == nil {
			p.wizard = nil
			return p, cmd
		}
		facts, notice, err := p.wizardHooks.Complete(decision)
		if err != nil {
			p.wizard.SetError(err.Error())
			return p, p.wizard.Reopen()
		}
		p.wizard = nil
		p.state.Apply(facts)
		if notice != "" {
			p.state.Apply(Notice{Text: notice})
		}
		return p, nil
	}
	return p, cmd
}

// renderWizardFrame keeps full-screen continuity during onboarding: the
// header and footer render exactly as in the idle canvas, and the dialog
// card is centered in the rows between them.
func (p *Program) renderWizardFrame() string {
	pal := PaletteFor(p.state.Theme)
	layout := ComputeLayout(p.state)
	var sb strings.Builder
	sb.WriteString(renderHeader(p.state, pal, layout) + "\n")
	sb.WriteString(SeparatorLine(separatorWidth(p.state, layout), pal) + "\n")
	width := p.state.Width
	if width <= 0 {
		width = 80
	}
	card := p.wizard.View(width)
	if p.state.Height > 0 {
		avail := p.state.Height - 3 // header, separator, status
		if avail < 1 {
			avail = 1
		}
		sb.WriteString(lipgloss.Place(width, avail, lipgloss.Center, lipgloss.Center, card))
	} else {
		sb.WriteString(card)
	}
	sb.WriteString("\n" + renderStatus(p.state, pal, layout))
	return sb.String()
}
