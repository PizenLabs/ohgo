# OHGO Design System

**Quiet by design. Colour with purpose.**

This document is the canonical contract for OHGO's visual identity, semantic
tokens, and appearance resolution. It describes the implemented terminal UI,
not a future design. Exact values live in
`internal/tui/design/tokens.go`; this document explains their jobs.

## 1. Identity and visual language

OHGO uses a neutral-first palette. Most of the interface is built from text
hierarchy, spacing, and low-contrast surfaces. Colour is reserved for identity,
state, and document structure; it never decorates ordinary metadata.

The interface is not monochrome. Four restrained accent families give a report
a navigable rhythm while the neutral base keeps long sessions comfortable:

| Family | Job | Character |
| --- | --- | --- |
| Structure | Markdown headings, table headers, quickstart legend | Violet |
| Emphasis | Bold lead-ins, key inline claims | Warm gold |
| Reference | Inline code, diff headers, phase spans | Steel blue |
| Reasoning | Thought disclosure and reasoning prose | Amber |

The two appearances share one semantic architecture but are calibrated
independently:

| Appearance | Direction | Character |
| --- | --- | --- |
| Dark | Midnight Slate | Cool near-black canvas, soft slate text, quiet violet/gold/blue accents |
| Light | Warm Porcelain | Barely-warm near-white canvas, cool ink text, deepened accents |

The terminal owns the canvas background. OHGO paints a surface only for a
selection row, a modal card, a code block, or a diff stripe.

## 2. Two independent colour systems

**Authority identity** is product identity, not theme chrome. It is owned by
`internal/control/workspace` and is fixed across appearances; only its depth
adapts for contrast.

| Authority | Hue | Dark | Light |
| --- | --- | --- | --- |
| ASK | Teal | `#5FB3A1` | `#1F6F61` |
| BUILD | Blue | `#61AFEF` | `#2B6CB0` |
| FUZZ | Magenta/coral | `#E06C75` | `#C53030` |

These three hex pairs are the identity contract. They are not theme chrome and
must not be re-tuned as palette decoration.

**Operational status** is separate and never replaces authority identity:

| Role | Meaning | Dark | Light |
| --- | --- | --- | --- |
| Success | Completed successfully | `#7FC29B` | `#2C7A55` |
| Warning | Degraded / needs a human | `#E0A465` | `#96551E` |
| Error | Failed operation | `#E49A9A` | `#A94545` |
| Running | Active, progressing | `#8FB4D9` | `#3C6E96` |
| Info | Informational emphasis | `#8FB4D9` | `#3C6E96` |

A successful FUZZ command keeps FUZZ magenta for the authority and carries the
success glyph/colour for the outcome. A failed ASK turn stays teal.

**Document accents** are semantic, not decorative. They answer a different
question than status: what kind of text is this?

| Role | Job | Dark | Light |
| --- | --- | --- | --- |
| `Heading` | Markdown headings, table headers, quickstart legend | `#CFC7FA` | `#443B86` |
| `Emphasis` | Bold lead-ins and key inline claims | `#E8CE9B` | `#7A5719` |
| `ListMarker` | List glyphs; a quieter relative of `Heading` | `#8F8FC9` | `#6B62A8` |
| `InlineCode` | Inline paths, filenames, and config values | `#D8E4F5` on `#1B2431` | `#23405C` on `#E7EDF4` |
| `ThoughtHeader` | Reasoning disclosure | `#DFB878` | `#8A6420` |
| `ThoughtBody` | Reasoning prose | `#C2A468` | `#6E5520` |

Warm gold emphasis and amber reasoning are deliberately separate values: gold
marks the author's claim, amber marks the model's internal cognition, and
orange warning marks degradation. None borrows an authority hue.

## 3. Focus hierarchy

Every surface is composed from three deliberate focus tiers. The tiers are
resolved from tokens, never chosen per component. Document accents add hue to
the High Focus tier; they do not create a fourth brightness ladder.

| Tier | Purpose | Dark | Light |
| --- | --- | --- | --- |
| High Focus | Application brand, dialog titles, greeting | `#E9EDF7` bold | `#12161D` bold |
| High Focus (document) | Headings, table headers, bold lead-ins | Heading / Emphasis accents, bold | Heading / Emphasis accents, bold |
| Medium Focus | Commands, tool invocations, answer prose, descriptions | `#C2CBDB` | `#262C36` |
| Support | Descriptions, labels | `#9DA9BD` | `#4A5361` |
| Low Focus | Timings, key hints, shortcuts, supporting metadata | `#4C5769` | `#9AA2AE` |

