// windows/internal/config/config.go
//
// Package config provides configuration loading for the application.
// This file implements loading a JSON config located next to the executable.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	CompanyName    = "Feralthedogg"
	ProductName    = "AppleMusicRPC"
	ConfigFileName = "config.json"
)

// Config describes all runtime settings loaded from config.json (or other loaders).
// JSON tags define the on-disk field names.
type Config struct {
	DiscordAppID  string   `json:"discord_app_id"` // Discord application/client ID used for Rich Presence.
	LargeImage    string   `json:"large_image"`    // Discord asset key for the large image when not using album art.
	LargeText     string   `json:"large_text"`     // Tooltip text for the large image.
	PreferSources []string `json:"prefer_sources"` // Preferred media session sources/providers, if multiple exist.
	PollMS        int      `json:"poll_ms"`        // Poll interval in milliseconds.

	UseAlbumArt bool `json:"use_album_art"` // If true, attempt to resolve and use album artwork.
	ShowPaused  bool `json:"show_paused"`   // If false, paused playback is treated as stopped (clears presence).

	ForceRefreshMS int `json:"force_refresh_ms"` // Periodic refresh interval in ms (0 means default/disabled override).

	Country string `json:"country"` // Optional. ISO 3166-1 alpha-2 (e.g. "KR"). Empty = auto-detect by external IP.
}

// Default returns the runtime defaults used before config.json values are applied.
func Default() Config {
	return Config{
		PollMS:         1200,
		LargeText:      "Apple Music",
		UseAlbumArt:    true,
		ShowPaused:     true,
		ForceRefreshMS: 0,
	}
}

// Sanitize fixes invalid or empty values that would cause degenerate runtime behavior.
func (cfg *Config) Sanitize() {
	if cfg.PollMS <= 0 {
		cfg.PollMS = 1200
	}
	if cfg.LargeText == "" {
		cfg.LargeText = "Apple Music"
	}
}

// LoadPath loads a JSON config from path on top of Default values.
func LoadPath(path string) (Config, error) {
	cfg := Default()

	b, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, err
	}
	cfg.Sanitize()
	return cfg, nil
}

// LoadNearExe loads config.json from the same directory as the running executable.
// It applies reasonable defaults first, then overwrites them with any JSON fields found,
// and finally validates/sanitizes select values.
func LoadNearExe() (Config, error) {
	// Resolve the executable path to locate config.json next to it.
	exe, err := os.Executable()
	if err != nil {
		return Default(), err
	}
	return LoadPath(filepath.Join(filepath.Dir(exe), ConfigFileName))
}

// executableConfigPaths returns portable and app-bundle config locations.
func executableConfigPaths() []string {
	exe, err := os.Executable()
	if err != nil {
		return nil
	}
	dir := filepath.Dir(exe)
	return []string{
		filepath.Join(dir, ConfigFileName),
		filepath.Clean(filepath.Join(dir, "..", "Resources", ConfigFileName)),
	}
}

// cwdConfigPaths helps local development and CI builds launched from the module directory.
func cwdConfigPaths() []string {
	wd, err := os.Getwd()
	if err != nil {
		return nil
	}
	return []string{
		filepath.Join(wd, ConfigFileName),
		filepath.Join(wd, "dist", ConfigFileName),
	}
}

func uniqueExistingOrder(paths []string) []string {
	out := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, p := range paths {
		p = filepath.Clean(strings.TrimSpace(p))
		if p == "." || p == "" {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out
}

// LoadAppConfig searches platform-specific install locations, app bundle resources,
// and portable/development locations for config.json.
func LoadAppConfig() (Config, error) {
	paths := uniqueExistingOrder(CandidateConfigPaths())
	for _, p := range paths {
		cfg, err := LoadPath(p)
		if err == nil {
			return cfg, nil
		}
		if !os.IsNotExist(err) {
			return cfg, fmt.Errorf("%s: %w", p, err)
		}
	}
	return Default(), fmt.Errorf("%s not found in: %s", ConfigFileName, strings.Join(paths, ", "))
}
