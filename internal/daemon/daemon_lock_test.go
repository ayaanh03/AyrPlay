package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// The instance lock is per open file, so two opens in one process contend
// exactly like two daemon processes would, on both flock(2) and LockFileEx.
func TestFileLockReportsContentionPortably(t *testing.T) {
	path := filepath.Join(t.TempDir(), "doubletake.lock")
	open := func() *os.File {
		t.Helper()
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { f.Close() })
		return f
	}
	first, second := open(), open()

	if err := acquireFileLock(first); err != nil {
		t.Fatalf("first lock: %v", err)
	}
	if err := acquireFileLock(second); !errors.Is(err, errInstanceLockHeld) {
		t.Fatalf("second lock = %v, want errInstanceLockHeld", err)
	}
	if err := releaseFileLock(first); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := acquireFileLock(second); err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	if err := releaseFileLock(second); err != nil {
		t.Fatalf("release second: %v", err)
	}
}
