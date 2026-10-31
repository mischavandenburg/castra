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

// checkError checks that an error names the file, contains the fragments
// castra chose, and avoids the library's JSON wording. It never compares the
// library's exact text, which changes between releases.
func checkError(t *testing.T, dir string, err error, fragments ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("want an error")
	}
	msg := err.Error()
	if prefix := filepath.Join(dir, "castra", "config.yaml") + ": "; !strings.HasPrefix(msg, prefix) {
		t.Fatalf("error = %q, want prefix %q", msg, prefix)
	}
	for _, fragment := range fragments {
		if !strings.Contains(msg, fragment) {
			t.Fatalf("error = %q, want fragment %q", msg, fragment)
		}
	}
	if strings.Contains(msg, "JSON") {
		t.Fatalf("error mentions JSON: %q", msg)
	}
}

// TestUnknownKeyIsAnError: a typo such as `dotfile:` must be reported with
// the file's path and the key's name, never silently ignored.
func TestUnknownKeyIsAnError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	write(t, dir, "dotfile:\n  repo: git@github.com:you/dotfiles.git\n")
	_, err := Load()
	checkError(t, dir, err, `unknown setting "dotfile"`)
}

// TestBrokenYAMLIsAnError: a syntax error names the file and the line,
// without the JSON conversion wording.
func TestBrokenYAMLIsAnError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	write(t, dir, "engine: docker\n  install: ./setup\n")
	_, err := Load()
	checkError(t, dir, err, "line 2")
}

// TestWrongTypeIsAnError: a value of the wrong type names the file and the
// setting, in castra's words rather than the decoder's.
func TestWrongTypeIsAnError(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
	}{
		{"array for engine", "engine: [a, b]\n", `wrong type for setting "engine" (got array)`},
		{"string for dotfiles", "dotfiles: x\n", `wrong type for setting "dotfiles" (got string)`},
		{"array for dotfiles.install", "dotfiles: {install: [a]}\n", `wrong type for setting "dotfiles.install" (got array)`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", dir)
			write(t, dir, tc.body)
			_, err := Load()
			checkError(t, dir, err, tc.want)
		})
	}
}

// TestRootListIsAnError: a list at the root has no setting name, so the error
// falls back to the library's text with the JSON wording stripped.
func TestRootListIsAnError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	write(t, dir, "- a\n")
	_, err := Load()
	checkError(t, dir, err, "array")
	if strings.Contains(err.Error(), "json:") {
		t.Fatalf("error keeps the JSON prefix: %q", err)
	}
}

// TestEmptyAndCommentOnlyFilesAreEmptyConfigs: a file with nothing to parse
// is not an error.
func TestEmptyAndCommentOnlyFilesAreEmptyConfigs(t *testing.T) {
	for _, tc := range []struct {
		name, body string
	}{
		{"empty", ""},
		{"comments only", "# only a comment\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", dir)
			write(t, dir, tc.body)
			c, err := Load()
			if err != nil || c != (Config{}) {
				t.Fatalf("Load with %q = %+v, %v", tc.body, c, err)
			}
		})
	}
}

// TestPathUsesAbsoluteXDGConfigHome: an absolute $XDG_CONFIG_HOME, even /,
// wins over $HOME.
func TestPathUsesAbsoluteXDGConfigHome(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, dir := range []string{t.TempDir(), "/"} {
		t.Setenv("XDG_CONFIG_HOME", dir)
		got, err := Path()
		if err != nil {
			t.Fatal(err)
		}
		if want := filepath.Join(dir, "castra", "config.yaml"); got != want {
			t.Fatalf("Path with XDG_CONFIG_HOME=%q = %q, want %q", dir, got, want)
		}
	}
}

// TestPathIgnoresRelativeXDGConfigHome: the XDG spec calls a relative value
// invalid, so it falls back to $HOME/.config like an unset variable.
func TestPathIgnoresRelativeXDGConfigHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, dir := range []string{"rel", "./rel", "."} {
		t.Setenv("XDG_CONFIG_HOME", dir)
		got, err := Path()
		if err != nil {
			t.Fatal(err)
		}
		if want := filepath.Join(home, ".config", "castra", "config.yaml"); got != want {
			t.Fatalf("Path with XDG_CONFIG_HOME=%q = %q, want %q", dir, got, want)
		}
	}
}

func TestPathWithoutXDGConfigHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	got, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".config", "castra", "config.yaml"); got != want {
		t.Fatalf("Path = %q, want %q", got, want)
	}
}

func TestPathRelativeXDGConfigHomeWithoutHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "rel")
	t.Setenv("HOME", "")
	if _, err := Path(); err == nil {
		t.Fatal("Path without HOME = nil error")
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
