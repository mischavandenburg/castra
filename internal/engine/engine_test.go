package engine

import (
	"context"
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
