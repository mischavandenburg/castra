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

func TestRenderBlock(t *testing.T) {
	p := testPaths(t)
	cfg, err := Render(demo, "/opt/my tools/castra", p)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Host demo.castra\n",
		"HostKeyAlias demo.castra\n",
		"User dev\n",
		"ProxyCommand '/opt/my tools/castra' proxy demo\n",
		"IdentityFile " + p.ClientKey + "\n",
		"IdentitiesOnly yes\n",
		"UserKnownHostsFile " + p.PinFile("demo") + "\n",
		"StrictHostKeyChecking yes\n",
		"UpdateHostKeys no\n",
		// Only castra's key, never a prompt a hostile server could send.
		"PreferredAuthentications publickey\n",
		"PasswordAuthentication no\n",
		"KbdInteractiveAuthentication no\n",
		// The agent and nothing else, whatever `Host *` says.
		"ForwardAgent yes\n",
		"ForwardX11 no\n",
		"ClearAllForwardings yes\n",
		"ControlMaster no\n",
		"ControlPath none\n",
	} {
		if !strings.Contains(cfg, want) {
			t.Errorf("config lacks %q:\n%s", want, cfg)
		}
	}
	if strings.Contains(cfg, "StrictHostKeyChecking no") {
		t.Error("host key checking switched off")
	}
}

// TestRenderRefusesUnsafeValues: the remote user comes from devcontainer.json
// and is written into ssh_config here, so this is where it is checked.
func TestRenderRefusesUnsafeValues(t *testing.T) {
	p := testPaths(t)
	for _, bad := range []string{"dev\nProxyCommand evil", `dev"`, "dev\tx", ""} {
		if _, err := Render(workspace.State{Name: "demo", RemoteUser: bad}, "/usr/bin/castra", p); err == nil {
			t.Errorf("remote user %q was written", bad)
		}
	}
	cfg, err := Render(workspace.State{Name: "demo", RemoteUser: "dev user"}, "/usr/bin/castra", p)
	if err != nil || !strings.Contains(cfg, "User \"dev user\"\n") {
		t.Fatalf("a user with a space should be quoted: %v\n%s", err, cfg)
	}
	if _, err := Render(demo, "/opt/\"quoted\"/castra", p); err == nil {
		t.Error("a binary path with a double quote was written")
	}
}

func TestProxyCommandEscapesPercent(t *testing.T) {
	got, err := ProxyCommand("/home/a%b/castra", "demo")
	if err != nil || got != "/home/a%%b/castra proxy demo" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestPinHostKeyReplacesOldKey(t *testing.T) {
	p := testPaths(t)
	other := workspace.State{Name: "other", RemoteUser: "dev"}
	_, first, _ := NewKey("one")
	_, second, _ := NewKey("two")
	_, otherKey, _ := NewKey("other")
	if err := p.PinHostKey(other, otherKey); err != nil {
		t.Fatal(err)
	}
	if err := p.PinHostKey(demo, first); err != nil {
		t.Fatal(err)
	}
	if err := p.PinHostKey(demo, second); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p.PinFile("demo"))
	if n := strings.Count(string(b), "demo.castra "); n != 1 || strings.Contains(string(b), "other.castra") {
		t.Fatalf("demo's pin file:\n%s", b)
	}
	if !p.HasHostKey(demo) || !p.HasHostKey(other) || p.HasHostKey(workspace.State{Name: "missing"}) {
		t.Fatal("HasHostKey is wrong")
	}
}

func TestWriteHostAndRemoveHost(t *testing.T) {
	p := testPaths(t)
	kept := workspace.State{Name: "kept", RemoteUser: "dev"}
	_, key, _ := NewKey("k")
	for _, s := range []workspace.State{demo, kept} {
		if err := p.WriteHost(s, "/usr/bin/castra"); err != nil {
			t.Fatal(err)
		}
		if err := p.PinHostKey(s, key); err != nil {
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
	for _, f := range []string{p.HostFile("demo"), p.PinFile("demo")} {
		if _, err := os.Stat(f); err == nil {
			t.Fatalf("%s still exists", f)
		}
	}
	if _, err := os.Stat(p.HostFile("kept")); err != nil || !p.HasHostKey(kept) {
		t.Fatal("removing one workspace touched another")
	}
}

func TestMigrateSplitsTheSharedFiles(t *testing.T) {
	p := testPaths(t)
	if err := os.MkdirAll(p.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	_, a, _ := NewKey("a")
	_, b, _ := NewKey("b")
	shared := "demo.castra " + string(a.Type()) + " x\nkept.castra " + string(b.Type()) + " y\n"
	if err := os.WriteFile(filepath.Join(p.Dir, "known_hosts"), []byte(shared), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.Config, []byte("Host demo.castra\n  User dev\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	kept := workspace.State{Name: "kept", RemoteUser: "dev"}
	load := func() ([]workspace.State, error) { return []workspace.State{demo, kept}, nil }
	if err := p.Migrate(load, "/usr/bin/castra"); err != nil {
		t.Fatal(err)
	}
	for _, s := range []workspace.State{demo, kept} {
		pin, _ := os.ReadFile(p.PinFile(s.Name))
		if !strings.HasPrefix(string(pin), s.Alias()+" ") {
			t.Fatalf("%s's pin after the migration: %q", s.Name, pin)
		}
		if _, err := os.Stat(p.HostFile(s.Name)); err != nil {
			t.Fatalf("%s has no host file after the migration", s.Name)
		}
	}
	if _, err := os.Stat(filepath.Join(p.Dir, "known_hosts")); err == nil {
		t.Fatal("the shared known_hosts is still there")
	}
	cfg, _ := os.ReadFile(p.Config)
	if !strings.Contains(string(cfg), "Include ") || strings.Contains(string(cfg), "Host demo.castra") {
		t.Fatalf("castra's config after the migration:\n%s", cfg)
	}
	if err := p.Migrate(load, "/usr/bin/castra"); err != nil {
		t.Fatalf("a second migration should do nothing: %v", err)
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

func TestClientKeyIsCreatedOnce(t *testing.T) {
	p := testPaths(t)
	a, err := p.EnsureClientKey()
	if err != nil {
		t.Fatal(err)
	}
	b, err := p.EnsureClientKey()
	if err != nil || string(a) != string(b) {
		t.Fatal("second call made a new key")
	}
	if fi, _ := os.Stat(p.ClientKey); fi.Mode().Perm() != 0o600 {
		t.Fatalf("private key mode %v", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(filepath.Dir(p.ClientKey)); fi.Mode().Perm() != 0o700 {
		t.Fatalf("key dir mode %v", fi.Mode().Perm())
	}
}
