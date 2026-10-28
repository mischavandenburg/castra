package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "castra"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "castra", "config.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadSendEnv(t *testing.T) {
	writeConfig(t, "dotfiles:\n  repo: git@example.com:me/dots\nsendEnv:\n  - GITHUB_TOKEN\n  - LC_*\n")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Dotfiles.Repo != "git@example.com:me/dots" || strings.Join(c.SendEnv, ",") != "GITHUB_TOKEN,LC_*" {
		t.Fatalf("loaded %+v", c)
	}
}

func TestRejectsBadSendEnvName(t *testing.T) {
	for _, bad := range []string{"GITHUB TOKEN", "A=B", "1ABC", "*", "X;rm"} {
		writeConfig(t, "sendEnv:\n  - \""+bad+"\"\n")
		if _, err := Load(); err == nil {
			t.Errorf("accepted sendEnv %q", bad)
		}
	}
}

func TestRejectsUnknownKey(t *testing.T) {
	writeConfig(t, "sendEnvs:\n  - GITHUB_TOKEN\n") // a typo must not pass silently
	if _, err := Load(); err == nil {
		t.Fatal("a misspelled key was accepted silently")
	}
}

func TestMissingFileIsEmptyConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	c, err := Load()
	if err != nil || c.Dotfiles.Repo != "" || len(c.SendEnv) != 0 {
		t.Fatalf("%+v, %v", c, err)
	}
}
