package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/PizenLabs/ohgo/internal/tui"
	"github.com/spf13/cobra"
	"github.com/spf13/cobra/doc"
)

// docsCmd generates reference documentation for the harness.
//
// Shell completion already ships via cobra's built-in `completion` command;
// this covers the other half of installable documentation: markdown pages
// and man pages, generated from the same command tree so they cannot drift
// from the flags they describe.
func docsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "docs",
		Short: "Generate man pages and markdown reference",
		Long: `Generate the man pages and markdown reference into a directory.

The output is derived from the live command tree, so a flag documented here
is a flag the binary accepts. Package managers and install scripts can ship
the result without maintaining it by hand.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, _ := cmd.Flags().GetString("dir")
			if dir == "" {
				return fmt.Errorf("docs: --dir is required")
			}
			// The generators write into the directory rather than creating
			// it: pointing at a fresh path must work, not fail on open.
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return fmt.Errorf("docs: create %s: %w", dir, err)
			}
			// Generate from a fresh tree: the running command's tree is
			// identical, but building one keeps docs independent of how this
			// invocation was parsed.
			root := rootCmd()
			root.DisableAutoGenTag = true
			if err := doc.GenManTree(root, nil, dir); err != nil {
				return fmt.Errorf("docs: man pages: %w", err)
			}
			if err := doc.GenMarkdownTree(root, dir); err != nil {
				return fmt.Errorf("docs: markdown: %w", err)
			}
			if err := writeRuntimeReference(dir); err != nil {
				return fmt.Errorf("docs: runtime commands: %w", err)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "wrote reference into %s\n", dir)
			return nil
		},
	}
	cmd.Flags().String("dir", "", "output directory for man pages and markdown")
	return cmd
}

// writeRuntimeReference documents the in-session slash commands.
//
// The cobra tree documents flags and subcommands; it cannot see /help or /mcp
// because those are runtime commands. They are generated from tui.Commands, the
// same registry that powers runtime help and completion, so the CLI reference
// and the in-session listing cannot drift.
func writeRuntimeReference(dir string) error {
	if err := os.WriteFile(filepath.Join(dir, "runtime-commands.md"),
		[]byte(runtimeCommandsMarkdown()), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "ohgo-runtime-commands.1"),
		[]byte(runtimeCommandsMan()), 0o644)
}

// runtimeCommandsMarkdown renders the slash-command registry as markdown.
func runtimeCommandsMarkdown() string {
	var b strings.Builder
	b.WriteString("# Runtime slash commands\n\n")
	b.WriteString("These commands run inside an ohgo session, not as CLI subcommands. ")
	b.WriteString("Type `/help` in a session to open the Command & Help Center.\n\n")
	for _, group := range []string{tui.CommandGroupWorkspace, tui.CommandGroupControl} {
		fmt.Fprintf(&b, "## %s\n\n", group)
		for _, spec := range tui.Commands {
			if spec.Hidden || groupOfCommand(spec) != group {
				continue
			}
			fmt.Fprintf(&b, "- `/%s` — %s\n", spec.Usage, spec.Description)
			for _, sub := range spec.Subcommands {
				fmt.Fprintf(&b, "  - `/%s %s` — %s\n", spec.Name, sub.Usage, sub.Description)
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

// runtimeCommandsMan renders the slash-command registry as a man page.
func runtimeCommandsMan() string {
	var b strings.Builder
	b.WriteString(".TH OHGO-RUNTIME-COMMANDS 1 \"\" \"ohgo\" \"Runtime slash commands\"\n")
	b.WriteString(".SH NAME\nohgo runtime commands \\- slash commands available inside a session\n")
	b.WriteString(".SH DESCRIPTION\n")
	b.WriteString("These commands run inside an ohgo session, not as CLI subcommands. ")
	b.WriteString("Type /help in a session to open the Command & Help Center.\n")
	for _, group := range []string{tui.CommandGroupWorkspace, tui.CommandGroupControl} {
		fmt.Fprintf(&b, ".SH %s\n", group)
		for _, spec := range tui.Commands {
			if spec.Hidden || groupOfCommand(spec) != group {
				continue
			}
			fmt.Fprintf(&b, ".TP\n.B /%s\n%s\n", spec.Usage, spec.Description)
			for _, sub := range spec.Subcommands {
				fmt.Fprintf(&b, ".TP\n.B /%s %s\n%s\n", spec.Name, sub.Usage, sub.Description)
			}
		}
	}
	return b.String()
}

// groupOfCommand mirrors tui's grouping so the generated reference keeps the
// same WORKSPACE/CONTROL order as the in-session help.
func groupOfCommand(spec tui.CommandSpec) string {
	if spec.Category == tui.CommandGroupWorkspace {
		return tui.CommandGroupWorkspace
	}
	return tui.CommandGroupControl
}
