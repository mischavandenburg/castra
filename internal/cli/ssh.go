package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"al.essio.dev/pkg/shellescape"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/mischavandenburg/castra/internal/engine"
	"github.com/mischavandenburg/castra/internal/server"
	"github.com/mischavandenburg/castra/internal/sshconfig"
	"github.com/mischavandenburg/castra/internal/workspace"
)

func newSSHCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ssh <workspace> [-- command...]",
		Short: "Open a shell in a workspace, or run a command there, with your agent forwarded",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			command := args[1:]
			if dash := cmd.ArgsLenAtDash(); dash > 1 {
				return errors.New("usage: castra ssh <workspace> [-- command...]")
			}
			st, err := workspace.Load(name)
			if err != nil {
				return err
			}
			eng, err := engine.Detect(st.Engine)
			if err != nil {
				return err
			}
			running, err := eng.Running(cmd.Context(), st.ContainerID)
			if err != nil || !running {
				// Stopped or gone: bring the camp back up the normal way, which
				// also runs postStartCommand.
				if st, err = up(cmd.Context(), st.Folder, upFlags{name: st.Name}, cmd.ErrOrStderr()); err != nil {
					return err
				}
			}
			paths, err := sshconfig.DefaultPaths()
			if err != nil {
				return err
			}
			sshPath, err := exec.LookPath("ssh")
			if err != nil {
				return fmt.Errorf("ssh not found on PATH: %w", err)
			}
			argv := []string{"ssh", "-F", paths.Config}
			if len(command) > 0 && term.IsTerminal(int(os.Stdin.Fd())) {
				argv = append(argv, "-t")
			}
			argv = append(argv, st.Alias())
			if len(command) > 0 {
				argv = append(argv, shellescape.QuoteCommand(command))
			}
			return syscall.Exec(sshPath, argv, os.Environ())
		},
	}
}

// newProxyCmd is what OpenSSH runs as ProxyCommand: it replaces itself with
// `podman exec -i` into the container's castra server, so SSH rides the
// exec's stdin and stdout and nothing listens on a port.
func newProxyCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "proxy <workspace>",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := workspace.Load(args[0])
			if err != nil {
				return err
			}
			eng, err := engine.Detect(st.Engine)
			if err != nil {
				return err
			}
			running, err := eng.Running(cmd.Context(), st.ContainerID)
			if err != nil {
				return err
			}
			if !running {
				return fmt.Errorf("workspace %s is not running: run `castra up %s`", st.Name, st.Name)
			}
			paths, err := sshconfig.DefaultPaths()
			if err != nil {
				return err
			}
			pub, err := os.ReadFile(paths.ClientKey + ".pub")
			if err != nil {
				return fmt.Errorf("read castra client key: %w (run `castra up` again)", err)
			}
			argv := eng.ExecArgs(st.RemoteUser, st.ContainerID,
				serverPath(st), "server", "--stdio",
				"--host-key", hostKeyPath(st),
				"--authorized-key", strings.TrimSpace(string(pub)),
				"--workdir", st.Workdir,
			)
			return syscall.Exec(argv[0], argv, os.Environ())
		},
	}
}

// newServerCmd runs inside the container. Its stdout is the SSH stream, so
// nothing else may ever be written there.
func newServerCmd() *cobra.Command {
	var stdio bool
	var hostKey, authorizedKey, workdir string
	cmd := &cobra.Command{
		Use:    "server",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !stdio {
				return errors.New("castra server only runs with --stdio")
			}
			key, err := os.ReadFile(hostKey)
			if err != nil {
				return err
			}
			return server.ServeStdio(server.Options{
				HostKey:       key,
				AuthorizedKey: []byte(authorizedKey),
				Workdir:       workdir,
			})
		},
	}
	cmd.Flags().BoolVar(&stdio, "stdio", false, "speak SSH on stdin and stdout")
	cmd.Flags().StringVar(&hostKey, "host-key", "", "path of the private host key")
	cmd.Flags().StringVar(&authorizedKey, "authorized-key", "", "the one client public key allowed in (authorized_keys format)")
	cmd.Flags().StringVar(&workdir, "workdir", "", "directory sessions start in")
	return cmd
}
