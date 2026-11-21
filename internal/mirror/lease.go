package mirror

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// leasePollInterval is how often a lease waits again for a mirror whose last
// holder is settling it.
const leasePollInterval = 100 * time.Millisecond

// Lease is one process's share in a mirror: a shared flock on a file named
// after the mirror. Every process using the mirror holds one, so the last to
// leave can tell it is the last and settle the mirror; the kernel drops the
// lock of a process that dies, so a killed process never keeps a mirror
// leased.
type Lease struct {
	f *os.File
}

// TakeLease takes a share in the mirror named name, the lock file being
// dir/name. While the mirror's last holder settles it, which may close it, the
// lease waits, so a mirror is never opened under a settle; ctx ends the wait.
func TakeLease(ctx context.Context, dir, name string) (*Lease, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("lease %s: %w", name, err)
	}
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("lease %s: %w", name, err)
	}
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB)
		if err == nil {
			return &Lease{f: f}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			_ = f.Close()
			return nil, fmt.Errorf("lease %s: %w", name, err)
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, fmt.Errorf("lease %s: %w", name, ctx.Err())
		case <-time.After(leasePollInterval):
		}
	}
}

// Release gives the share up and reports whether this process was the last
// holder, which then holds the mirror alone until Close, to settle it. The
// exclusive lock is tried without waiting: it is granted only when no other
// process holds a share. A holder that is not the last gives its share up all
// the same, so of holders leaving at once the last one to try gets it.
func (l *Lease) Release() (last bool, err error) {
	err = syscall.Flock(int(l.f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, syscall.EWOULDBLOCK):
		// Linux drops the shared lock on a failed conversion; not every system
		// does, and a share kept here would keep the true last holder from
		// knowing it is the last.
		_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
		return false, nil
	default:
		return false, fmt.Errorf("release lease %s: %w", filepath.Base(l.f.Name()), err)
	}
}

// Close drops whatever lock the lease still holds. The lock file stays: removing
// it could part a process that has it open from one that opens it anew.
func (l *Lease) Close() error { return l.f.Close() }
