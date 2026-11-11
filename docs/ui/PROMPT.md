# MASTER IMPLEMENTATION PROMPT
# OHGO — Unified Visual Identity & Complete UI/UX Refinement

**Project:** OHGO — Human-Controlled Agent Runtime for Real Codebases  
**Scope:** Product-wide UI/UX, Visual Identity, Theme Architecture, Dark/Light Appearance, and Terminal Experience  
**Execution:** Two sequential implementation phases with independent acceptance gates  
**Priority:** Product identity, consistency, usability, correctness, and implementation quality

---

# 1. MISSION

Act as a Principal Product Designer, Senior UX Engineer, and Staff-Level Software Engineer responsible for establishing and implementing the complete visual identity and UI/UX foundation of OHGO.

Your mission is not simply to make the existing interface look better.

Your mission is to establish a distinctive, coherent, professional, and recognizable design language that makes someone encountering OHGO for the first time immediately feel:

**"This is OHGO."**

That recognition must come from the consistency and quality of the entire experience:

- Visual identity.
- Color language.
- Typography.
- Layout and information hierarchy.
- Command and output presentation.
- Agent execution feedback.
- Interactive states.
- Terminal-native ergonomics.
- Dark and Light appearances.
- Theme behavior and transitions.
- Consistency across all user-facing interfaces.

The result must feel like one carefully designed product rather than a collection of independently styled components.

OHGO should communicate:

- Professionalism.
- Clarity.
- Technical maturity.
- Reliability.
- Human control.
- Operational transparency.
- Quiet confidence.

The design philosophy is:

**Quiet by design. Clear by nature.**

Do not pursue visual novelty for its own sake.

Do not introduce unnecessary decorations, visual effects, panels, colors, or abstractions.

Every visual decision must improve identity, readability, usability, or operational clarity.

---

# 2. PRODUCT IDENTITY — NON-NEGOTIABLE CONSTRAINTS

Before making any changes, understand and preserve the following product identity.

## 2.1 OHGO's Three Signature Mode Colors

These are established identity elements of OHGO.

They are intentional and must never be treated as arbitrary accent colors.

| Mode | Signature Color |
|---|---|
| ASK | Teal |
| FUZZ | Magenta |
| BUILD | Blue |

These three identities must be preserved throughout the entire implementation.

Requirements:

1. ASK must remain Teal.
2. FUZZ must remain Magenta.
3. BUILD must remain Blue.
4. Their semantic meaning must never change.
5. They must remain recognizable in both Dark and Light appearances.
6. Their identity must not be replaced by the general brand color.
7. Operational status colors must never overwrite mode identity.

You may introduce carefully calibrated foreground, background, muted, and surface variants to maintain readability across appearances.

However, preserve the original signature colors as canonical identity tokens. Do not arbitrarily redefine their hues.

The three mode colors are not the complete brand identity. They are part of a broader visual system.

## 2.2 Separate Mode Identity from Operational Status

Establish two independent semantic systems.

**Mode identity:**

- ASK — Teal.
- FUZZ — Magenta.
- BUILD — Blue.

**Operational status:**

- Success — Restrained green.
- Warning — Muted amber.
- Error — Soft red.
- Running — Subtle active indicator.
- Idle — Neutral.
- Informational content — Appropriate neutral or informational treatment.

Never confuse these systems.

For example:

When FUZZ successfully executes a command:

- The FUZZ identity remains Magenta.
- The execution success indicator uses the success color.

When ASK encounters an error:

- ASK remains Teal.
- The error state uses the error color.

The meaning of a color must remain consistent throughout OHGO.

Do not use color as decoration.

## 2.3 Existing Product Behavior Must Be Preserved

This is a UI/UX-focused project.

Do not unnecessarily modify:

- Agent orchestration.
- Execution semantics.
- Tool protocols.
- Core runtime behavior.
- Session management.
- Task lifecycle.
- Backend business logic.
- Existing user workflows.
- Existing interaction contracts.

A visual refinement must never silently change runtime behavior.

All displayed operational information must come from the actual application state and underlying runtime.

