package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMissingFileIsEmptyConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	c, err := Load()
	if err != nil || c != (Config{}) {
		t.Fatalf("Load with no file = %+v, %v", c, err)
	}
}

func TestLoadReadsEveryField(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	write(t, dir, "engine: docker\ndotfiles:\n  repo: git@github.com:you/dotfiles.git\n  install: ./setup\n")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Engine != "docker" || c.Dotfiles.Repo != "git@github.com:you/dotfiles.git" || c.Dotfiles.Install != "./setup" {
		t.Fatalf("Load = %+v", c)
	}
}

// TestUnknownKeyIsAnError: a typo such as `dotfile:` must be reported with
// the file's path, never silently ignored.
func TestUnknownKeyIsAnError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	write(t, dir, "dotfile:\n  repo: git@github.com:you/dotfiles.git\n")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), filepath.Join(dir, "castra", "config.yaml")) {
		t.Fatalf("want an error naming the file, got %v", err)
	}
}

func write(t *testing.T, xdg, body string) {
	t.Helper()
	p := filepath.Join(xdg, "castra", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
