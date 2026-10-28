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
// so a private repo cloned over SSH uses the forwarded agent. Only a
// successful install writes the marker that later runs skip on, so a failed
// install is retried by the next `castra up`.
func dotfilesScript(repo, install string, refresh bool) string {
	q := shellescape.Quote
	refreshFlag := "0"
	if refresh {
		refreshFlag = "1"
	}
	return `set -e
d="$HOME/dotfiles"
marker="$HOME/.castra/dotfiles-installed"
repo=` + q(repo) + `
if [ -f "$marker" ] && [ "$(cat "$marker")" = "$repo" ] && [ ` + refreshFlag + ` != 1 ]; then
  echo "castra: dotfiles already installed in $d, skipping"; exit 0
fi
export GIT_SSH_COMMAND="ssh -o StrictHostKeyChecking=accept-new"
if [ -d "$d/.git" ]; then
  git -C "$d" pull --ff-only
else
  git clone "$repo" "$d"
fi
cd "$d"
install=` + q(install) + `
if [ -n "$install" ]; then
  sh -c "$install"
else
  ran=0
  for f in install.sh install bootstrap.sh bootstrap setup.sh setup; do
    if [ -f "$f" ]; then chmod +x "$f"; echo "castra: running dotfiles/$f"; "./$f"; ran=1; break; fi
  done
  [ $ran = 1 ] || echo "castra: no install script in $d"
fi
mkdir -p "$(dirname "$marker")" && printf %s "$repo" > "$marker"
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
		return fmt.Errorf("%s is up, but installing your dotfiles failed (%v, output above); fix the cause and run `castra up %s` again to retry", st.Name, err, st.Name)
	}
	return nil
}