Never invent execution states, success indicators, error information, metrics, progress, or capabilities to make the interface appear more polished.

If information is unavailable, represent that limitation honestly.

---

# 3. EXECUTION STRATEGY

Implement the complete project in exactly two sequential phases.

## PHASE 1 — VISUAL IDENTITY FOUNDATION & THEME ARCHITECTURE

Establish the permanent design foundation of OHGO.

This phase covers:

- Current interface and design audit.
- Visual identity definition.
- Semantic color architecture.
- Dark appearance.
- Light appearance.
- Auto/Dark/Light theme preference.
- System appearance detection.
- Theme persistence and migration.
- Theme-resolution architecture.
- Foundational typography, spacing, and visual tokens.

Phase 1 must produce a working, testable, documented foundation.

## PHASE 2 — PRODUCT-WIDE UI/UX REFINEMENT & EXPERIENCE CONSOLIDATION

Apply the approved foundation across the entire OHGO user experience.

This phase covers:

- Every user-facing interface.
- Timeline and execution presentation.
- Commands and tool results.
- Thought presentation.
- Errors, warnings, and execution states.
- Input and interaction surfaces.
- Footer and status bar.
- Long-content handling.
- Spacing, typography, contrast, and visual hierarchy.
- Full Dark/Light consistency.
- Terminal compatibility.
- End-to-end visual and functional verification.

Phase 2 must not create an independent design system.

It must consistently consume the foundation established in Phase 1.

**Do not skip the acceptance gate between phases. Do not redesign Phase 1 while implementing Phase 2 unless an objectively demonstrated defect requires correction.**

Execute both phases sequentially in this implementation task. Use the gates as engineering controls, not as reasons to stop unnecessarily for user confirmation.

---

# PHASE 0 — DISCOVERY AND BASELINE

Before implementing either phase, perform a thorough investigation of the actual repository.

Do not assume that the architecture, rendering system, theme configuration, or existing color values match any proposed implementation.

## 0.1 Understand the Existing UI Architecture

Identify:

- Application entry points.
- Main rendering architecture.
- TUI framework and terminal abstraction.
- Component hierarchy.
- Existing layout systems.
- Styling and color implementation.
- Theme configuration and `/theme` command.
- Input and command presentation.
- Timeline and tool execution rendering.
- Status bar and footer implementation.
- Configuration persistence.
- Existing platform-specific functionality.
- Existing tests and visual verification infrastructure.

Trace how theme information flows from configuration into actual rendered components.

Identify duplicated color definitions and inconsistent styling.

## 0.2 Capture the Baseline

Inspect the current Git status before editing anything.

Preserve all pre-existing user changes.

Record:

- Existing behavior of `/theme`.
- Current available theme options.
- Existing mode signature colors.
- Current color values and their sources of truth.
- Existing configuration compatibility.
- Supported operating systems.
- Supported terminal capabilities.
- Existing rendering limitations.

Run the appropriate baseline checks.

Capture or inspect representative terminal sessions wherever the tooling permits.

Evaluate the existing interface in real terminal conditions, not exclusively through source-code inspection.

## 0.3 Produce an Implementation Map

Determine which components must be changed and how they depend on the theme system.

Identify:

- Shared components.
- Theme-dependent components.
- Components with hardcoded colors.
- Components with duplicated semantic logic.
- Components that may bypass the primary styling system.

Create a concise implementation map before proceeding.

Do not introduce unnecessary abstractions. Prefer the smallest architecture that can support the complete product consistently.

---

# PHASE 1 — VISUAL IDENTITY FOUNDATION & THEME ARCHITECTURE

# 4. ESTABLISH THE OHGO VISUAL LANGUAGE

OHGO needs a recognizable identity beyond its three mode colors.

The overall visual direction must be:

**Refined Dark Forest / Soft Sage / Quiet Terminal**

This is a design direction, not permission to blindly apply arbitrary color values.

Study the actual interface, existing brand colors, and terminal rendering constraints before selecting final values.

The result must feel:

- Minimal but not visually empty.
- Dense but not cluttered.
- Technical but approachable.
- Professional without looking corporate.
- Distinctive without becoming visually loud.
- Calm without sacrificing operational visibility.

