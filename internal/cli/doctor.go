package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mischavandenburg/castra/internal/config"
	"github.com/mischavandenburg/castra/internal/devcontainer"
	"github.com/mischavandenburg/castra/internal/engine"
	"github.com/mischavandenburg/castra/internal/sshconfig"
)

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check that castra has what it needs",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, out := cmd.Context(), cmd.OutOrStdout()
			ok := true
			fmt.Fprintln(out, "castra:", Version())

			cfg, err := config.Load()
			if err != nil {
				fmt.Fprintln(out, "config: error:", err)
				ok = false
			}
			if eng, err := engine.Detect(cfg.Engine); err != nil {
				fmt.Fprintln(out, "engine: missing (install podman, or docker)")
				ok = false
			} else {
				v, _ := eng.Version(ctx)
				fmt.Fprintf(out, "engine: %s (%s)\n", v, eng.Path)
				if eng.Name == "podman" && os.Getuid() != 0 && !lingering(ctx) {
					fmt.Fprintf(out, "linger: off, so rootless Podman stops your containers when your last login session ends\n"+
						"        (on a machine you reach over SSH, run: sudo loginctl enable-linger %s)\n", os.Getenv("USER"))
				}
			}
			if cli, err := devcontainer.FindCLI(); err != nil {
				fmt.Fprintln(out, "devcontainer: missing, install it with: castra install-deps")
				ok = false
			} else {
				v, err := devcontainer.Version(ctx, cli)
				if err != nil {
					v = "unknown version: " + err.Error()
				}
				fmt.Fprintf(out, "devcontainer: %s (%s)\n", v, cli)
			}
			if p, err := exec.LookPath("ssh"); err != nil {
				fmt.Fprintln(out, "ssh: missing (install the OpenSSH client)")
				ok = false
			} else {
				v, _ := exec.CommandContext(ctx, p, "-V").CombinedOutput()
				fmt.Fprintf(out, "ssh: %s (%s)\n", strings.TrimSpace(string(v)), p)
			}
			if os.Getenv("SSH_AUTH_SOCK") == "" {
				fmt.Fprintln(out, "ssh-agent: SSH_AUTH_SOCK is not set, so no keys will be forwarded")
			} else {
				fmt.Fprintln(out, "ssh-agent: SSH_AUTH_SOCK is set")
			}
			if paths, err := sshconfig.DefaultPaths(); err == nil {
				if b, err := os.ReadFile(paths.UserConfig); err == nil && strings.Contains(string(b), "# BEGIN castra") {
					fmt.Fprintf(out, "ssh config: %s includes castra's hosts\n", paths.UserConfig)
				} else {
					fmt.Fprintf(out, "ssh config: no castra Include in %s yet (added on the first `castra up`)\n", paths.UserConfig)
				}
			}
			if !ok {
				return errors.New("doctor found missing pieces")
			}
			return nil
		},
	}
}

// lingering reports whether systemd keeps the user's processes alive after
// logout. Without it, rootless Podman containers die with the last session.
// Where loginctl is absent (no systemd), there is nothing to warn about.
func lingering(ctx context.Context) bool {
	out, err := exec.CommandContext(ctx, "loginctl", "show-user", os.Getenv("USER"), "--property=Linger", "--value").Output()
	if err != nil {
		return true
	}
	return strings.TrimSpace(string(out)) == "yes"
}

func newInstallDepsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install-deps",
		Short: "Install the devcontainer CLI " + devcontainer.CLIVersion + " with its own Node, under ~/.local/share/castra",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			prefix, err := devcontainer.InstallPrefix()
			if err != nil {
				return err
			}
			script, err := download(ctx, devcontainer.InstallScriptURL())
			if err != nil {
				return err
			}
			defer os.Remove(script)
			fmt.Fprintf(cmd.ErrOrStderr(), "castra: installing devcontainer CLI %s into %s\n", devcontainer.CLIVersion, prefix)
			run := exec.CommandContext(ctx, "sh", script, "--prefix", prefix, "--version", devcontainer.CLIVersion)
			run.Stdout, run.Stderr = cmd.ErrOrStderr(), cmd.ErrOrStderr()
			if err := run.Run(); err != nil {
				return fmt.Errorf("devcontainer CLI installer: %w", err)
			}
			cli, err := devcontainer.FindCLI()
			if err != nil {
				return err
			}
			v, err := devcontainer.Version(ctx, cli)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "devcontainer: %s (%s)\n", v, cli)
			return nil
		},
	}
}

func download(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s: %s", url, resp.Status)
	}
	f, err := os.CreateTemp("", "castra-install-*.sh")
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := io.Copy(f, resp.Body); err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}
