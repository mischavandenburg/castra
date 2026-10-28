package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/mischavandenburg/castra/internal/engine"
	"github.com/mischavandenburg/castra/internal/workspace"
)

type listEntry struct {
	Name   string `json:"name"`
	State  string `json:"state"`
	ID     string `json:"id"`
	Folder string `json:"folder"`
}

func newListCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List workspaces and whether their containers are running",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			states, err := workspace.All()
			if err != nil {
				return err
			}
			entries := make([]listEntry, 0, len(states))
			for _, st := range states {
				status := "unknown"
				if eng, err := engine.Detect(st.Engine); err == nil {
					status = eng.Status(cmd.Context(), st.ContainerID)
				}
				entries = append(entries, listEntry{Name: st.Name, State: status, ID: shortID(st.ContainerID), Folder: st.Folder})
			}
			if asJSON {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(entries)
			}
			return printList(cmd.OutOrStdout(), entries)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

func printList(w io.Writer, entries []listEntry) error {
	if len(entries) == 0 {
		_, err := fmt.Fprintln(w, "no workspaces yet: run `castra up` in a folder with a .devcontainer")
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tSTATE\tCONTAINER\tFOLDER")
	for _, e := range entries {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", e.Name, e.State, e.ID, e.Folder)
	}
	return tw.Flush()
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
