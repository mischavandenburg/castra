package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mischavandenburg/castra/internal/pick"
	"github.com/mischavandenburg/castra/internal/workspace"
)

// chooseWorkspace asks which workspace to use when none was named. With
// autoSingle, a lone workspace is used without asking.
func chooseWorkspace(cmd *cobra.Command, title string, autoSingle bool) (workspace.State, error) {
	states, err := workspace.All()
	if err != nil {
		return workspace.State{}, err
	}
	switch {
	case len(states) == 0:
		return workspace.State{}, errors.New("no workspaces yet: run `castra up` in a folder with a .devcontainer")
	case len(states) == 1 && autoSingle:
		fmt.Fprintf(cmd.ErrOrStderr(), "castra: %s is the only workspace\n", states[0].Name)
		return states[0], nil
	}

	width := 0
	for _, st := range states {
		width = max(width, len(st.Name))
	}
	status := containerStates(cmd.Context(), states)
	items := make([]string, len(states))
	for i, st := range states {
		items[i] = fmt.Sprintf("%-*s  %-8s  %s", width, st.Name, status[st.Name], st.Folder)
	}
	i, err := pick.Pick(os.Stdin, cmd.ErrOrStderr(), title, items)
	if errors.Is(err, pick.ErrCanceled) {
		return workspace.State{}, errors.New("nothing chosen")
	}
	if err != nil {
		return workspace.State{}, fmt.Errorf("name the workspace (%s)", strings.TrimSuffix(err.Error(), "."))
	}
	return states[i], nil
}
