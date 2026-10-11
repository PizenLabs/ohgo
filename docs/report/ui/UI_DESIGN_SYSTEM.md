# ohgo UI Design System

An original, terminal-native visual and interaction language. Per spec §13,
brainless is a **pattern reference only** — no component, glyph, palette, label,
or layout is copied from it. Nothing here introduces a dependency: the system is
expressed with the existing `lipgloss` tokens in `tui/style.go`.

---

## 1. Design character

A precise engineering instrument. Calm, dense, honest.

- **Content-first** — the conversation is the screen. No splash, no dashboard, no
  permanent sidebar, no badges for their own sake.
- **Quiet confidence** — identity appears once, in the header, and nowhere else.
- **Evidence over narration** — a tool card and its real result outrank a model's
  claim about what it did.
- **Truthful** — nothing animates, counts, or completes unless the runtime said so.

## 2. Semantic tokens

Defined once in `tui/style.go`; no literal colour appears at a call site.

| Role | Token | Notes |
| --- | --- | --- |
| identity / focus / selection | `darkAccent` 121 (mint) | the single accent |
| body text | `darkText` 252 | |
| metadata, hints, separators | `darkMuted` 244 | |
| tool identity | `darkTool` 73 | distinct from the accent on purpose |
| success / warning / danger | 42 / 214 / 196 | |
| code surface | fg 228 on bg 236 | |
| headings | emerald 85 / sage 114 / muted sage 144 | depth reads without parsing |

A light theme mirrors the same semantics. An unknown theme name degrades to
dark rather than erroring.

**Rules**

- Colour reinforces meaning and is never the only signal. Every state that uses
  colour also carries a word or a glyph (`✓ ✗ ● ○ ◐ ■`, `running`/`done`/`error`).
- 256-colour indexes, not truecolor, so they degrade predictably.
- Routine metadata is never coloured brightly.
- Whole paragraphs are never coloured; a marker or label suffices.

## 3. Status glyphs

| Phase | Glyph | Word |
| --- | --- | --- |
| idle | `○` | idle |
| working / streaming | `●` | working · streaming |
| running tool | `●` | running tool |
| awaiting approval | `◐` | awaiting approval |
| shutting down | `■` | shutting down |

These are ohgo's own set, chosen for wide terminal coverage and paired with words
so hue is never load-bearing.

## 4. Layout

Four regions, in order, sized by `ComputeLayout`:

```
header            identity · model · phase · connection      (1–2 rows)
────────────────────────────────────────────── separator
transcript        ConversationHeight rows exactly
[approval modal]  when a tool is blocked
status            context · session · cwd · usage           (0–1 rows)
────────────────────────────────────────────── separator
composer          > input                                    (ComposerLines rows)
hints             state-dependent, only when it fits
```

**Responsive tiers** (`tui/layout.go`): wide ≥100, medium 70–99, narrow <70,
mini <40×8.

- The transcript emits **exactly** its budget: entry separators are blank rows
  *inside* the budget, never extra rows past it. Overflow trims from the top so
  the live edge stays visible.
- Suggestion rows carry their own budget and yield first on short screens.
- An open picker releases the composer rows it hides.
- **Mini** concedes the viewport with a plain, truncated notice rather than
  pretending to be a workbench.
- Overlays take over completely — transcript and composer hidden — so streaming
  output never fights a moving cursor.

## 5. Composer

Two restrained rules around one input line, plus an optional state-dependent hint
row. It is not a card and does not frame itself. The focused state is signalled
by the accent cursor, not by a heavy border.

Multiline via `alt+enter` / `ctrl+j`; history with draft preservation; queueing
while busy; `/` opens inline discovery filtered from the real command registry.

## 6. Approval

The one place borders are justified, because it is a human decision boundary:
it shows the tool, the target, and the actions the runtime actually permits.
The default selection is always the **conservative** choice (last, i.e. "No").

The composer and its hint row must **not** render behind the dialog advertising
keys the dialog has captured — see BUG-006.

## 7. Mouse, wheel, and selection

The wheel and text selection are mutually exclusive by construction: a terminal
either sends mouse events to the program, or performs its own selection. Never
both. ohgo therefore resolves the mode from the **environment**, and leaves an
explicit override:

| Context | Default | Drag-select |
| --- | --- | --- |
| plain terminal (no multiplexer) | `select` — alternate scroll (DEC 1007) | works, no modifier |
| inside tmux (`$TMUX` set) | `report` — real mouse events (`?1002`) | needs Shift / Option |

The asymmetry is measured, not assumed. tmux 3.7c delivers **nothing** to a pane
that enables alternate scroll alone — verified with a program independent of
ohgo, across alt-screen and normal screen, with `mouse off` and with the mode
emitted before and after alt-screen. Only a pane that requests mouse reporting
is forwarded to. Defaulting to `select` inside tmux would leave the wheel dead.

`--mouse select|report`, or `"mouse"` in `~/.ohgo/settings.json`, always wins.
An unrecognised value degrades to `select`, because silently enabling reporting
would take away drag-copy over a typo — the capability a user notices losing.

Because the terminal cannot say which arrows it synthesised, `wheelScrolls`
decides by state:

| State | Arrows do |
| --- | --- |
| idle, empty composer | scroll the transcript (the reading state) |
| text in the composer | move the cursor / recall history, unchanged |
| picker, approval, secret open | belong to the overlay, unchanged |

History recall stays on `ctrl+p` / `ctrl+n` in every state.

## 8. Markdown

Rendered by `tui/markdown.go`, dependency-free: fences, inline code, headings,
emphasis, links, lists (including nesting and hanging indents), quotes, rules,
and tables with alignment and a stacked narrow fallback.

Two invariants govern it:

1. **One wrap ownership.** Plain cells are measured and folded first; ANSI is
   applied after. Styling can never inflate the budget.
2. **The terminal is the unit.** A tab, a CJK glyph, or a combining mark is
   measured as the cells it occupies, never as bytes or runes. This is what
   BUG-001 violated.

Partial streaming constructs render provisionally — unclosed markers stay
literal — and converge on completion, so rendering is a pure function of the
accumulated text and chunk splits cannot diverge it (verified, see UI_AUDIT §2).

## 9. Not doing

Deliberately excluded per spec §3: React or any new frontend, a permanent
sidebar or dashboard, fake progress percentages, rotating activity verbs, ASCII
branding, decorative borders around every element, and any UI-owned execution
state machine.