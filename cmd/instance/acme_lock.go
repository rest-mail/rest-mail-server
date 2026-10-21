package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// lockACMEIssuer serializes instance issuers sharing a certs volume, including
// different processes or containers. Close releases the advisory lock, as does
// process exit. Keep the empty file: unlinking it could let a third issuer lock
// a different inode while a second issuer still has the old inode open.
// The volume's filesystem must support flock; errors fail closed.
func lockACMEIssuer(dir string) (*os.File, error) {
	lock, err := os.OpenFile(filepath.Join(dir, ".acme-issue.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open ACME issuer lock: %w", err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lock.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("another ACME issuer is using the certs volume; retry after it finishes: %w", err)
		}
		return nil, fmt.Errorf("lock ACME issuer: %w", err)
	}
	return lock, nil
}
