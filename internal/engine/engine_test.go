package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectRejectsUnknownEngine(t *testing.T) {
	_, err := Detect("nerdctl")
	if err == nil || !strings.Contains(err.Error(), "podman or docker") {
		t.Fatalf("Detect(nerdctl) = %v, want a refusal naming the two engines", err)
	}
	if _, err := Detect("/usr/bin/podman"); err == nil {
		t.Fatal("a path is not an engine name")
	}
}

// TestStatesParsesOneLinePerContainer runs States against a fake engine
// that prints what `podman ps -a --no-trunc --format '{{.ID}} {{.State}}'`
// prints.
func TestStatesParsesOneLinePerContainer(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "podman")
	script := "#!/bin/sh\ncase \"$*\" in *ps*) printf 'aaaa1111 running\\nbbbb2222 exited\\n' ;; *) exit 1 ;; esac\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	e := Engine{Name: "podman", Path: fake}
	states, err := e.States(context.Background(), "castra.workspace")
	if err != nil || states["aaaa1111"] != "running" || states["bbbb2222"] != "exited" || len(states) != 2 {
		t.Fatalf("States = %v, %v", states, err)
	}
}

// TestRemoveRefusesWhenTheEngineIsDown: an inspect that fails for a reason
// other than "no such container" is an error, so delete never claims success
// while the container may still exist.
func TestRemoveRefusesWhenTheEngineIsDown(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "podman")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho 'cannot connect to the engine' >&2; exit 125\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	e := Engine{Name: "podman", Path: fake}
	if err := e.Remove(context.Background(), "abc"); err == nil || !strings.Contains(err.Error(), "cannot connect") {
		t.Fatalf("Remove with the engine down = %v, want its error", err)
	}
	gone := filepath.Join(dir, "docker")
	if err := os.WriteFile(gone, []byte("#!/bin/sh\necho 'Error: No such container: abc' >&2; exit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := (Engine{Name: "docker", Path: gone}).Remove(context.Background(), "abc"); err != nil {
		t.Fatalf("Remove of a missing container = %v, want nil", err)
	}
}

// TestStopRefusesWhenTheEngineIsDown: an engine that cannot be asked is an
// error, never a success, just as for Remove.
func TestStopRefusesWhenTheEngineIsDown(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "podman")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho 'cannot connect to the engine' >&2; exit 125\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	e := Engine{Name: "podman", Path: fake}
	if stopped, err := e.Stop(context.Background(), "abc"); err == nil || stopped || !strings.Contains(err.Error(), "cannot connect") {
		t.Fatalf("Stop with the engine down = %v, %v, want its error", stopped, err)
	}
}

// TestStopOfAMissingContainerSaysToRunUp: a container castra can no longer
// find is an error, not a quiet success.
func TestStopOfAMissingContainerSaysToRunUp(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "docker")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho 'Error: No such container: abc' >&2; exit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	e := Engine{Name: "docker", Path: fake}
	if stopped, err := e.Stop(context.Background(), "abc"); err == nil || stopped || !strings.Contains(err.Error(), "castra up") {
		t.Fatalf("Stop of a missing container = %v, %v, want an error naming `castra up`", stopped, err)
	}
}

// TestRunningOfAMissingContainerNamesIt: a caller that passes inspect's
// error through still learns which container the engine could not find.
func TestRunningOfAMissingContainerNamesIt(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "docker")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho 'Error: No such container: abc' >&2; exit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	e := Engine{Name: "docker", Path: fake}
	id := "485830b0b7e9f00dbadc0ffee"
	running, err := e.Running(context.Background(), id)
	if running || err == nil || !errors.Is(err, errNoSuchContainer) || !strings.Contains(err.Error(), ShortID(id)) {
		t.Fatalf("Running of a missing container = %v, %v, want an error naming %s", running, err, ShortID(id))
	}
}

// TestStopStopsARunningContainer pins the stop shape: ten seconds, then the
// container id.
func TestStopStopsARunningContainer(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "podman")
	script := "#!/bin/sh\ncase \"$*\" in\n*inspect*) echo running ;;\n\"stop -t 10 abc\") exit 0 ;;\n*) exit 1 ;;\nesac\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	e := Engine{Name: "podman", Path: fake}
	if stopped, err := e.Stop(context.Background(), "abc"); err != nil || !stopped {
		t.Fatalf("Stop of a running container = %v, %v, want stopped", stopped, err)
	}
}

// TestStopLeavesAnExitedContainerAlone: leaving already stopped is success,
// and castra does not call stop again.
func TestStopLeavesAnExitedContainerAlone(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "docker")
	script := "#!/bin/sh\ncase \"$*\" in\n*inspect*) echo exited ;;\n*) exit 1 ;;\nesac\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	e := Engine{Name: "docker", Path: fake}
	if stopped, err := e.Stop(context.Background(), "abc"); err != nil || stopped {
		t.Fatalf("Stop of an exited container = %v, %v, want not stopped and no error", stopped, err)
	}
}

// TestStopOfAPausedContainerSaysToUnpause: podman reports a paused container
// as not running, and its stop leaves it frozen, so castra refuses instead of
// claiming it stopped.
func TestStopOfAPausedContainerSaysToUnpause(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "podman")
	script := "#!/bin/sh\ncase \"$*\" in\n*inspect*) echo paused ;;\n*) exit 1 ;;\nesac\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	e := Engine{Name: "podman", Path: fake}
	if stopped, err := e.Stop(context.Background(), "abc"); err == nil || stopped || !strings.Contains(err.Error(), "podman unpause abc") {
		t.Fatalf("Stop of a paused container = %v, %v, want an error naming unpause", stopped, err)
	}
}

func TestDetectWithoutEnginesSaysSo(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, err := Detect(""); err == nil || !strings.Contains(err.Error(), "install podman") {
		t.Fatalf("Detect on an empty PATH = %v", err)
	}
	if _, err := Detect("docker"); err == nil || !strings.Contains(err.Error(), `"docker" not found`) {
		t.Fatalf("Detect(docker) on an empty PATH = %v", err)
	}
}

// TestExecArgs pins the exec shape the ProxyCommand depends on: stdin
// attached, no terminal, the user only when there is one.
func TestExecArgs(t *testing.T) {
	e := Engine{Name: "podman", Path: "/usr/bin/podman"}
	got := strings.Join(e.ExecArgs("dev", "abc123", "sh", "-c", "echo hi"), " ")
	if got != "/usr/bin/podman exec -i -u dev abc123 sh -c echo hi" {
		t.Fatalf("ExecArgs = %q", got)
	}
	got = strings.Join(e.ExecArgs("", "abc123", "id"), " ")
	if got != "/usr/bin/podman exec -i abc123 id" {
		t.Fatalf("ExecArgs without a user = %q", got)
	}
}

func TestShortID(t *testing.T) {
	if got := ShortID("485830b0b7e9f00dbadc0ffee"); got != "485830b0b7e9" {
		t.Fatalf("ShortID = %q", got)
	}
	if got := ShortID("short"); got != "short" {
		t.Fatalf("ShortID on a short id = %q", got)
	}
}
