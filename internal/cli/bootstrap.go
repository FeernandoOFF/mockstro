package cli

import (
	"fmt"
	"path/filepath"

	"github.com/feernandooff/mockstro/internal/bootstrap"
	"github.com/spf13/cobra"
)

func bootstrapCommand(g *globals) *cobra.Command {
	var version string
	cmd := &cobra.Command{
		Use: "bootstrap", Short: "Add a pinned wrapper to an existing .maestro project (preserves config)", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := installRoot(g)
			if err != nil {
				return err
			}
			if err := bootstrap.Project(root, version); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Added mockstrow and .maestro/.mockstro-version. Commit both; run ./mockstrow --help to download the pinned CLI.")
			return nil
		},
	}
	cmd.Flags().StringVar(&version, "cli-version", "", "release tag to pin, e.g. v1.2.3 (required)")
	_ = cmd.MarkFlagRequired("cli-version")
	return cmd
}

func skillCommand(g *globals) *cobra.Command {
	var claude bool
	cmd := &cobra.Command{Use: "skill", Short: "Manage the agent skill bundled with this CLI", Args: cobra.NoArgs}
	install := &cobra.Command{
		Use: "install", Short: "Install into .agents/skills/mockstro without overwriting existing files", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := installRoot(g)
			if err != nil {
				return err
			}
			if err := bootstrap.Skill(root, claude); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Installed .agents/skills/mockstro from this CLI. Commit it with the wrapper and version pin.")
			return nil
		},
	}
	install.Flags().BoolVar(&claude, "claude", false, "also link .claude/skills/mockstro to the canonical skill")
	cmd.AddCommand(install)
	return cmd
}

func installRoot(g *globals) (string, error) {
	dir := g.projectDir
	if dir == "" {
		dir = "."
	}
	return filepath.Abs(dir)
}
