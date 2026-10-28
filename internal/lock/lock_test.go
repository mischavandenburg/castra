package lock

import (
	"path/filepath"
	"testing"
	"time"
)

// TestSecondHolderWaits takes the lock twice on separate descriptors, as two
// castra processes would: the second Acquire must block until the first
// Release, and then succeed.
func TestSecondHolderWaits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "demo.lock")
	first, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}

	got := make(chan error, 1)
	go func() {
		second, err := Acquire(path)
		if err == nil {
			_ = second.Release()
		}
		got <- err
	}()

	select {
	case err := <-got:
		t.Fatalf("second holder got the lock while the first held it (err %v)", err)
	case <-time.After(200 * time.Millisecond):
	}

	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-got:
		if err != nil {
			t.Fatalf("second holder failed after the release: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second holder still waiting after the first released")
	}
}

func TestAcquireCreatesTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new.lock")
	l, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	if _, err := Acquire(filepath.Join(t.TempDir(), "missing-dir", "x.lock")); err == nil {
		t.Fatal("a lock in a missing directory should fail")
	}
}
