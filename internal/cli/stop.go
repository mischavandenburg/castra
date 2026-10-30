package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/mischavandenburg/castra/internal/engine"
	"github.com/mischavandenburg/castra/internal/workspace"
)

func newStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop [workspace]",
		Short: "Stop a workspace's container, keeping its records",
		Long: `Stop a workspace's container. Its record and SSH host entry stay,
so a later castra up or castra ssh starts the same container again. Without a name, castra shows a menu.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var st workspace.State
			var err error
			if len(args) == 1 {
				st, err = workspace.Load(args[0])
			} else {
				st, err = chooseWorkspace(cmd, "Stop which workspace? (arrows or j/k, Enter, q to quit)", false)
			}
			if err != nil {
				return err
			}
			return stopWorkspace(cmd, st)
		},
	}
}

func stopWorkspace(cmd *cobra.Command, st workspace.State) error {
	held, err := workspace.Lock(st.Name)
	if err != nil {
		return err
	}
	defer func() { _ = held.Release() }()

	eng, err := engine.Detect(st.Engine)
	if err != nil {
		return err
	}
	stopped, err := eng.Stop(cmd.Context(), st.ContainerID)
	if err != nil {
		return err
	}
	if !stopped {
		fmt.Fprintf(cmd.ErrOrStderr(), "castra: %s is already stopped (container %s)\n", st.Name, engine.ShortID(st.ContainerID))
		return nil
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "castra: stopped %s (container %s); `castra up %s` or `castra ssh %s` starts it again\n",
		st.Name, engine.ShortID(st.ContainerID), st.Name, st.Name)
	return nil
}