## 4.1 Neutral-First Visual Language

Most of the interface should use a carefully calibrated neutral palette.

The three mode colors should be meaningful identity accents, not colors applied indiscriminately to the entire interface.

Use tonal hierarchy before introducing additional colors.

Prioritize:

1. Foreground contrast.
2. Surface separation.
3. Typography weight.
4. Spacing.
5. Semantic indicators.
6. Decorative treatment only when genuinely necessary.

## 4.2 Define a Semantic Design Token Architecture

Establish one authoritative source of truth for visual tokens.

The exact implementation must follow the existing language, rendering framework, and project conventions.

At minimum, organize the semantic system around:

**Surface**
- Background.
- Primary surface.
- Secondary surface.
- Elevated surface.
- Input surface.
- Selection surface.

**Text**
- Primary.
- Secondary.
- Muted.
- Disabled.
- Inverse.

**Borders**
- Subtle.
- Default.
- Strong.
- Focus.

**Mode Identity**
- ASK.
- FUZZ.
- BUILD.
- Their accessible foreground and background variants.

**Operational Status**
- Success.
- Warning.
- Error.
- Running.
- Idle.
- Informational.

**Interaction**
- Focus.
- Selection.
- Cursor.
- Hover, where supported.
- Active.
- Disabled.

Do not force meaningless tokens into the implementation.

Each token must have a clear semantic purpose.

Avoid creating multiple competing color sources of truth.

All components must consume semantic tokens rather than defining arbitrary local colors.

The architecture must make future appearance extensions possible without requiring a redesign of the component system.

Do not implement additional themes in this project.

---

# 5. DESIGN DARK AND LIGHT AS TWO COMPLETE APPEARANCES

Dark and Light are equally important product appearances.

Neither is a secondary or automatically inverted version of the other.

They must share the same semantic architecture while having independently calibrated values.

## 5.1 Dark Appearance — Deep Forest

Design the Dark appearance around:

- Deep charcoal with a subtle forest undertone.
- Distinct but restrained dark surfaces.
- Soft warm-white primary text.
- Muted sage-gray secondary text.
- Carefully calibrated metadata.
- Low-contrast but visible separators.
- Restrained semantic status colors.
- Subtle interaction states.

Avoid:

- Pure black everywhere.
- Excessive green surfaces.
- Overly bright foreground text.
- Excessive contrast between every adjacent element.
- Borders surrounding every component.
- Glowing effects.
- Excessive color saturation.

Dark should feel calm, focused, deep, and technically mature.

## 5.2 Light Appearance — Soft Sage

Design Light independently around:

- Warm off-white or carefully calibrated soft-sage backgrounds.
- Near-white content surfaces.
- Deep charcoal-green primary text.
- Muted but readable secondary text.
- Subtle neutral-gray borders.
- Restrained mode identity accents.
- Carefully calibrated semantic states.

Avoid:

- Pure white everywhere.
- Washed-out secondary text.
- Excessive green backgrounds.
- Heavy borders.
- High-contrast black separators.
- An appearance that feels like an ordinary office application.
- Simply reversing the Dark color values.

Light must retain OHGO's distinctive identity.

It should feel clean, sophisticated, quiet, and comfortable for extended use.

## 5.3 Independent Appearance Calibration

For both appearances:

- Check text readability.
- Check metadata readability.
- Check low-emphasis text.
- Check selection and focus visibility.
- Check mode color distinguishability.
- Check success, warning, and error visibility.
- Check inactive and disabled states.
- Check terminal background and foreground behavior.

Where applicable, use WCAG contrast targets as a measurable reference:

- At least 4.5:1 for normal text.
- At least 3:1 for large text and meaningful non-text UI indicators.

For terminal-rendered content, account for the actual foreground/background pair and ANSI color mapping.

Do not claim accessibility compliance without verification.

Mode signature colors may have complementary tonal variants for accessible presentation. Preserve their canonical identity rather than weakening or replacing the signature unnecessarily.

---

# 6. IMPLEMENT AUTO / DARK / LIGHT THEME SUPPORT

