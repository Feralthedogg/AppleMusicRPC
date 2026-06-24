//go:build darwin

package ui

import (
	"os/exec"
	"strings"
)

func Info(title, text string) {
	messageBox(title, text, "note")
}

func Error(title, text string) {
	messageBox(title, text, "stop")
}

func messageBox(title, text, icon string) {
	script := "display dialog " + appleScriptString(text) +
		" with title " + appleScriptString(title) +
		" buttons {\"OK\"} default button \"OK\" with icon " + icon
	_ = exec.Command("/usr/bin/osascript", "-e", script).Run()
}

func appleScriptString(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.ReplaceAll(s, "\n", "\\n")
	return "\"" + s + "\""
}
