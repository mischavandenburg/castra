// Package devcontainer runs Microsoft's devcontainer CLI, which does all of
// the devcontainer.json work: builds, features, lifecycle commands, mounts.
package devcontainer

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/mischavandenburg/castra/internal/workspace"
)

// CLIVersion is the devcontainer CLI release `castra install-deps` installs.
const CLIVersion = "0.90.0"

// InstallPrefix is where `castra install-deps` puts the CLI and its own Node.
func InstallPrefix() (string, error) {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, "castra"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "castra"), nil
}

// FindCLI locates the devcontainer CLI: $CASTRA_DEVCONTAINER, then PATH, then
// the copy `castra install-deps` installed.
func FindCLI() (string, error) {
	if p := os.Getenv("CASTRA_DEVCONTAINER"); p != "" {
		return p, nil
	}
	if p, err := exec.LookPath("devcontainer"); err == nil {
		return p, nil
	}
	if prefix, err := InstallPrefix(); err == nil {
		p := filepath.Join(prefix, "bin", "devcontainer")
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p, nil
		}
	}
	return "", errors.New("devcontainer CLI not found: run `castra install-deps`")
}

// Version asks the CLI for its version.
func Version(ctx context.Context, cli string) (string, error) {
	out, err := exec.CommandContext(ctx, cli, "--version").Output()
	return strings.TrimSpace(string(out)), err
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
	}
	if o.Recreate {
		args = append(args, "--remove-existing-container")
	}
	return args
}

// Result is what `devcontainer up` prints on success.
type Result struct {
	Outcome               string `json:"outcome"`
	Message               string `json:"message"`
	Description           string `json:"description"`
	ContainerID           string `json:"containerId"`
	RemoteUser            string `json:"remoteUser"`
	RemoteWorkspaceFolder string `json:"remoteWorkspaceFolder"`
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

// InstallScriptURL is the official installer, pinned to the CLI release.
func InstallScriptURL() string {
	return "https://raw.githubusercontent.com/devcontainers/cli/v" + CLIVersion + "/scripts/install.sh"
}
