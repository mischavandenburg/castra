package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"

	"al.essio.dev/pkg/shellescape"

	"github.com/mischavandenburg/castra/internal/sshconfig"
	"github.com/mischavandenburg/castra/internal/workspace"
)

// dotfilesScript clones the dotfiles repo into ~/dotfiles and runs its
// installer. It runs inside the container through castra's own SSH session,
// so a private repo cloned over SSH uses the forwarded agent.
func dotfilesScript(repo, install string, refresh bool) string {
	q := shellescape.Quote
	refreshFlag := "0"
	if refresh {
		refreshFlag = "1"
	}
	return `set -e
d="$HOME/dotfiles"
export GIT_SSH_COMMAND="ssh -o StrictHostKeyChecking=accept-new"
if [ -d "$d/.git" ]; then
  if [ ` + refreshFlag + ` != 1 ]; then echo "castra: dotfiles already in $d, skipping"; exit 0; fi
  git -C "$d" pull --ff-only
else
  git clone ` + q(repo) + ` "$d"
fi
cd "$d"
install=` + q(install) + `
if [ -n "$install" ]; then sh -c "$install"; exit 0; fi
for f in install.sh install bootstrap.sh bootstrap setup.sh setup; do
  if [ -f "$f" ]; then chmod +x "$f"; echo "castra: running dotfiles/$f"; "./$f"; exit 0; fi
done
echo "castra: no install script in $d"
`
}

func installDotfiles(ctx context.Context, st workspace.State, paths sshconfig.Paths, repo, install string, refresh bool, log io.Writer) error {
	fmt.Fprintf(log, "castra: installing dotfiles from %s\n", repo)
	ssh, err := exec.LookPath("ssh")
	if err != nil {
		return fmt.Errorf("ssh not found on PATH: %w", err)
	}
	remote := "sh -c " + shellescape.Quote(dotfilesScript(repo, install, refresh))
	cmd := exec.CommandContext(ctx, ssh, "-F", paths.Config, st.Alias(), remote)
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.Env = os.Environ() // carries SSH_AUTH_SOCK, so the agent is forwarded
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("dotfiles: %w", err)
	}
	return nil
}
