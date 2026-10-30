package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigShowFullFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	writeConfig(t, dir, "engine: docker\ndotfiles:\n  repo: git@github.com:you/dotfiles.git\n  install: ./setup\n")

	out, err := runConfig(t, "show")
	if err != nil {
		t.Fatal(err)
	}
	want := `engine: docker
dotfiles.repo: git@github.com:you/dotfiles.git
dotfiles.install: ./setup
`
	if out != want {
		t.Fatalf("config show =\n%s\nwant\n%s", out, want)
	}
}

func TestConfigShowWithoutFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	out, err := runConfig(t, "show")
	if err != nil {
		t.Fatal(err)
	}
	want := `engine: not set (podman if installed, otherwise docker)
dotfiles.repo: not set (castra installs no dotfiles)
dotfiles.install: not set (castra runs the first install script it finds in the repository)
`
	if out != want {
		t.Fatalf("config show =\n%s\nwant\n%s", out, want)
	}
}

func TestConfigShowUnknownKey(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	writeConfig(t, dir, "dotfile:\n  repo: git@github.com:you/dotfiles.git\n")

	out, err := runConfig(t, "show")
	if err == nil {
		t.Fatalf("config show accepted an unknown key:\n%s", out)
	}
	if !strings.Contains(err.Error(), filepath.Join(dir, "castra", "config.yaml")) {
		t.Fatalf("error does not name the file: %v", err)
	}
}

func TestConfigShowBadYAML(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	writeConfig(t, dir, "engine: [docker\n")

	out, err := runConfig(t, "show")
	if err == nil {
		t.Fatalf("config show accepted bad YAML:\n%s", out)
	}
	if !strings.Contains(err.Error(), filepath.Join(dir, "castra", "config.yaml")) {
		t.Fatalf("error does not name the file: %v", err)
	}
}

func TestConfigShowQuotesControlCharacters(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	writeConfig(t, dir, "dotfiles:\n  install: \"echo one\\necho two\"\n")

	out, err := runConfig(t, "show")
	if err != nil {
		t.Fatal(err)
	}
	want := `engine: not set (podman if installed, otherwise docker)
dotfiles.repo: not set (castra installs no dotfiles)
dotfiles.install: "echo one\necho two"
`
	if out != want {
		t.Fatalf("config show =\n%s\nwant\n%s", out, want)
	}
}

func TestConfigPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	out, err := runConfig(t, "path")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "castra", "config.yaml") + "\n"; out != want {
		t.Fatalf("config path = %q, want %q", out, want)
	}
}

func TestConfigUnknownSubcommand(t *testing.T) {
	out, err := runConfig(t, "shwo")
	if err == nil {
		t.Fatalf("config accepted an unknown subcommand:\n%s", out)
	}
	if !strings.Contains(err.Error(), `unknown command "shwo"`) {
		t.Fatalf("error does not name the unknown command: %v", err)
	}
}

func TestConfigWithoutSubcommandPrintsHelp(t *testing.T) {
	out, err := runConfig(t)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Usage:") || !strings.Contains(out, "castra config [command]") {
		t.Fatalf("config did not print its help:\n%s", out)
	}
}

func runConfig(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newRootCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs(append([]string{"config"}, args...))
	err := cmd.Execute()
	if stderr.Len() != 0 {
		t.Fatalf("command wrote to stderr:\n%s", stderr.String())
	}
	return stdout.String(), err
}

func writeConfig(t *testing.T, xdg, body string) {
	t.Helper()
	p := filepath.Join(xdg, "castra", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
