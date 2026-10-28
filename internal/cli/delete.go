package cli

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/mischavandenburg/castra/internal/engine"
	"github.com/mischavandenburg/castra/internal/sshconfig"
	"github.com/mischavandenburg/castra/internal/workspace"
)

func newDeleteCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "delete [workspace]",
		Aliases: []string{"rm"},
		Short:   "Remove a workspace's container and castra's records of it (castra movere: break camp)",
		Long: `Remove a workspace's container, its SSH host entry and pinned host key, and castra's record of it.
Your folder, the images and any named volumes stay. Without a name, castra shows a menu.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var st workspace.State
			var err error
			if len(args) == 1 {
				st, err = workspace.Load(args[0])
			} else {
				st, err = chooseWorkspace(cmd, "Delete which workspace? (arrows or j/k, Enter, q to quit)", false)
			}
			if err != nil {
				return err
			}
			if !yes {
				ok, err := confirm(cmd, fmt.Sprintf("Delete workspace %s? This removes container %s and everything in it that is not on a mount; the folder %s stays. [y/N] ",
					st.Name, engine.ShortID(st.ContainerID), st.Folder))
				if err != nil {
					return err
				}
				if !ok {
					return errors.New("not deleted")
				}
			}
			return deleteWorkspace(cmd, st)
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return cmd
}

// confirm asks a yes/no question on the terminal. Without a terminal there is
// nobody to ask, so it refuses rather than guess.
func confirm(cmd *cobra.Command, question string) (bool, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return false, errors.New("stdin is not a terminal, so castra cannot ask; pass --yes to delete")
	}
	fmt.Fprint(cmd.ErrOrStderr(), question)
	answer, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return false, err
	}
	a := strings.ToLower(strings.TrimSpace(answer))
	return a == "y" || a == "yes", nil
}

func deleteWorkspace(cmd *cobra.Command, st workspace.State) error {
	held, err := workspace.Lock(st.Name)
	if err != nil {
		return err
	}
	defer func() { _ = held.Release() }()

	eng, err := engine.Detect(st.Engine)
	if err != nil {
		return err
	}
	if err := eng.Remove(cmd.Context(), st.ContainerID); err != nil {
		return err
	}
	paths, err := sshconfig.DefaultPaths()
	if err != nil {
		return err
	}
	if err := paths.RemoveHost(st); err != nil {
		return err
	}
	if err := workspace.Delete(st.Name); err != nil {
		return err
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "castra: deleted %s (container %s); %s is untouched\n", st.Name, engine.ShortID(st.ContainerID), st.Folder)
	return nil
}