The existing `/theme` functionality must evolve into a reliable three-option appearance system.

The available choices must be exactly:

1. Auto.
2. Dark.
3. Light.

**Auto must be the default for new or previously unset theme preferences.**

## 6.1 Theme Preference Model

Separate the user's preference from the currently resolved appearance.

Conceptually:

- `auto` — follow system appearance.
- `dark` — force Dark.
- `light` — force Light.

The effective appearance must be resolved independently from the stored preference.

For example:

- Preference: `auto`.
- System appearance: `dark`.
- Effective appearance: `dark`.

If the system later switches to Light:

- Preference remains `auto`.
- Effective appearance becomes `light`.

Never replace the user's `auto` preference with the currently resolved value.

## 6.2 System Appearance Detection

Investigate the actual target platforms and runtime environment before selecting a detection strategy.

Do not assume that all terminals expose system appearance through the same mechanism.

Implement an appropriate system appearance provider or adapter using supported platform mechanisms.

Requirements:

- Detect Dark or Light when reliable system information is available.
- Follow system changes dynamically where the environment supports notifications.
- Re-evaluate appearance on startup or resume where appropriate.
- Avoid excessive polling.
- Avoid shelling out on every render.
- Avoid introducing expensive startup work.
- Handle missing or inaccessible system information.
- Preserve existing application responsiveness.

If system appearance cannot be determined reliably:

- Fall back deterministically to Dark.
- Preserve the user's `auto` preference.
- Do not silently claim that the system appearance was detected.
- Clearly distinguish a fallback from an actual system result where appropriate.

Account for terminal-specific environments, including:

- macOS.
- Linux.
- Windows, if supported.
- SSH sessions.
- tmux sessions.
- Terminals without reliable system appearance integration.

Do not assume that the operating system running a remote application is necessarily the same as the user's local desktop environment.

Document the actual resolution behavior and limitations.

Do not claim universal real-time system integration unless it has been verified on the supported platforms.

## 6.3 `/theme` User Experience

The command must communicate the available choices clearly and consistently.

The user must be able to identify:

- Current preference.
- Current effective appearance.
- Whether the effective appearance came from system detection or fallback.

Example:

`Theme: Auto → Dark (System)`

Or an equivalent presentation that follows the actual existing CLI conventions.

The presentation must remain compact and natural.

Requirements:

- Easy selection.
- Clear current selection.
- Immediate application.
- No unnecessary restart.
- No session reset.
- No loss of timeline or agent state.
- No interruption of an active operation.
- No unnecessary terminal flicker.
- Consistent behavior after application restart.

## 6.4 Configuration Migration and Persistence

Inspect existing configuration formats before changing them.

Requirements:

- New or unset preferences default to Auto.
- Existing explicit Dark preferences remain Dark.
- Existing explicit Light preferences remain Light.
- Existing user configuration must not be silently overwritten.
- Invalid values must have a deterministic fallback.
- Configuration writes must follow existing atomicity and error-handling conventions.
- Theme changes must persist correctly.

Do not migrate preferences by destroying or replacing unrelated settings.

## 6.5 Theme Resolution Architecture

Keep the following concepts separate:

1. Stored theme preference.
2. System appearance.
3. Resolved effective appearance.
4. Rendered semantic tokens.

Components must consume resolved tokens rather than independently inspecting system configuration.

Avoid theme-specific business logic inside individual components.

Theme resolution must be centralized and deterministic.

---

# PHASE 1 ACCEPTANCE GATE

Phase 1 is complete only when all of the following are verified.

**Identity**
- ASK remains Teal.
- FUZZ remains Magenta.
- BUILD remains Blue.
- Mode and operational status colors are independent.

**Design Foundation**
- Dark semantic palette exists.
- Light semantic palette exists.
- Both appearances have clear and consistent token mappings.
- There is one authoritative token source.

**Theme Functionality**
- `/theme` supports Auto, Dark, and Light.
- Auto is the default for new/unset preferences.
- Explicit existing preferences are preserved.
- System detection works where supported.
- Unavailable detection has a deterministic fallback.
- Preference and effective appearance are independent.
- Theme changes do not reset runtime state.

