package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestDotfilesScript runs the command castra sends into the container under
// a local sh, with HOME in a temp dir and a bare repo on disk in place of the
// SSH remote. What it proves is the script's logic and its quoting: clone,
// installer order, skip, refresh, custom install, and the message when
// nothing installs.
func TestDotfilesScript(t *testing.T) {
	for _, tool := range []string{"sh", "git"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("no %s on this machine", tool)
		}
	}

	t.Run("clones and runs the first installer in order", func(t *testing.T) {
		repo := bareRepo(t, map[string]string{
			"setup":      "#!/bin/sh\necho setup-ran > \"$HOME/which\"\n",
			"install.sh": "#!/bin/sh\necho install.sh-ran > \"$HOME/which\"\n",
		})
		home := t.TempDir()
		out := runDotfiles(t, home, repo, "", false)
		if !strings.Contains(out, "castra: running dotfiles/install.sh") {
			t.Fatalf("output %q", out)
		}
		if got := read(t, home, "which"); got != "install.sh-ran\n" {
			t.Fatalf("installer that ran: %q, want install.sh (first in the documented order)", got)
		}
		if _, err := os.Stat(filepath.Join(home, "dotfiles", ".git")); err != nil {
			t.Fatal("no clone in ~/dotfiles")
		}
	})

	t.Run("second run skips, refresh pulls and reruns", func(t *testing.T) {
		repo := bareRepo(t, map[string]string{"install.sh": "#!/bin/sh\ndate +%s%N >> \"$HOME/runs\"\n"})
		home := t.TempDir()
		runDotfiles(t, home, repo, "", false)
		out := runDotfiles(t, home, repo, "", false)
		if !strings.Contains(out, "already in") || strings.Count(read(t, home, "runs"), "\n") != 1 {
			t.Fatalf("second run should skip; output %q, runs %q", out, read(t, home, "runs"))
		}
		out = runDotfiles(t, home, repo, "", true)
		if strings.Contains(out, "already in") || strings.Count(read(t, home, "runs"), "\n") != 2 {
			t.Fatalf("refresh should pull and rerun; output %q, runs %q", out, read(t, home, "runs"))
		}
	})

	t.Run("a custom install command replaces the search", func(t *testing.T) {
		repo := bareRepo(t, map[string]string{"install.sh": "#!/bin/sh\necho wrong > \"$HOME/which\"\n"})
		home := t.TempDir()
		runDotfiles(t, home, repo, "echo custom > \"$HOME/which\"", false)
		if got := read(t, home, "which"); got != "custom\n" {
			t.Fatalf("ran %q, want the custom command", got)
		}
	})

	t.Run("no installer is reported, not an error", func(t *testing.T) {
		repo := bareRepo(t, map[string]string{"README": "nothing to run\n"})
		out := runDotfiles(t, t.TempDir(), repo, "", false)
		if !strings.Contains(out, "castra: no install script in") {
			t.Fatalf("output %q", out)
		}
	})

	t.Run("a quote in the repo name survives quoting", func(t *testing.T) {
		repo := bareRepo(t, map[string]string{"install.sh": "#!/bin/sh\necho ok > \"$HOME/which\"\n"})
		quoted := filepath.Join(filepath.Dir(repo), "it's dotfiles.git")
		if err := os.Rename(repo, quoted); err != nil {
			t.Fatal(err)
		}
		home := t.TempDir()
		runDotfiles(t, home, quoted, "", false)
		if got := read(t, home, "which"); got != "ok\n" {
			t.Fatalf("install did not run from %q", quoted)
		}
	})
}

// runDotfiles executes the remote command line the way the container's login
// shell would: as one string handed to sh.
func runDotfiles(t *testing.T, home, repo, install string, refresh bool) string {
	t.Helper()
	cmd := exec.Command("sh", "-c", dotfilesCommand(repo, install, refresh))
	cmd.Env = append(os.Environ(), "HOME="+home, "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dotfiles command failed: %v\n%s", err, out)
	}
	return string(out)
}

// bareRepo builds a bare git repository holding files, as a dotfiles remote.
func bareRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	work := filepath.Join(t.TempDir(), "work")
	bare := filepath.Join(t.TempDir(), "dotfiles.git")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(work, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=castra", "GIT_AUTHOR_EMAIL=castra@example.invalid",
			"GIT_COMMITTER_NAME=castra", "GIT_COMMITTER_EMAIL=castra@example.invalid",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	git(work, "init", "-q", "-b", "main")
	git(work, "add", ".")
	git(work, "commit", "-q", "-m", "dotfiles")
	git(work, "clone", "-q", "--bare", work, bare)
	return bare
}

func read(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return ""
	}
	return string(b)
}
