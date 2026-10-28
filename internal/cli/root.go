// Package cli holds castra's commands.
package cli

import (
	"context"
	"fmt"
	"os"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// version is set at release time with
// -ldflags "-X github.com/mischavandenburg/castra/internal/cli.version=vX.Y.Z".
var version = ""

// Version is castra's version: the release tag, the module version for
// `go install`, or "dev".
func Version() string {
	if version != "" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return "dev"
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "castra",
		Short: "Devcontainers on Podman or Docker, reached over SSH with your agent forwarded",
		Long: `castra builds a devcontainer from a folder's .devcontainer and lets you in over SSH,
with your ssh-agent forwarded for the length of each session. Nothing listens on a port.

Roman legions built the same camp at every stop. castra builds the same container every time.`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(
		newUpCmd(),
		newSSHCmd(),
		newListCmd(),
		newDoctorCmd(),
		newVersionCmd(),
		newProxyCmd(),
		newServerCmd(),
	)
	return root
}

// Execute runs castra and returns the process exit code.
func Execute() int {
	if err := newRootCmd().ExecuteContext(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "castra:", err)
		return 1
	}
	return 0
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print castra's version",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintln(cmd.OutOrStdout(), "castra", Version())
		},
	}
}
