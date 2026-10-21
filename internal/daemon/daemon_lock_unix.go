//go:build !windows

package daemon

import (
	"errors"
	"os"
	"syscall"
)

// errInstanceLockHeld reports that another process already holds the daemon
// instance lock. acquireFileLock returns it (not the raw OS error) so callers
// can distinguish contention from a genuine failure in a portable way.
var errInstanceLockHeld = errors.New("doubletake instance lock held")

// acquireFileLock takes an exclusive, non-blocking advisory lock on f using
// flock(2). It returns errInstanceLockHeld if another process holds the lock.
func acquireFileLock(f *os.File) error {
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return errInstanceLockHeld
		}
		return err
	}
	return nil
}

// releaseFileLock drops the advisory lock held on f.
func releaseFileLock(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
