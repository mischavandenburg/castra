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

func TestRenderBlock(t *testing.T) {
	p := testPaths(t)
	cfg := Render([]workspace.State{{Name: "demo", RemoteUser: "dev"}}, "/opt/my tools/castra", p)
	for _, want := range []string{
		"Host demo.castra\n",
		"HostKeyAlias demo.castra\n",
		"User dev\n",
		"ProxyCommand '/opt/my tools/castra' proxy demo\n",
		"StrictHostKeyChecking yes\n",
		"ForwardAgent yes\n",
		"IdentitiesOnly yes\n",
		"UserKnownHostsFile " + p.KnownHosts + "\n",
	} {
		if !strings.Contains(cfg, want) {
			t.Errorf("config lacks %q:\n%s", want, cfg)
		}
	}
	if strings.Contains(cfg, "StrictHostKeyChecking no") {
		t.Error("host key checking switched off")
	}
}

func TestProxyCommandEscapesPercent(t *testing.T) {
	if got := ProxyCommand("/home/a%b/castra", "demo"); got != "/home/a%%b/castra proxy demo" {
		t.Fatalf("got %q", got)
	}
}

func TestPinHostKeyReplacesOldKey(t *testing.T) {
	p := testPaths(t)
	_, first, _ := NewKey("one")
	_, second, _ := NewKey("two")
	_, other, _ := NewKey("other")
	if err := p.PinHostKey("other.castra", other); err != nil {
		t.Fatal(err)
	}
	if err := p.PinHostKey("demo.castra", first); err != nil {
		t.Fatal(err)
	}
	if err := p.PinHostKey("demo.castra", second); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p.KnownHosts)
	if n := strings.Count(string(b), "demo.castra "); n != 1 {
		t.Fatalf("%d lines for demo.castra:\n%s", n, b)
	}
	if !strings.Contains(string(b), "other.castra ") {
		t.Fatal("pinning one workspace dropped another")
	}
	if !p.HasHostKey("demo.castra") || p.HasHostKey("missing.castra") {
		t.Fatal("HasHostKey is wrong")
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
