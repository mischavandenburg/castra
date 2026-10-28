// Package lock serializes castra runs on one workspace with an advisory
// file lock (flock), so two `castra up` on the same folder never race. Linux
// and macOS both have flock; castra runs nowhere else.
package lock

import (
	"fmt"
	"os"
	"syscall"
)

// Lock is a held exclusive lock on a file.
type Lock struct {
	f *os.File
}

// Acquire opens path (creating it if needed) and waits for an exclusive lock
// on it. The lock is released by Release, or by the process ending.
func Acquire(path string) (*Lock, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return &Lock{f: f}, nil
}

// Release gives the lock up. Closing the descriptor releases it.
func (l *Lock) Release() error {
	return l.f.Close()
}
