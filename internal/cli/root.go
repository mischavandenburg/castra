// Package cli holds castra's commands.
package cli

import (
	"context"
	"errors"
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
		Use:     "castra",
		Short:   "Devcontainers on Podman or Docker, reached over SSH with your agent forwarded",
		Version: Version(),
		Long: `castra builds a devcontainer from a folder's .devcontainer and lets you in over SSH,
with your ssh-agent forwarded for the length of each session. Nothing listens on a port.

Roman legions built the same camp at every stop. castra builds the same container every time.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		// The commands that write castra's SSH files or connect through them
		// first bring files from an older castra build to this layout, so an
		// upgrade never meets a Host block the new server cannot satisfy.
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			switch cmd.Name() {
			case "up", "ssh", "proxy", "delete":
				return migrate()
			}
			return nil
		},
	}
	// --version must print what `castra version` prints; cobra's own template
	// would read "castra version dev".
	root.SetVersionTemplate("castra {{.Version}}\n")
	root.AddCommand(
		newUpCmd(),
		newSSHCmd(),
		newListCmd(),
		newStopCmd(),
		newDeleteCmd(),
		newDoctorCmd(),
		newConfigCmd(),
		newVersionCmd(),
		newProxyCmd(),
		newServerCmd(),
	)
	return root
}

// Execute runs castra and returns the process exit code. A cancelled context
// means Ctrl-C reached a command that catches it (`up` while the devcontainer
// CLI builds); everything else dies on the signal the ordinary way.
func Execute() int {
	err := newRootCmd().ExecuteContext(context.Background())
	switch {
	case err == nil:
		return 0
	case errors.Is(err, context.Canceled):
		fmt.Fprintln(os.Stderr, "castra: interrupted")
		return 130
	default:
		fmt.Fprintln(os.Stderr, "castra:", err)
		return 1
	}
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
