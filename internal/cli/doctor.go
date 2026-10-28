package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
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
			cfgPath, _ := config.Path()
			switch {
			case err != nil:
				fmt.Fprintln(out, "config: error:", err)
				ok = false
			case cfg.Dotfiles.Repo == "":
				fmt.Fprintf(out, "dotfiles: none configured (set dotfiles.repo in %s)\n", cfgPath)
			default:
				fmt.Fprintf(out, "dotfiles: %s\n", cfg.Dotfiles.Repo)
			}
			if eng, err := engine.Detect(cfg.Engine); err != nil {
				fmt.Fprintln(out, "engine: missing (install podman, or docker)")
				ok = false
			} else {
				v, _ := eng.Version(ctx)
				fmt.Fprintf(out, "engine: %s (%s)\n", v, eng.Path)
				if eng.Name == "podman" && os.Getuid() != 0 && !lingering(ctx) {
					fmt.Fprintf(out, "linger: off, so rootless Podman stops your containers when your last login session ends\n"+
						"        (on a machine you reach over SSH, run: sudo loginctl enable-linger %s)\n", userName())
				}
			}
			if cli, err := devcontainer.FindCLI(ctx); err != nil {
				fmt.Fprintln(out, "devcontainer:", err)
				ok = false
			} else {
				fmt.Fprintf(out, "devcontainer: %s (%s)\n", cli.Version, cli.Path)
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
				if paths.HasInclude() {
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
	out, err := exec.CommandContext(ctx, "loginctl", "show-user", userName(), "--property=Linger", "--value").Output()
	if err != nil {
		return true
	}
	return strings.TrimSpace(string(out)) == "yes"
}

// userName is the login name, from the account database rather than $USER,
// which cron jobs and some shells leave unset.
func userName() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return os.Getenv("USER")
}
