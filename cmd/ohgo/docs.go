package main

import (
	"fmt"
	"os"

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
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "wrote reference into %s\n", dir)
			return nil
		},
	}
	cmd.Flags().String("dir", "", "output directory for man pages and markdown")
	return cmd
}