**Engineering**
- Configuration migration is tested.
- Theme resolution is tested.
- Color mappings are tested.
- Existing checks remain healthy.
- No unrelated runtime behavior has been modified.

**Deliverables**
- Implemented theme foundation.
- Canonical semantic token system.
- Dark palette.
- Light palette.
- Theme preference and resolution implementation.
- `/theme` functionality.
- Relevant tests.
- Concise design documentation.
- Phase 1 verification report.

Do not proceed to Phase 2 with unresolved fundamental defects.

---

# PHASE 2 — PRODUCT-WIDE UI/UX REFINEMENT

The purpose of this phase is to transform the foundational appearance system into one cohesive OHGO experience.

Do not simply replace color constants.

Review how every component communicates information and how the entire interface feels during real operation.

The existing terminal-native layout is a valuable foundation.

**Preserve the current overall layout unless an actual usability problem justifies a structural change.**

Do not redesign for the sake of redesigning.

---

# 7. GLOBAL VISUAL HIERARCHY

Establish a consistent hierarchy throughout OHGO.

The general priority should be:

1. Primary user-facing information.
2. Current agent activity.
3. Commands and execution results.
4. Operational states.
5. Supporting metadata.
6. Decorative or low-priority information.

The hierarchy must work in both Dark and Light.

Prefer typography, spacing, alignment, and tonal contrast before introducing more colors or visual containers.

Use a consistent spacing system.

Avoid independently chosen margins and padding that produce subtle visual inconsistencies.

Respect the existing terminal's available dimensions.

---

# 8. REFINE THE EXISTING INTERFACE

Use the actual application architecture and the supplied reference screenshots as starting points.

Preserve the established terminal-native identity.

## 8.1 Header

Refine the header to communicate:

- OHGO identity.
- Version.
- Active profile or execution environment.
- Relevant runtime state.

Requirements:

- Clear alignment.
- Consistent typography.
- Appropriate contrast.
- Restrained metadata.
- No unnecessary decoration.

The header should be recognizable without dominating the screen.

## 8.2 Timeline and Thought Presentation

Preserve the chronological execution experience.

Make the distinction between:

- Thought.
- Tool invocation.
- Tool execution.
- Output.
- Errors.
- Recovery or subsequent execution.

visually clear.

Thought should use a neutral, low-emphasis presentation.

Do not use warning amber merely because the label is called Thought.

Thought, Warning, and Error are different semantic concepts.

Avoid excessive separators and unnecessary card-like containers.

## 8.3 Command Presentation

Command readability is a high priority.

Refine command styling:

- Keep execution indicators semantic.
- Use neutral foreground colors for command content.
- Avoid coloring every successful command entirely green.
- Avoid turning an entire failed command into a large red visual block.
- Preserve readable command arguments.
- Make long commands manageable.
- Maintain proper wrapping or the best supported long-line presentation.
- Preserve the complete command content.

The user should be able to scan multiple commands quickly without excessive visual noise.

## 8.4 Tool Execution Lifecycle

Provide clear, truthful visual feedback for actual execution states.

Where supported by real runtime events, distinguish:

- Running.
- Completed.
- Failed.
- Timed out.
- Cancelled.
- Retrying or recovering.
- Awaiting user input or authorization.

Do not introduce UI states that the backend does not actually expose.

Do not infer success from the absence of an error.

Do not invent retry or recovery behavior.

The interface should make important state transitions understandable without turning every event into a prominent notification.

## 8.5 Output Presentation

Improve the readability of tool outputs.

Requirements:

- Preserve useful source content.
- Keep summaries and metadata visually subordinate.
- Maintain consistent indentation.
- Preserve expand/collapse functionality.
- Clearly communicate truncation.
- Avoid presenting incomplete content as complete.
- Maintain readable long-output navigation.

Output should remain easy to scan during long investigation sessions.

## 8.6 Errors and Warnings

Make error information immediately recognizable without making the entire interface aggressive.

Requirements:

