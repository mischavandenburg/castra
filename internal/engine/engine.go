// Package engine runs the container engine's CLI (podman or docker). castra
// never talks to an engine API; everything goes through the same commands a
// user would type.
package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
)

// Engine is a container engine CLI on PATH.
type Engine struct {
	Name string // "podman" or "docker"
	Path string
}

// Detect finds the engine to use: the preferred one if named, otherwise
// podman, then docker.
func Detect(preferred string) (Engine, error) {
	candidates := []string{"podman", "docker"}
	if preferred != "" {
		candidates = []string{preferred}
	}
	for _, c := range candidates {
		if p, err := exec.LookPath(c); err == nil {
			return Engine{Name: filepath.Base(c), Path: p}, nil
		}
	}
	if preferred != "" {
		return Engine{}, fmt.Errorf("container engine %q not found on PATH", preferred)
	}
	return Engine{}, errors.New("no container engine found: install podman (or docker)")
}

// Version returns the engine's own version line.
func (e Engine) Version(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, e.Path, "--version").Output()
	return strings.TrimSpace(string(out)), err
}

// Running reports whether the container exists and is running.
func (e Engine) Running(ctx context.Context, id string) (bool, error) {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, e.Path, "inspect", "--format", "{{.State.Running}}", id)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("%s inspect %s: %v: %s", e.Name, short(id), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)) == "true", nil
}

// Status is the container's state as the engine names it (running, exited,
// created, paused), or "missing" when the container no longer exists.
func (e Engine) Status(ctx context.Context, id string) string {
	out, err := exec.CommandContext(ctx, e.Path, "inspect", "--format", "{{.State.Status}}", id).Output()
	if err != nil {
		return "missing"
	}
	return strings.TrimSpace(string(out))
}

// ExecArgs is the argv that runs args inside the container as user, with
// stdin attached and no terminal.
func (e Engine) ExecArgs(user, id string, args ...string) []string {
	argv := []string{e.Path, "exec", "-i"}
	if user != "" {
		argv = append(argv, "-u", user)
	}
	argv = append(argv, id)
	return append(argv, args...)
}

// Exec runs args inside the container as user and returns stdout.
func (e Engine) Exec(ctx context.Context, user, id string, stdin io.Reader, args ...string) ([]byte, error) {
	argv := e.ExecArgs(user, id, args...)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin = stdin
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("%s exec in %s: %v: %s", e.Name, short(id), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func short(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
