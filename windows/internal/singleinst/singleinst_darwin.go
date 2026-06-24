//go:build darwin

package singleinst

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

type Lock struct {
	f *os.File
}

func (l *Lock) Release() {
	if l == nil || l.f == nil {
		return
	}
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	_ = l.f.Close()
	l.f = nil
}

func Acquire(name string) (*Lock, bool, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return nil, false, fmt.Errorf("resolve user config dir: %w", err)
	}
	if base == "" {
		return nil, false, errors.New("resolve user config dir: empty path")
	}

	dir := filepath.Join(base, "Feralthedogg", "AppleMusicRPC")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, false, err
	}

	fileName := strings.NewReplacer("/", "_", "\\", "_", ":", "_").Replace(name) + ".lock"
	f, err := os.OpenFile(filepath.Join(dir, fileName), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if err == syscall.EWOULDBLOCK {
			return nil, true, nil
		}
		return nil, false, err
	}

	return &Lock{f: f}, false, nil
}