- `TextTitle` is High Focus chrome. `TextPrimary` is Medium Focus. `TextSecondary`
  is the support form of Medium Focus. `TextFaint` is Low Focus.
- `TextMuted` remains the quiet-but-readable role. It clears 4.5:1 and is used
  wherever the user must still be able to act on the text (footer context,
  warnings that are not critical, placeholders).
- `TextFaint` is intentionally below the 4.5:1 normal-text floor (measured at
  roughly 2.6:1 dark and 2.4:1 light). It is therefore reserved for metadata
  that is useful but not required: measured durations, `ctrl+o` hints, and
  quickstart hint text. Essential information never uses it.
- `tokens_test.go` enforces that the tiers stay ordered (`title > primary >
  faint`) in both appearances, and that heading, emphasis, list marker, and body
  copy stay distinct values.

Measured contrast against each canvas:

| Role | Dark | Light |
| --- | --- | --- |
| Title | 16.13:1 | 16.89:1 |
| Body | 11.57:1 | 13.08:1 |
| Secondary | 7.96:1 | 7.24:1 |
| Muted | 5.48:1 | 5.01:1 |
| Faint | 2.59:1 | 2.40:1 |
| Heading | 11.88:1 | 8.83:1 |
| Emphasis | 12.36:1 | 6.10:1 |
| List marker | 6.25:1 | 4.97:1 |
| Inline code pill | 12.16:1 | 9.09:1 |

In the light appearance a deep violet heading carries less luminance contrast
than body ink but more hue contrast; bold weight plus document spacing is what
makes it the first thing the eye lands on. It is never rendered at body weight.

## 4. Thought presentation

Reasoning is a cognitive stop, not a warning and not a second answer. It has
its own amber pair:

| Role | Dark | Light | Weight |
| --- | --- | --- | --- |
| Thought header (`+ Thought · 1.2s`) | `#DFB878` | `#8A6420` | Bold |
| Thought body | `#C2A468` | `#6E5520` | Italic |

The body stays behind the dim `│` guide, so expansion still reads as an
annotation of the disclosure row. Operational warning is a warmer orange
(`#E0A465` / `#96551E`); a thought never borrows it merely because the label is
"Thought".

## 5. Token source of truth

`internal/tui/design/tokens.go` is the single authoritative value source.
`internal/tui` and `internal/tui/diff` both consume it. Components resolve
semantic roles; they do not define local palettes.

Surfaces:

- `SurfaceBackground` — the canvas pair the text is calibrated against.
- `SurfacePrimary` / `SurfaceSecondary` — content and secondary regions.
- `SurfaceElevated` — modal/onboarding cards.
- `SurfaceInput` — documented input surface; the composer intentionally
  inherits the terminal background rather than painting a box.
- `SurfaceSelection` — selected list row and approval choice.

Text: `TextTitle`, `TextPrimary`, `TextSecondary`, `TextMuted`,
`TextDisabled`, `TextFaint`, `TextInverse`.

Borders: `BorderSubtle`, `BorderDefault`, `BorderStrong`, `BorderFocus`.

Chrome: `Brand`, `BrandMute`, `Model`, `Check`, `Active`, `Prompt`, `User`,
`ToolName`, `Recap`, `Idle`, `Info`.

Document: `Heading`, `Emphasis`, `ListMarker`, `ThoughtHeader`, `ThoughtBody`.

Semantic: `Success`, `Warning`, `Error`, `Running`.

Content: `CodeForeground`, `CodeBackground`, `CodeString`, `CodeKeyword`,
`InlineCodeForeground`, `InlineCodeBackground`, `Quote`, plus the diff and
support accent roles.

`internal/tui/design/tokens_test.go` measures WCAG contrast for normal text,
disabled text, focus, code, selection, and diff pairs, enforces the three focus
tiers, and verifies that the report accents stay distinct. These checks are a
calibration guard, not a claim of full terminal-theme conformance.

## 6. Presentation conventions

- The header brand, greeting, and dialog titles use `TextTitle` bold. They are
  the strongest chrome text; they do not compete on hue with the authority
  modes.
- Markdown headings (`#`, `##`, `###`) use the violet `Heading` accent, bold.
  Table headers use the same accent. A bold lead-in such as
  `**Project Analysis:**` uses the gold `Emphasis` accent, so a section prefix
  outranks the explanation on the same line without becoming a second heading.
