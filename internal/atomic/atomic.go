// Package atomic writes files so that a reader sees either the old content
// or the new, never half of either: the data goes to a temporary file in the
// same directory, is synced to disk, given its mode, and renamed into place.
package atomic

import (
	"os"
	"path/filepath"
)

// WriteFile replaces path's contents in one step. When path is a symlink (a
// dotfiles manager owns ~/.ssh/config, say), the target is replaced and the
// link is kept. The file ends up with exactly mode, whatever the umask.
func WriteFile(path string, data []byte, mode os.FileMode) (err error) {
	if target, err := filepath.EvalSymlinks(path); err == nil {
		path = target
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	if _, err = tmp.Write(data); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Chmod(mode); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	// Best effort: make the rename itself durable, so a power cut right after
	// leaves the new name, not a missing file.
	if d, derr := os.Open(dir); derr == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
