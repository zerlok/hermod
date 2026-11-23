package relay

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// lockPollInterval is how often a lease waits again for a relay whose last
// holder is settling it.
const lockPollInterval = 100 * time.Millisecond

// lock is one lease's lock on its relay's lock file. Each lease opens the file
// on its own, and flock treats each open file as a holder of its own, so two
// leases in one process count as two; the kernel drops the lock of a process
// that dies, so a killed process never keeps a relay leased.
type lock struct {
	f *os.File
}

// takeLock takes a lease's lock on dir/name: a shared one, or for a relay
// with a single reader an exclusive one, refused with ErrInUse while another
// lease holds it. While the relay's last holder settles it, which may remove
// it, a shared lock waits, so a relay is never connected under a settle; ctx
// ends the wait.
func takeLock(ctx context.Context, dir, name string, exclusive bool) (*lock, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("lease %s: %w", name, err)
	}
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("lease %s: %w", name, err)
	}
	how := syscall.LOCK_SH
	if exclusive {
		how = syscall.LOCK_EX
	}
	for {
		err := syscall.Flock(int(f.Fd()), how|syscall.LOCK_NB)
		if err == nil {
			return &lock{f: f}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			_ = f.Close()
			return nil, fmt.Errorf("lease %s: %w", name, err)
		}
		if exclusive {
			_ = f.Close()
			return nil, fmt.Errorf("lease %s: %w", name, ErrInUse)
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, fmt.Errorf("lease %s: %w", name, ctx.Err())
		case <-time.After(lockPollInterval):
		}
	}
}

// last reports whether this was the relay's last lease, which then holds the
// relay alone until close, to settle it. The exclusive lock is tried without
// waiting: it is granted only when no other lease holds the file. A lease that
// is not the last drops its lock all the same, so of leases given up at once
// the last one to try gets it.
func (l *lock) last() (bool, error) {
	err := syscall.Flock(int(l.f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, syscall.EWOULDBLOCK):
		// Linux drops the shared lock on a failed conversion; not every system
		// does, and a lock kept here would keep the true last holder from
		// knowing it is the last.
		_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
		return false, nil
	default:
		return false, fmt.Errorf("give up lease %s: %w", filepath.Base(l.f.Name()), err)
	}
}

// close drops whatever lock the lease still holds. The lock file stays:
// removing it could part a process that has it open from one that opens it
// anew.
func (l *lock) close() error { return l.f.Close() }

// leased reports whether any lease holds dir/name, without taking one: the
// exclusive lock is tried and, when granted, dropped at once.
//
// A holder giving its lease up converts its lock, which Linux does by dropping
// it first; a read in that instant takes the relay for unleased, and the holder
// then for not the last, so it skips the settle and leaves the relay as a
// holder that died would: an FS relay's Mutagen session is neither paused nor
// terminated and keeps syncing with no lease on it, which List reads as
// Disconnected, until the next lease taken on it is given up. Reading
// /proc/locks or OFD locks would avoid the collision but exist on Linux alone,
// and Hermod also runs on macOS.
func leased(dir, name string) (bool, error) {
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_RDWR, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("lease %s: %w", name, err)
	}
	defer f.Close()
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	switch {
	case err == nil:
		return false, nil
	case errors.Is(err, syscall.EWOULDBLOCK):
		return true, nil
	default:
		return false, fmt.Errorf("lease %s: %w", name, err)
	}
}
