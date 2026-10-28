package cli

import (
	"context"
	"fmt"
	"os/exec"

	"github.com/mischavandenburg/castra/internal/engine"
	"github.com/mischavandenburg/castra/internal/workspace"
)

// containerStates is the state of every workspace's container, by workspace
// name, from one `ps` per engine: "missing" when the container is gone,
// "unknown" when its engine is not installed or cannot be asked.
func containerStates(ctx context.Context, states []workspace.State) map[string]string {
	out := make(map[string]string, len(states))
	byEngine := map[string]map[string]string{}
	for _, st := range states {
		known, seen := byEngine[st.Engine]
		if !seen {
			if eng, err := engine.Detect(st.Engine); err == nil {
				known, _ = eng.States(ctx, workspace.Label)
			}
			byEngine[st.Engine] = known
		}
		switch {
		case known == nil:
			out[st.Name] = "unknown"
		case known[st.ContainerID] == "":
			out[st.Name] = "missing"
		default:
			out[st.Name] = known[st.ContainerID]
		}
	}
	return out
}

// sshPath finds the OpenSSH client.
func sshPath() (string, error) {
	p, err := exec.LookPath("ssh")
	if err != nil {
		return "", fmt.Errorf("ssh not found on PATH: %w", err)
	}
	return p, nil
}
