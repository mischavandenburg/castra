// Package devcontainer runs Microsoft's devcontainer CLI, which does all of
// the devcontainer.json work: builds, features, lifecycle commands, mounts.
package devcontainer

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/mischavandenburg/castra/internal/workspace"
)

// MinCLIVersion is the oldest devcontainer CLI castra has been verified with
// end to end. Newer releases are accepted, so `mise up` keeps working.
const MinCLIVersion = "0.89.0"

// InstallHint says how to get the CLI. castra never installs it itself.
const InstallHint = `  install it with mise:  mise use -g node@lts npm:@devcontainers/cli
  or with npm:           npm install -g @devcontainers/cli`

// CLI is a devcontainer CLI castra has found, run, and version-checked.
type CLI struct {
	Path    string
	Version string
}

// FindCLI locates the devcontainer CLI ($CASTRA_DEVCONTAINER, then PATH),
// runs it, and checks its version. Every error says how to fix it.
func FindCLI(ctx context.Context) (CLI, error) {
	path := os.Getenv("CASTRA_DEVCONTAINER")
	if path == "" {
		p, err := exec.LookPath("devcontainer")
		if err != nil {
			return CLI{}, fmt.Errorf("the devcontainer CLI is not on your PATH; castra needs it to build containers\n%s", InstallHint)
		}
		path = p
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, path, "--version")
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(string(out))
		}
		if detail != "" {
			detail = ":\n  " + strings.ReplaceAll(detail, "\n", "\n  ")
		}
		return CLI{Path: path}, fmt.Errorf("the devcontainer CLI at %s does not run (%v)%s\n%s", path, err, detail, InstallHint)
	}

	version := lastLine(string(out))
	got, ok := parseVersion(version)
	if !ok {
		return CLI{Path: path}, fmt.Errorf("the devcontainer CLI at %s printed %q for --version, which is not a version", path, version)
	}
	want, _ := parseVersion(MinCLIVersion)
	if older(got, want) {
		return CLI{Path: path, Version: version}, fmt.Errorf(
			"the devcontainer CLI at %s is version %s; castra needs %s or newer\n  upgrade it with mise:  mise up npm:@devcontainers/cli\n  or with npm:           npm install -g @devcontainers/cli",
			path, version, MinCLIVersion)
	}
	return CLI{Path: path, Version: version}, nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// parseVersion reads "1.2.3", "v1.2.3" or "1.2.3-beta.1" as three numbers.
func parseVersion(s string) ([3]int, bool) {
	var v [3]int
	s = strings.TrimPrefix(s, "v")
	if i := strings.IndexAny(s, "-+ "); i >= 0 {
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return v, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return v, false
		}
		v[i] = n
	}
	return v, true
}

func older(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// UpOptions describes one `devcontainer up` run.
type UpOptions struct {
	CLI        string
	EnginePath string
	Folder     string
	Name       string
	Recreate   bool
	Stderr     io.Writer
	// HangGrace is how long castra waits for the CLI to exit after it has
	// printed its result (devcontainers/cli issue 1236: on Podman it can wait
	// forever for a start event it already missed).
	HangGrace time.Duration
}

// Args is the argv castra passes to `devcontainer`. Dotfiles flags are never
// among them: the CLI installs dotfiles without the forwarded agent, which
// fails on a private repo, so castra installs them itself over SSH.
func (o UpOptions) Args() []string {
	args := []string{
		"up",
		"--docker-path", o.EnginePath,
		"--workspace-folder", o.Folder,
		"--id-label", workspace.Label + "=" + o.Name,
		"--log-format", "text",
		// The resolved remoteEnv comes back in the result, for castra's sessions.
		"--include-merged-configuration",
	}
	// The devcontainer markers castra's sessions get, for the lifecycle
	// commands too (DevPod sets them on the container itself).
	for _, kv := range MarkerEnv(o.Name) {
		args = append(args, "--remote-env", kv)
	}
	if o.Recreate {
		args = append(args, "--remove-existing-container")
	}
	return args
}

// MarkerEnv is what tells dotfiles and scripts they run in a devcontainer:
// REMOTE_CONTAINERS is the VS Code and DevPod convention (DevPod also sets
// DEVPOD and DEVPOD_WORKSPACE_ID, which CASTRA and CASTRA_WORKSPACE mirror).
func MarkerEnv(name string) []string {
	return []string{"REMOTE_CONTAINERS=true", "CASTRA=true", "CASTRA_WORKSPACE=" + name}
}

// Result is what `devcontainer up` prints on success.
type Result struct {
	Outcome               string `json:"outcome"`
	Message               string `json:"message"`
	Description           string `json:"description"`
	ContainerID           string `json:"containerId"`
	RemoteUser            string `json:"remoteUser"`
	RemoteWorkspaceFolder string `json:"remoteWorkspaceFolder"`
	MergedConfiguration   struct {
		// RemoteEnv is devcontainer.json's remoteEnv with ${containerEnv:...}
		// already resolved; a null value means unset the variable.
		RemoteEnv map[string]*string `json:"remoteEnv"`
	} `json:"mergedConfiguration"`
}

// Up runs `devcontainer up` and returns its result. Build output streams to
// opts.Stderr as it happens.
func Up(ctx context.Context, opts UpOptions) (Result, error) {
	grace := opts.HangGrace
	if grace == 0 {
		grace = 30 * time.Second
	}
	cmd := exec.Command(opts.CLI, opts.Args()...)
	cmd.Stderr = opts.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Result{}, err
	}
	if err := cmd.Start(); err != nil {
		return Result{}, fmt.Errorf("start devcontainer CLI: %w", err)
	}

	results := make(chan Result, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
		for sc.Scan() {
			var r Result
			line := strings.TrimSpace(sc.Text())
			if strings.HasPrefix(line, "{") && json.Unmarshal([]byte(line), &r) == nil && r.Outcome != "" {
				results <- r
				return
			}
			if opts.Stderr != nil && line != "" {
				fmt.Fprintln(opts.Stderr, line)
			}
		}
		close(results)
	}()
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	var res Result
	var gotResult bool
	select {
	case r, ok := <-results:
		res, gotResult = r, ok
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		return Result{}, ctx.Err()
	}
	if !gotResult {
		err := <-exited
		return Result{}, fmt.Errorf("devcontainer up printed no result: %v", err)
	}

	select {
	case <-exited:
	case <-time.After(grace):
		_ = cmd.Process.Kill()
		if opts.Stderr != nil {
			fmt.Fprintf(opts.Stderr, "castra: the devcontainer CLI printed its result but did not exit within %s; continuing\n", grace)
		}
	}
	if res.Outcome != "success" {
		return res, fmt.Errorf("devcontainer up failed: %s %s", res.Message, res.Description)
	}
	if res.ContainerID == "" || res.RemoteUser == "" {
		return res, fmt.Errorf("devcontainer up result lacks containerId or remoteUser")
	}
	return res, nil
}
