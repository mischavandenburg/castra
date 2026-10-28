package devcontainer

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestArgsNeverCarryDotfilesFlags(t *testing.T) {
	for _, recreate := range []bool{false, true} {
		args := UpOptions{EnginePath: "/usr/bin/podman", Folder: "/src/demo", Name: "demo", Recreate: recreate}.Args()
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "--dotfiles") {
			t.Fatalf("dotfiles flag in %q", joined)
		}
		for _, want := range []string{"up", "--docker-path /usr/bin/podman", "--workspace-folder /src/demo", "--id-label castra.workspace=demo"} {
			if !strings.Contains(joined, want) {
				t.Fatalf("%q missing from %q", want, joined)
			}
		}
		if got := strings.Contains(joined, "--remove-existing-container"); got != recreate {
			t.Fatalf("recreate=%v but args are %q", recreate, joined)
		}
		for _, want := range []string{"--include-merged-configuration", "--remote-env REMOTE_CONTAINERS=true", "--remote-env CASTRA=true", "--remote-env CASTRA_WORKSPACE=demo"} {
			if !strings.Contains(joined, want) {
				t.Fatalf("%q missing from %q", want, joined)
			}
		}
	}
}

func TestUpReturnsResolvedRemoteEnv(t *testing.T) {
	cli := fakeCLI(t, `echo '{"outcome":"success","containerId":"abc","remoteUser":"dev","remoteWorkspaceFolder":"/workspaces/demo","mergedConfiguration":{"remoteEnv":{"PATH":"/usr/bin:/opt/x","GONE":null}}}'`)
	res, err := Up(context.Background(), UpOptions{CLI: cli, EnginePath: "podman", Folder: "/x", Name: "demo", Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	env := res.MergedConfiguration.RemoteEnv
	if p := env["PATH"]; p == nil || *p != "/usr/bin:/opt/x" {
		t.Fatalf("PATH = %v", p)
	}
	if gone, ok := env["GONE"]; !ok || gone != nil {
		t.Fatalf("GONE should be present and null, got %v %v", gone, ok)
	}
}

// fakeCLI writes a script that stands in for the devcontainer CLI.
func fakeCLI(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "devcontainer")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestUpSurvivesHangingCLI covers devcontainers/cli issue 1236: the CLI
// prints its result and then never exits.
func TestUpSurvivesHangingCLI(t *testing.T) {
	cli := fakeCLI(t, `echo '{"outcome":"success","containerId":"abc123","remoteUser":"dev","remoteWorkspaceFolder":"/workspaces/demo"}'; exec sleep 60`)
	startedAt := time.Now()
	res, err := Up(context.Background(), UpOptions{CLI: cli, EnginePath: "podman", Folder: "/x", Name: "demo", Stderr: io.Discard, HangGrace: 500 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(startedAt); took > 5*time.Second {
		t.Fatalf("Up waited %s on a hanging CLI", took)
	}
	if res.ContainerID != "abc123" || res.RemoteUser != "dev" || res.RemoteWorkspaceFolder != "/workspaces/demo" {
		t.Fatalf("result %+v", res)
	}
}

func TestUpReportsCLIError(t *testing.T) {
	cli := fakeCLI(t, `echo '{"outcome":"error","message":"build failed","description":"no such image"}'; exit 1`)
	_, err := Up(context.Background(), UpOptions{CLI: cli, EnginePath: "podman", Folder: "/x", Name: "demo", Stderr: io.Discard})
	if err == nil || !strings.Contains(err.Error(), "build failed") {
		t.Fatalf("want the CLI's error, got %v", err)
	}
}

func TestFindCLIMissing(t *testing.T) {
	t.Setenv("CASTRA_DEVCONTAINER", "")
	t.Setenv("PATH", t.TempDir())
	_, err := FindCLI(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not on your PATH") || !strings.Contains(err.Error(), "mise use -g node@lts npm:@devcontainers/cli") {
		t.Fatalf("missing CLI: %v", err)
	}
}

func TestFindCLIOnPath(t *testing.T) {
	dir := filepath.Dir(fakeCLI(t, `echo 0.90.0`))
	t.Setenv("CASTRA_DEVCONTAINER", "")
	t.Setenv("PATH", dir)
	cli, err := FindCLI(context.Background())
	if err != nil || cli.Version != "0.90.0" || cli.Path != filepath.Join(dir, "devcontainer") {
		t.Fatalf("FindCLI = %+v, %v", cli, err)
	}
}

func TestFindCLIChecks(t *testing.T) {
	cases := []struct {
		name, script string
		wantErr      string // empty: must succeed
	}{
		{"oldest verified release", `echo 0.89.0`, ""},
		{"newer release", `echo 1.4.2`, ""},
		{"v prefix and a log line", `echo "starting"; echo v0.91.0`, ""},
		{"too old", `echo 0.88.9`, "castra needs 0.89.0 or newer"},
		{"mise shim with no version set", `echo "mise ERROR No version is set for shim: devcontainer" >&2; exit 1`, "No version is set for shim"},
		{"broken node", `echo "node: not found" >&2; exit 127`, "does not run"},
		{"not a version", `echo "hello"`, "not a version"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("CASTRA_DEVCONTAINER", fakeCLI(t, c.script))
			_, err := FindCLI(context.Background())
			switch {
			case c.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)):
				t.Fatalf("want an error containing %q, got %v", c.wantErr, err)
			}
		})
	}
}

// TestUpIsCancelled: a cancelled context ends the CLI and its process group
// (a build in progress) and reports the cancellation, so Ctrl-C during a
// long build does not leave work running.
func TestUpIsCancelled(t *testing.T) {
	cli := fakeCLI(t, `sleep 60; echo '{"outcome":"success","containerId":"abc","remoteUser":"dev"}'`)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel()
	}()
	startedAt := time.Now()
	_, err := Up(ctx, UpOptions{CLI: cli, EnginePath: "podman", Folder: "/x", Name: "demo", Stderr: io.Discard})
	if err != context.Canceled {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if took := time.Since(startedAt); took > 8*time.Second {
		t.Fatalf("Up took %s to stop after the cancellation", took)
	}
}

func TestUpWithoutResultFails(t *testing.T) {
	cli := fakeCLI(t, `echo "not json"; exit 2`)
	if _, err := Up(context.Background(), UpOptions{CLI: cli, EnginePath: "podman", Folder: "/x", Name: "demo", Stderr: io.Discard}); err == nil {
		t.Fatal("Up succeeded although the CLI printed no result")
	}
}
