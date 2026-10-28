package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/mischavandenburg/castra/internal/shell"
	"github.com/mischavandenburg/castra/internal/sshconfig"
	"github.com/mischavandenburg/castra/internal/workspace"
)

// dotfilesScript clones the dotfiles repo into ~/dotfiles and runs its
// installer. It runs inside the container through castra's own SSH session,
// so a private repo cloned over SSH uses the forwarded agent. Its arguments:
// $1 the repository, $2 the install command or "", $3 "1" to pull and rerun
// when ~/dotfiles already exists.
const dotfilesScript = `set -e
d="$HOME/dotfiles"
export GIT_SSH_COMMAND="ssh -o StrictHostKeyChecking=accept-new"
if [ -d "$d/.git" ]; then
  if [ "$3" != 1 ]; then echo "castra: dotfiles already in $d, skipping"; exit 0; fi
  git -C "$d" pull --ff-only
else
  git clone "$1" "$d"
fi
cd "$d"
if [ -n "$2" ]; then sh -c "$2"; exit 0; fi
for f in install.sh install bootstrap.sh bootstrap setup.sh setup; do
  if [ -f "$f" ]; then chmod +x "$f"; echo "castra: running dotfiles/$f"; "./$f"; exit 0; fi
done
echo "castra: no install script in $d"
`

// dotfilesCommand is the remote command line for the session: sh, the
// script and its three arguments, each quoted once for the remote shell.
func dotfilesCommand(repo, install string, refresh bool) string {
	flag := "0"
	if refresh {
		flag = "1"
	}
	return shell.Join([]string{"sh", "-c", dotfilesScript, "sh", repo, install, flag})
}

func installDotfiles(ctx context.Context, st workspace.State, paths sshconfig.Paths, repo, install string, refresh bool, log io.Writer) error {
	fmt.Fprintf(log, "castra: installing dotfiles from %s\n", repo)
	ssh, err := sshPath()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, ssh, "-F", paths.Config, st.Alias(), dotfilesCommand(repo, install, refresh))
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.Env = os.Environ() // carries SSH_AUTH_SOCK, so the agent is forwarded
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("dotfiles: %w", err)
	}
	return nil
}
