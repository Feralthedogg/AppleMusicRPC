//go:build !windows

package platform

// DetachConsole is a no-op on non-Windows platforms.
func DetachConsole() {}