- Use restrained semantic error colors.
- Keep actual error messages readable.
- Distinguish warnings from failures.
- Preserve command and diagnostic context.
- Avoid color-only communication.
- Avoid hiding important error details behind ambiguous indicators.

A professional runtime communicates failure clearly rather than trying to make failure look attractive.

## 8.7 Input and Mode Indicators

Preserve the canonical mode identity.

ASK remains Teal.

FUZZ remains Magenta.

BUILD remains Blue.

Ensure the active mode is recognizable without overpowering the prompt or surrounding content.

Refine:

- Prompt spacing.
- Cursor visibility.
- Focus indication.
- Input foreground/background.
- Placeholder or hint styling.
- Active mode presentation.

Do not replace the established mode colors with one universal accent.

## 8.8 Footer and Status Bar

The footer contains operationally valuable information.

Refine its hierarchy so that:

- Important keyboard shortcuts are readable.
- Session information is distinguishable.
- Runtime status is understandable.
- Secondary metadata remains subtle.
- Active context is recognizable.
- Unavailable information is not represented as factual.

Avoid both excessively dim text and a footer that competes with the primary timeline.

The footer must remain balanced in both appearances.

## 8.9 Long Sessions and High Information Density

OHGO is an agent runtime.

Long-running sessions can contain many tool calls and extensive output.

Verify the design under realistic conditions:

- Repeated Thought entries.
- Numerous commands.
- Long command lines.
- Large tool outputs.
- Nested output.
- Repeated failures.
- Multiple execution transitions.
- Long sessions with scrolling.

The interface must maintain readability without continuously increasing visual complexity.

Do not let repeated status indicators become visual noise.

---

# 9. TYPOGRAPHY, SPACING, AND INTERACTION REFINEMENT

Establish a consistent typographic system.

Use the existing font and terminal conventions unless actual problems justify changes.

Do not introduce a new font dependency simply for visual novelty.

Typography must distinguish:

- Primary content.
- Tool and command text.
- Supporting metadata.
- Timing information.
- Keyboard shortcuts.
- State labels.

Use weight, contrast, and spacing carefully.

Avoid excessive boldness and unnecessary uppercase labels.

Review line-height and vertical spacing so that the timeline remains compact but comfortable to read.

Interactions must have clear states:

- Default.
- Focused.
- Selected.
- Active.
- Disabled.
- Error, where appropriate.

Do not rely exclusively on color to distinguish interaction states.

Preserve keyboard-first usability and existing interaction conventions.

---

# 10. TERMINAL-NATIVE QUALITY

OHGO must remain an efficient terminal-native product.

Visual quality must never compromise terminal ergonomics.

Verify:

- Different terminal widths.
- Narrow terminal windows.
- Different terminal heights.
- Resizing while an operation is active.
- True-color terminals.
- 256-color terminals.
- Limited-color environments where supported.
- tmux compatibility.
- SSH environments.
- Unicode and glyph fallback behavior.
- Terminal restoration after exit or interruption.

Do not assume that all terminal environments support the same color capabilities.

Use the existing terminal abstraction and capability detection.

Provide appropriate fallbacks without fragmenting the product identity.

The interface must remain usable under constrained terminal capabilities.

Do not introduce heavy rendering dependencies or background processing without a demonstrated need.

Do not sacrifice responsiveness for visual effects.

---

# 11. CONSISTENCY ACROSS THE ENTIRE PRODUCT

Perform a product-wide styling audit.

Search for:

- Hardcoded colors.
- Duplicated mode identity values.
- Duplicated status colors.
- Inconsistent metadata styling.
- Inconsistent separators.
- Inconsistent input presentation.
- Theme-specific component logic.
- Components bypassing semantic tokens.
- Legacy styling paths.

Migrate relevant components to the canonical design system.

Avoid a partial implementation in which the main timeline looks polished but smaller screens or secondary components remain visually inconsistent.

Every user-facing surface must use the same semantic language.

However, consistency does not mean every component should look identical.

Different components should retain their functional purpose while sharing the same underlying visual principles.

Do not introduce unnecessary component abstractions solely to enforce superficial uniformity.

---

# 12. PERFORMANCE AND ENGINEERING QUALITY

