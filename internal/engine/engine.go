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

// errNoSuchContainer is the engine's answer when it does not know the
// container; inspect maps it here so callers can tell it from an engine that
// cannot be asked.
var errNoSuchContainer = errors.New("no such container")

// inspect returns the trimmed output of an inspect with the given format. An
// engine that cannot be asked is an error, never a missing container.
func (e Engine) inspect(ctx context.Context, id, format string) (string, error) {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, e.Path, "inspect", "--format", format, id)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if strings.Contains(strings.ToLower(stderr.String()), "no such") {
			return "", fmt.Errorf("%w: %s", errNoSuchContainer, ShortID(id))
		}
		return "", fmt.Errorf("%s inspect %s: %v: %s", e.Name, ShortID(id), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

// Running reports whether the container exists and is running.
func (e Engine) Running(ctx context.Context, id string) (bool, error) {
	out, err := e.inspect(ctx, id, "{{.State.Running}}")
	if err != nil {
		return false, err
	}
	return out == "true", nil
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
	_, err := e.inspect(ctx, id, "{{.Id}}")
	if errors.Is(err, errNoSuchContainer) {
		return false, nil
	}
	return err == nil, err
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

// Stop decides whether the container needs stopping and does it, giving it
// ten seconds like Remove. A paused container is refused rather than reported
// stopped, because podman's stop leaves it frozen. A container that no longer
// exists is an error, so the user learns to run `castra up`; an engine that
// cannot be reached is an error too. The container and castra's records stay.
func (e Engine) Stop(ctx context.Context, id string) (bool, error) {
	status, err := e.inspect(ctx, id, "{{.State.Status}}")
	switch {
	case errors.Is(err, errNoSuchContainer):
		return false, fmt.Errorf("container %s no longer exists: run `castra up`", ShortID(id))
	case err != nil:
		return false, err
	}
	switch status {
	case "running", "restarting":
		var stderr bytes.Buffer
		cmd := exec.CommandContext(ctx, e.Path, "stop", "-t", "10", id)
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return false, fmt.Errorf("%s stop %s: %v: %s", e.Name, ShortID(id), err, strings.TrimSpace(stderr.String()))
		}
		return true, nil
	case "paused":
		return false, fmt.Errorf("container %s is paused: run `%s unpause %s` first", ShortID(id), e.Name, ShortID(id))
	default:
		return false, nil
	}
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
