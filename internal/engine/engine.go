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
	"slices"
	"strings"
)

// Engine is a container engine CLI on PATH.
type Engine struct {
	Name string // "podman" or "docker"
	Path string
}

// Names are the engines castra knows, in the order Detect tries them.
var Names = []string{"podman", "docker"}

// Detect finds the engine to use: the preferred one if named, otherwise the
// first of Names that is installed.
func Detect(preferred string) (Engine, error) {
	candidates := Names
	if preferred != "" {
		if !slices.Contains(Names, preferred) {
			return Engine{}, fmt.Errorf("unknown container engine %q: castra runs on %s", preferred, strings.Join(Names, " or "))
		}
		candidates = []string{preferred}
	}
	for _, c := range candidates {
		if p, err := exec.LookPath(c); err == nil {
			return Engine{Name: c, Path: p}, nil
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
		return false, fmt.Errorf("%s inspect %s: %v: %s", e.Name, ShortID(id), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)) == "true", nil
}

// States lists every container carrying label, by full id, with its state as
// the engine names it (running, exited, created, paused). One call serves a
// whole `castra ls`.
func (e Engine) States(ctx context.Context, label string) (map[string]string, error) {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, e.Path, "ps", "-a", "--no-trunc", "--filter", "label="+label, "--format", "{{.ID}} {{.State}}")
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s ps: %v: %s", e.Name, err, strings.TrimSpace(stderr.String()))
	}
	states := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if id, state, ok := strings.Cut(line, " "); ok {
			states[id] = state
		}
	}
	return states, nil
}

// exists reports whether the engine knows the container. An engine that
// cannot be asked is an error, never a missing container.
func (e Engine) exists(ctx context.Context, id string) (bool, error) {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, e.Path, "inspect", "--format", "{{.Id}}", id)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if strings.Contains(strings.ToLower(stderr.String()), "no such") {
			return false, nil
		}
		return false, fmt.Errorf("%s inspect %s: %v: %s", e.Name, ShortID(id), err, strings.TrimSpace(stderr.String()))
	}
	return true, nil
}

// Remove stops the container (giving it ten seconds) and removes it. A
// container that no longer exists is not an error; an engine that cannot be
// reached is, so `castra delete` never reports success for nothing. Images
// and named volumes are left alone.
func (e Engine) Remove(ctx context.Context, id string) error {
	exists, err := e.exists(ctx, id)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	_ = exec.CommandContext(ctx, e.Path, "stop", "-t", "10", id).Run()
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, e.Path, "rm", id)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s rm %s: %v: %s", e.Name, ShortID(id), err, strings.TrimSpace(stderr.String()))
	}
	return nil
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
		return out, fmt.Errorf("%s exec in %s: %v: %s", e.Name, ShortID(id), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// ShortID is the twelve-character container id the engines print themselves.
func ShortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
