//go:build !windows && !darwin

package config

import (
	"os"
	"path/filepath"
)

// CandidateConfigPaths returns config search paths in priority order on Unix-like systems.
func CandidateConfigPaths() []string {
	paths := make([]string, 0, 6)

	if ud, err := os.UserConfigDir(); err == nil && ud != "" {
		paths = append(paths, filepath.Join(ud, CompanyName, ProductName, ConfigFileName))
	}

	paths = append(paths, executableConfigPaths()...)
	paths = append(paths, cwdConfigPaths()...)
	return paths
}
