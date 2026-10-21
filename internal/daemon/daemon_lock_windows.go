//go:build windows

package daemon

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// errInstanceLockHeld reports that another process already holds the daemon
// instance lock. acquireFileLock returns it (not the raw OS error) so callers
// can distinguish contention from a genuine failure in a portable way.
var errInstanceLockHeld = errors.New("doubletake instance lock held")

// acquireFileLock takes an exclusive, immediate (non-blocking) lock on the
// first byte of f using LockFileEx, the Windows analogue of flock(2). It
// returns errInstanceLockHeld if another process already holds the lock.
func acquireFileLock(f *os.File) error {
	ol := new(windows.Overlapped)
	err := windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, ol,
	)
	if err != nil {
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_IO_PENDING) {
			return errInstanceLockHeld
		}
		return err
	}
	return nil
}

// releaseFileLock drops the lock held on the first byte of f.
func releaseFileLock(f *os.File) error {
	ol := new(windows.Overlapped)
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, ol)
}
