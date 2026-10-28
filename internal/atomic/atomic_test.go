package atomic

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestWriteFileReplacesContentAndSetsMode(t *testing.T) {
	old := syscall.Umask(0o077) // the umask must not win over the mode asked for
	defer syscall.Umask(old)
	dir := t.TempDir()
	path := filepath.Join(dir, "known_hosts")
	if err := os.WriteFile(path, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(path, []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	fi, _ := os.Stat(path)
	if string(b) != "new\n" || fi.Mode().Perm() != 0o644 {
		t.Fatalf("content %q, mode %v", b, fi.Mode().Perm())
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("temp file left behind: %v", entries)
	}
}

// TestWriteFileKeepsSymlink: dotfiles managers such as stow and yadm make
// ~/.ssh/config a symlink; the target is written, the link stays.
func TestWriteFileKeepsSymlink(t *testing.T) {
	target := filepath.Join(t.TempDir(), "real")
	if err := os.WriteFile(target, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(link, []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the symlink was replaced: %v, %v", fi, err)
	}
	if b, _ := os.ReadFile(target); string(b) != "new\n" {
		t.Fatalf("target holds %q", b)
	}
}

func TestWriteFileInMissingDirFails(t *testing.T) {
	if err := WriteFile(filepath.Join(t.TempDir(), "missing", "f"), []byte("x"), 0o600); err == nil {
		t.Fatal("writing into a missing directory should fail")
	}
}