The visual refinement must not degrade application performance.

Requirements:

- Avoid unnecessary allocations during rendering.
- Avoid repeated theme resolution.
- Avoid filesystem reads during normal render operations.
- Avoid repeated shell commands.
- Avoid excessive polling.
- Avoid introducing expensive dependencies.
- Avoid unnecessary state duplication.
- Avoid terminal redraw storms.
- Avoid full application restarts for theme changes.

Resolve theme tokens efficiently and reuse them.

Changing the appearance should be a presentation-level operation.

Do not recreate or reset the agent runtime just to update the UI.

Preserve all existing operational behavior.

Keep implementation complexity proportional to the actual requirements.

Prefer native language and framework capabilities over unnecessary dependencies.

---

# 13. VERIFICATION AND VISUAL QUALITY ASSURANCE

Visual quality must be verified through actual rendered interfaces.

Source-code review alone is insufficient.

## 13.1 Dark and Light Verification

Capture and inspect representative sessions in both appearances.

At minimum verify:

- Initial interface.
- Normal command execution.
- Successful tool output.
- Thought presentation.
- Warning.
- Error.
- Timeout.
- Long output.
- Expanded and collapsed output.
- Input focus.
- Active ASK mode.
- Active FUZZ mode.
- Active BUILD mode.
- Footer and status bar.
- Theme selection interface.

Check both color consistency and readability.

## 13.2 Auto Verification

Where platform support permits, verify:

- System Dark resolves to Dark.
- System Light resolves to Light.
- System changes update the effective appearance.
- Explicit Dark overrides the system.
- Explicit Light overrides the system.
- Auto preference survives restarts.
- Missing system appearance uses the documented fallback.
- SSH or remote execution does not claim unsupported local system detection.

Test the actual behavior, not merely a mocked expected result.

## 13.3 Terminal Geometry Verification

Test representative terminal dimensions, including:

- 80 × 24.
- 120 × 40.
- A larger terminal window.
- A narrow resized terminal window.

Use the project's available PTY or interactive terminal testing infrastructure.

Check for:

- Unexpected wrapping.
- Horizontal overflow.
- Broken alignment.
- Clipped content.
- Unreadable footer elements.
- Incorrect cursor placement.
- Layout instability after resizing.

## 13.4 Regression Verification

Run the relevant project checks:

- Formatting.
- Static analysis.
- Linting.
- Unit tests.
- Integration tests.
- Configuration migration tests.
- Theme resolution tests.
- Existing application tests.
- Build checks.

Do not weaken or remove existing tests to make the implementation pass.

Add tests where new behavior or architecture requires them.

Run actual interactive checks for functionality that automated tests cannot validate.

---

# 14. IMPLEMENTATION RULES

These rules apply throughout both phases.

## 14.1 No Blind Redesign

Understand the existing implementation before editing.

Preserve useful design decisions.

Change only what requires improvement or is necessary to establish consistency.

## 14.2 No Arbitrary Design Decisions

Every significant visual decision should have a reason.

Do not add colors because they look attractive in isolation.

Do not add UI elements because they appear in other agent products.

Do not imitate another product's identity.

OHGO must have its own coherent visual language.

## 14.3 No Cosmetic-Only Implementation

A theme selector that changes a few visible colors is not a complete theme implementation.

A consistent design system must be used across the actual application.

A visually polished screenshot is not sufficient proof of completion.

## 14.4 No False Operational Information

All UI states, metrics, tool information, progress, and execution feedback must remain grounded in actual application state.

Never invent information to make the interface appear complete.

## 14.5 Preserve Existing Work

Before changing files:

- Inspect Git status.
- Identify existing modifications.
- Preserve unrelated changes.
- Avoid destructive operations.
- Do not overwrite user work.
- Do not reset or clean the repository.

Do not create commits, tags, releases, or push changes unless explicitly requested.

## 14.6 Documentation

Update or create concise design documentation using the repository's existing documentation conventions.

Document:

