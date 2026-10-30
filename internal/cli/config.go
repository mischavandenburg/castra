package cli

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode"

	"github.com/spf13/cobra"

	"github.com/mischavandenburg/castra/internal/config"
)

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Show castra's config file and the settings in it",
		Long:  "Every setting is optional; castra works without a config file.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(newConfigPathCmd(), newConfigShowCmd())
	return cmd
}

func newConfigPathCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "path",
		Short: "Print the path of the config file",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := config.Path()
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), p)
			return err
		},
	}
}

func newConfigShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Print the settings in the config file",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			printConfig(cmd.OutOrStdout(), cfg)
			return nil
		},
	}
}

// printConfig writes one line per setting, in Config's order. For an unset
// setting it says what castra does instead.
func printConfig(w io.Writer, cfg config.Config) {
	printSetting(w, "engine", cfg.Engine, "not set (podman if installed, otherwise docker)")
	printSetting(w, "dotfiles.repo", cfg.Dotfiles.Repo, "not set (castra installs no dotfiles)")
	printSetting(w, "dotfiles.install", cfg.Dotfiles.Install, "not set (castra runs the first install script it finds in the repository)")
}

func printSetting(w io.Writer, key, value, unset string) {
	if value == "" {
		fmt.Fprintf(w, "%s: %s\n", key, unset)
		return
	}
	if strings.IndexFunc(value, unicode.IsControl) >= 0 {
		value = strconv.Quote(value)
	}
	fmt.Fprintf(w, "%s: %s\n", key, value)
}
