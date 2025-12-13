//go:build windows

// internal/ui/msgbox_windows.go
//
// Windows-only UI helpers.
//
// This file provides minimal message box helpers using the Win32 MessageBoxW API.
// It is used for simple informational/error dialogs when configuration or startup
// fails and a GUI notification is preferred over console output.
package ui

import (
	"syscall"
	"unsafe"
)

var (
	// user32 provides access to Win32 UI APIs.
	user32 = syscall.NewLazyDLL("user32.dll")

	// MessageBoxW is the wide-character (UTF-16) version of MessageBox.
	procMessageBox = user32.NewProc("MessageBoxW")
)

const (
	// MessageBoxW style flags.
	mbOK        = 0x00000000
	mbIconInfo  = 0x00000040
	mbIconError = 0x00000010
)

// Info shows an informational message box with an OK button.
func Info(title, text string) {
	messageBox(title, text, mbOK|mbIconInfo)
}

// Error shows an error message box with an OK button.
func Error(title, text string) {
	messageBox(title, text, mbOK|mbIconError)
}

// messageBox displays a Win32 message box using UTF-16 strings.
// flags is a combination of MessageBox style constants (button set, icon, etc.).
func messageBox(title, text string, flags uintptr) {
	tPtr, _ := syscall.UTF16PtrFromString(title)
	mPtr, _ := syscall.UTF16PtrFromString(text)

	// HWND = 0 (no owner), then message text, title, and style flags.
	_, _, _ = procMessageBox.Call(
		0,
		uintptr(unsafe.Pointer(mPtr)),
		uintptr(unsafe.Pointer(tPtr)),
		flags,
	)
}
