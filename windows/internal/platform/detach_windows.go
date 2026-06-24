//go:build windows

package platform

import "golang.org/x/sys/windows"

var (
	kernel32        = windows.NewLazySystemDLL("kernel32.dll")
	procFreeConsole = kernel32.NewProc("FreeConsole")
)

// DetachConsole detaches this process from the current console, if any.
func DetachConsole() {
	_, _, _ = procFreeConsole.Call()
}
