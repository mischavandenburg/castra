package sshconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mischavandenburg/castra/internal/workspace"
)

func testPaths(t *testing.T) Paths {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	p, err := DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

var demo = workspace.State{Name: "demo", RemoteUser: "dev"}

// TestRenderBlock: the Host block is DevPod's, with castra's ProxyCommand and
// a keepalive, and names no key of castra's.
func TestRenderBlock(t *testing.T) {
	cfg, err := Render(demo, "/opt/my tools/castra")
	if err != nil {
		t.Fatal(err)
	}
	want := "# Managed by castra for workspace demo. Rewritten on `castra up`; edits are lost.\n" +
		"Host demo.castra\n" +
		"  ForwardAgent yes\n" +
		"  LogLevel error\n" +
		"  StrictHostKeyChecking no\n" +
		"  UserKnownHostsFile /dev/null\n" +
		"  ProxyCommand '/opt/my tools/castra' proxy demo\n" +
		"  User dev\n" +
		"  ServerAliveInterval 30\n"
	if cfg != want {
		t.Fatalf("Host block:\n%s\nwant:\n%s", cfg, want)
	}
}

// TestRenderRefusesUnsafeValues: the remote user comes from devcontainer.json
// and is written into ssh_config here, so this is where it is checked.
func TestRenderRefusesUnsafeValues(t *testing.T) {
	for _, bad := range []string{"dev\nProxyCommand evil", `dev"`, "dev\tx", ""} {
		if _, err := Render(workspace.State{Name: "demo", RemoteUser: bad}, "/usr/bin/castra"); err == nil {
			t.Errorf("remote user %q was written", bad)
		}
	}
	cfg, err := Render(workspace.State{Name: "demo", RemoteUser: "dev user"}, "/usr/bin/castra")
	if err != nil || !strings.Contains(cfg, "User \"dev user\"\n") {
		t.Fatalf("a user with a space should be quoted: %v\n%s", err, cfg)
	}
	if _, err := Render(demo, "/opt/\"quoted\"/castra"); err == nil {
		t.Error("a binary path with a double quote was written")
	}
}

func TestProxyCommandEscapesPercent(t *testing.T) {
	got, err := ProxyCommand("/home/a%b/castra", "demo")
	if err != nil || got != "/home/a%%b/castra proxy demo" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestWriteHostAndRemoveHost(t *testing.T) {
	p := testPaths(t)
	kept := workspace.State{Name: "kept", RemoteUser: "dev"}
	for _, s := range []workspace.State{demo, kept} {
		if err := p.WriteHost(s, "/usr/bin/castra"); err != nil {
			t.Fatal(err)
		}
	}
	cfg, _ := os.ReadFile(p.Config)
	if !strings.Contains(string(cfg), "Include "+filepath.Join(p.HostsDir, "*.conf")+"\n") {
		t.Fatalf("castra's config:\n%s", cfg)
	}
	block, _ := os.ReadFile(p.HostFile("demo"))
	if !strings.Contains(string(block), "Host demo.castra\n") {
		t.Fatalf("demo's host file:\n%s", block)
	}
	if err := p.RemoveHost(demo); err != nil {
		t.Fatal(err)
	}
	if err := p.RemoveHost(demo); err != nil {
		t.Fatalf("removing twice: %v", err)
	}
	if _, err := os.Stat(p.HostFile("demo")); err == nil {
		t.Fatal("demo's host file still exists")
	}
	if _, err := os.Stat(p.HostFile("kept")); err != nil {
		t.Fatal("removing one workspace touched another")
	}
}

// TestMigrateFromKeyedLayouts: both earlier layouts (one shared config and
// known_hosts; one file per workspace with pins and a client key) end as one
// DevPod-style host file per workspace, with every key and pin file gone.
func TestMigrateFromKeyedLayouts(t *testing.T) {
	p := testPaths(t)
	if err := os.MkdirAll(filepath.Join(p.Dir, "known_hosts.d"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"known_hosts":        "demo.castra ssh-ed25519 AAAA\n",
		"known_hosts.d/kept": "kept.castra ssh-ed25519 AAAA\n",
		"id_ed25519":         "private\n",
		"id_ed25519.pub":     "ssh-ed25519 AAAA castra\n",
		"config":             "Host demo.castra\n  User dev\n  IdentityFile ~/.ssh/castra/id_ed25519\n",
	} {
		path := filepath.Join(p.Dir, name)
		_ = os.MkdirAll(filepath.Dir(path), 0o700)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	kept := workspace.State{Name: "kept", RemoteUser: "dev"}
	load := func() ([]workspace.State, error) { return []workspace.State{demo, kept}, nil }
	if err := p.Migrate(load, "/usr/bin/castra"); err != nil {
		t.Fatal(err)
	}
	for _, s := range []workspace.State{demo, kept} {
		block, err := os.ReadFile(p.HostFile(s.Name))
		if err != nil || !strings.Contains(string(block), "StrictHostKeyChecking no\n") || strings.Contains(string(block), "IdentityFile") {
			t.Fatalf("%s's host file after the migration (%v):\n%s", s.Name, err, block)
		}
	}
	for _, name := range legacy {
		if _, err := os.Lstat(filepath.Join(p.Dir, name)); err == nil {
			t.Fatalf("%s is still there after the migration", name)
		}
	}
	cfg, _ := os.ReadFile(p.Config)
	if !strings.Contains(string(cfg), "Include ") || strings.Contains(string(cfg), "Host demo.castra") {
		t.Fatalf("castra's config after the migration:\n%s", cfg)
	}
	if err := p.Migrate(func() ([]workspace.State, error) {
		t.Fatal("a second migration should not load anything")
		return nil, nil
	}, "/usr/bin/castra"); err != nil {
		t.Fatalf("a second migration: %v", err)
	}
}

func TestEnsureIncludeOnceAndKeepsUserConfig(t *testing.T) {
	p := testPaths(t)
	if err := os.MkdirAll(p.SSHDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.UserConfig, []byte("Host mine\n  User me\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if p.HasInclude() {
		t.Fatal("HasInclude before the Include was added")
	}
	for i, wantChanged := range []bool{true, false} {
		changed, err := p.EnsureInclude()
		if err != nil || changed != wantChanged {
			t.Fatalf("run %d: changed=%v err=%v", i, changed, err)
		}
	}
	b, _ := os.ReadFile(p.UserConfig)
	got := string(b)
	if strings.Count(got, "Include "+p.Config) != 1 || !strings.HasSuffix(got, "Host mine\n  User me\n") {
		t.Fatalf("user config now:\n%s", got)
	}
	if fi, _ := os.Stat(p.UserConfig); fi.Mode().Perm() != 0o640 {
		t.Fatalf("mode changed to %v", fi.Mode().Perm())
	}
	if !p.HasInclude() {
		t.Fatal("HasInclude after the Include was added")
	}
}

// TestEnsureIncludeWritesThroughSymlink: dotfiles managers such as stow and
// yadm make ~/.ssh/config a symlink; castra must write the target, not
// replace the link with a plain file.
func TestEnsureIncludeWritesThroughSymlink(t *testing.T) {
	p := testPaths(t)
	if err := os.MkdirAll(p.SSHDir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "ssh_config")
	if err := os.WriteFile(target, []byte("Host mine\n  User me\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, p.UserConfig); err != nil {
		t.Fatal(err)
	}
	if _, err := p.EnsureInclude(); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(p.UserConfig); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("~/.ssh/config is no longer a symlink: %v, %v", fi, err)
	}
	b, _ := os.ReadFile(target)
	if !strings.Contains(string(b), "Include "+p.Config) || !strings.HasSuffix(string(b), "Host mine\n  User me\n") {
		t.Fatalf("symlink target now:\n%s", b)
	}
}
