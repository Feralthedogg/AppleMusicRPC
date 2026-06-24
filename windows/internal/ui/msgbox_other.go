//go:build !windows && !darwin

package ui

import (
	"fmt"
	"os"
)

func Info(title, text string) {
	_, _ = fmt.Fprintf(os.Stderr, "%s: %s\n", title, text)
}

func Error(title, text string) {
	_, _ = fmt.Fprintf(os.Stderr, "%s: %s\n", title, text)
}
