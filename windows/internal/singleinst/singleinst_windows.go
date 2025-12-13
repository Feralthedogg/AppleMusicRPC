//go:build windows

// internal/singleinst/singleinst_windows.go
//
// Windows-only single-instance lock using a named mutex.
//
// IMPORTANT:
// CreateMutex can return a valid handle AND an error value of ERROR_ALREADY_EXISTS.
// That is not a fatal error; it means another instance already created the mutex.
// We must treat that case as "already running" and still return the handle.

package singleinst

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// Lock represents an acquired instance lock backed by a Windows mutex handle.
type Lock struct {
	h windows.Handle
}

// Release closes the underlying mutex handle.
// It is safe to call Release multiple times.
func (l *Lock) Release() {
	if l != nil && l.h != 0 {
		_ = windows.CloseHandle(l.h)
		l.h = 0
	}
}

// Acquire creates (or opens) a named mutex and returns:
// - a Lock that should be released by the caller
// - already=true if another instance already created the mutex
// - error if the mutex could not be created/opened for reasons other than "already exists"
//
// The mutex name is prefixed with "Local\" so it is scoped to the current logon session.
func Acquire(name string) (*Lock, bool, error) {
	n := "Local\\" + name

	h, err := windows.CreateMutex(nil, false, windows.StringToUTF16Ptr(n))
	if err != nil && err != windows.ERROR_ALREADY_EXISTS {
		return nil, false, fmt.Errorf("CreateMutexW: %w", err)
	}

	already := (err == windows.ERROR_ALREADY_EXISTS)
	return &Lock{h: h}, already, nil
}
