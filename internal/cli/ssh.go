package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/mischavandenburg/castra/internal/engine"
	"github.com/mischavandenburg/castra/internal/server"
	"github.com/mischavandenburg/castra/internal/shell"
	"github.com/mischavandenburg/castra/internal/sshconfig"
	"github.com/mischavandenburg/castra/internal/workspace"
)

func newSSHCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ssh [workspace] [-- command...]",
		Short: "Open a shell in a workspace, or run a command there, with your agent forwarded",
		Long: `Open a shell in a workspace, or run a command there, with your agent forwarded.
Without a workspace name, castra shows a menu (or uses the only workspace there is).`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			var name string
			var command []string
			switch dash := cmd.ArgsLenAtDash(); {
			case dash > 1:
				return errors.New("usage: castra ssh [workspace] [-- command...]")
			case dash == 0:
				command = args
			case len(args) > 0:
				name, command = args[0], args[1:]
			}
			var st workspace.State
			var err error
			if name == "" {
				st, err = chooseWorkspace(cmd, "Which workspace? (arrows or j/k, Enter, q to quit)", true)
			} else {
				st, err = workspace.Load(name)
			}
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
			sshPath, err := sshPath()
			if err != nil {
				return err
			}
			argv := []string{"ssh", "-F", paths.Config}
			if len(command) > 0 && term.IsTerminal(int(os.Stdin.Fd())) {
				argv = append(argv, "-t")
			}
			argv = append(argv, st.Alias())
			if len(command) > 0 {
				argv = append(argv, shell.Join(command))
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
			if err := refreshServer(cmd.Context(), eng, st, cmd.ErrOrStderr()); err != nil {
				return err
			}
			argv := eng.ExecArgs(st.RemoteUser, st.ContainerID,
				serverPath(st), "server", "--stdio",
				"--workdir", st.Workdir,
				"--workspace", st.Name,
				"--remote-env-file", remoteEnvPath(st),
			)
			return syscall.Exec(argv[0], argv, os.Environ())
		},
	}
}

// refreshServer installs this build of castra in the container when the one
// there is from another build, so a `castra` upgrade on the host never meets
// an older server that does not know its flags. Most connections cost one
// hash of the local binary; only the first after an upgrade copies it in.
func refreshServer(ctx context.Context, eng engine.Engine, st workspace.State, log io.Writer) error {
	self, err := readSelf()
	if err != nil {
		return err
	}
	if self.sum == st.ServerSum {
		return nil
	}
	held, err := workspace.Lock(st.Name)
	if err != nil {
		return err
	}
	defer func() { _ = held.Release() }()
	if err := installServer(ctx, eng, st, self, log); err != nil {
		return err
	}
	st.ServerSum = self.sum
	return workspace.Save(st)
}

// newServerCmd runs inside the container. Its stdout is the SSH stream, so
// nothing else may ever be written there.
func newServerCmd() *cobra.Command {
	var stdio bool
	var workdir, workspaceName, remoteEnvFile string
	cmd := &cobra.Command{
		Use:    "server",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !stdio {
				return errors.New("castra server only runs with --stdio")
			}
			var remoteEnv map[string]*string
			if remoteEnvFile != "" {
				b, err := os.ReadFile(remoteEnvFile)
				switch {
				case errors.Is(err, os.ErrNotExist): // written by `castra up`; older containers lack it
				case err != nil:
					return err
				default:
					if err := json.Unmarshal(b, &remoteEnv); err != nil {
						return fmt.Errorf("%s: %w", remoteEnvFile, err)
					}
				}
			}
			return server.ServeStdio(server.Options{
				Workdir:   workdir,
				Workspace: workspaceName,
				RemoteEnv: remoteEnv,
			})
		},
	}
	cmd.Flags().BoolVar(&stdio, "stdio", false, "speak SSH on stdin and stdout")
	cmd.Flags().StringVar(&workdir, "workdir", "", "directory sessions start in")
	cmd.Flags().StringVar(&workspaceName, "workspace", "", "workspace name, exported as CASTRA_WORKSPACE")
	cmd.Flags().StringVar(&remoteEnvFile, "remote-env-file", "", "JSON file with devcontainer.json's resolved remoteEnv")
	return cmd
}