- Paragraph, list, and table body text is `TextPrimary` at regular weight.
  Inline code (`src/`, `go.mod`, `docker-compose.yml`) takes the inline-code
  pill: a cool tinted surface with pale blue text, distinct from both the
  sentence and a fenced block. Bullet glyphs use `ListMarker`; item text stays
  body copy.
- The quickstart card's legend ("Quickstart") uses `Heading`; the rule around
  it stays `BorderDefault`, so the category label has gravity without a bright
  frame. The greeting above it remains `TextTitle`.
- A completed tool row colours the status glyph, not the command. Command text
  is `TextPrimary`; measured durations and `ctrl+o` hints use `TextFaint`. A
  failed command colours its glyph and branch marker, while the reason remains
  readable body text.
- A tool-run phase span (`[Phase: BUILD]`) takes the `Info` blue while its tool
  count and duration stay `TextMuted`, so the row has one structural anchor
  without becoming a status claim.
- A `needs review` task row and its reason use `Warning`: a human decision is
  pending, and the row must not hide among muted telemetry.
- A direct shell block follows the same rule: the command is `TextPrimary`, the
  exit code is `TextMuted`, and the measured duration is `TextFaint`.
- Reasoning uses the amber thought pair: a bold `ThoughtHeader` disclosure row
  and an italic `ThoughtBody` behind the dim guide.
- Descriptions in the quickstart use `TextSecondary`; taught affordances
  (`/model`, `/help`, `@files`, `$sup`) use `TextTitle`; their hints use
  `TextFaint`.
- Authority identity is confined to the prompt word, the footer badge, and
  authority-specific dialogs. It never paints a whole panel.
- Every coloured state also carries a text marker or word, so a monochrome
  terminal remains understandable.
- Dark and Light use the same markup and hierarchy; only resolved tokens differ.

## 7. Appearance preference and resolution

The stored setting is a **preference**, never an effective palette:

| Preference | Meaning |
| --- | --- |
| `auto` | Follow the host appearance. This is the default for new and unset settings. |
| `dark` | Force Midnight Slate. |
| `light` | Force Warm Porcelain. |

Migration rules:

- An omitted `theme` field resolves to `auto`.
- An existing explicit `dark` or `light` is preserved.
- An invalid value falls back deterministically to `auto`.
- The parser is `config.NormalizeThemePreference`; there is no second parser.
- Writes stay atomic through `config.Save`.

Resolution is centralized in `Program.setThemePreference`:

1. The preference is normalized.
2. Explicit choices resolve directly; Auto asks the host detector once.
3. The effective appearance is stored in `State.Theme`.
4. The source (`system`, `explicit`, or `fallback`) is stored separately.
5. Components render from resolved tokens only.

A system change never rewrites `auto`. While Auto is active, a bounded probe
runs at startup and every 10 seconds; an explicit choice disables probing.

## 8. System detection and fallback

Detection is best-effort and bounded to 750 ms per command. It never runs in a
render path.

| Platform | Source |
| --- | --- |
| macOS | `defaults read -g AppleInterfaceStyle`; an absent key is the light Aqua appearance |
| GNOME/Linux | `gsettings` `color-scheme`, then `gtk-theme` |
| KDE/Linux | `kreadconfig6`/`kreadconfig5` `ColorScheme` |
| Windows | `HKCU\...\Themes\Personalize\AppsUseLightTheme` |
| SSH | Unknown by design: the process cannot see the client desktop |
| Unsupported/headless | Unknown |

When detection is unknown, Auto resolves to Dark and is labelled
`fallback`, never `system`. tmux is treated as local: the host preference is
still readable even though the multiplexer owns the screen.

A terminal emulator can override its own colours independently of the desktop
appearance; OHGO cannot detect that and does not claim to.

## 9. `/theme`

Bare `/theme` opens a three-row chooser: Auto, Dark, Light. The current
preference is marked, and the card states the effective appearance and source:

```text
Theme: Auto → Dark (system)
Theme: Auto → Dark (fallback: system unavailable)
Theme: Light (explicit)
```

Selecting a row applies immediately, persists the preference, and reflows the
cached transcript without resetting the session, draft, or runtime.

## 10. Terminal compatibility

Styles are truecolor hex where calibration matters and degrade through the
terminal's colour profile. No information depends on colour alone, motion is
optional, `NO_COLOR`/`REDUCED_MOTION` fall back to static text, and every surface
is measured before styling so ANSI never affects layout width.