- Visual identity principles.
- Signature mode colors.
- Semantic token definitions.
- Dark palette.
- Light palette.
- Theme preference model.
- System appearance resolution.
- Fallback behavior.
- Component usage conventions.
- Terminal compatibility constraints.

Documentation must describe the actual implemented behavior, not intended or hypothetical functionality.

---

# 15. FINAL ACCEPTANCE CRITERIA

The project is complete only when all the following conditions are satisfied.

## Brand Identity

- OHGO has one cohesive and recognizable visual identity.
- ASK, FUZZ, and BUILD retain their signature colors.
- Mode identity and operational status are independent.
- Both appearances feel unmistakably like OHGO.
- The design does not depend on unnecessary decorative effects.

## Appearance System

- Auto, Dark, and Light all work.
- Auto is the default for new or unset preferences.
- Existing explicit preferences are preserved.
- Dark and Light are independently designed.
- System appearance handling is accurate within documented platform capabilities.
- Theme changes do not interrupt the runtime.

## Visual Experience

- Typography is consistent.
- Spacing is consistent.
- Command content is readable.
- Timeline hierarchy is clear.
- Output is understandable.
- Errors and warnings are distinguishable.
- Interaction states are visible.
- Footer information is balanced.
- Long sessions remain comfortable to use.

## Engineering

- Design tokens have one source of truth.
- Components consistently consume semantic tokens.
- No unnecessary architecture was introduced.
- Performance remains healthy.
- Configuration migration is safe.
- Existing runtime functionality remains intact.
- Tests and regression checks pass.
- Terminal compatibility is verified to the extent supported.

## Product Recognition

Ask the following final question:

**If someone sees an OHGO screenshot without its name, logo, or branding text, is there enough consistency in its visual language to recognize that it belongs to the same product?**

The goal is not to manufacture recognition through excessive branding.

The goal is to earn recognition through a consistent design language.

That is the standard.

---

# 16. REQUIRED FINAL REPORT

After completing both phases, produce a factual implementation report.

Use this structure:

## 1. Executive Summary
- What was implemented.
- Whether both phases were completed.
- Overall design outcome.

## 2. Initial Audit
- Existing architecture.
- Problems discovered.
- Important constraints.
- Existing behavior preserved.

## 3. Phase 1 — Identity and Theme Foundation
- Design token architecture.
- Final Dark palette.
- Final Light palette.
- Mode identity preservation.
- Auto/Dark/Light implementation.
- System detection and fallback.
- Configuration migration.
- Tests and verification.

## 4. Phase 2 — Product-wide UI/UX
- Components refined.
- Timeline and command presentation.
- Operational states.
- Input and footer.
- Typography and spacing.
- Terminal compatibility.
- Visual verification.

## 5. Files Changed
List the important files and explain the purpose of their changes.

## 6. Verification Results
Provide actual commands and results.

Clearly distinguish:
- Passed.
- Failed.
- Not tested.
- Not supported.
- Partially verified.

Never claim a test passed without executing it.

## 7. Remaining Limitations
Report any unresolved issues honestly.

## 8. Final Assessment
Explain whether OHGO now has a coherent and recognizable visual identity, how the two appearances differ, and whether the acceptance criteria have been met.

Do not claim complete success if important acceptance criteria remain unmet.

---

# FINAL DIRECTIVE

Build OHGO as one product with one identity, not as multiple interfaces sharing the same name.

The three signature mode colors remain recognizable.

The Dark and Light appearances must each feel intentionally designed.

Auto must behave naturally.

The semantic design system must remain consistent.

Every component must contribute to the same visual language.

The interface must be refined without becoming decorative, information-dense without becoming cluttered, and powerful without sacrificing clarity.

**Do not optimize for how impressive OHGO looks in a screenshot alone. Optimize for how coherent, readable, predictable, and comfortable it feels throughout real working sessions.**

The ultimate objective is simple:

**When developers see OHGO, they should recognize OHGO — not because it looks like another AI product, but because it has developed a distinctive identity of its own.**

Begin with Phase 0 discovery and baseline. Then execute Phase 1 and Phase 2 sequentially, respecting their acceptance gates.

Prioritize correctness, consistency, and verifiable quality over implementation speed.
