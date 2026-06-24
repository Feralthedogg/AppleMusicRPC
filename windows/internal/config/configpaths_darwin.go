//go:build darwin

package config

import (
	"os"
	"path/filepath"
)

// CandidateConfigPaths returns config search paths in priority order on macOS.
func CandidateConfigPaths() []string {
	paths := make([]string, 0, 8)

	if ud, err := os.UserConfigDir(); err == nil && ud != "" {
		paths = append(paths, filepath.Join(ud, CompanyName, ProductName, ConfigFileName))
		paths = append(paths, filepath.Join(ud, ProductName, ConfigFileName))
	}

	paths = append(paths, filepath.Join("/Library", "Application Support", CompanyName, ProductName, ConfigFileName))
	paths = append(paths, executableConfigPaths()...)
	paths = append(paths, cwdConfigPaths()...)
	return paths
}
