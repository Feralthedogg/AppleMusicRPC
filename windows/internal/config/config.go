// windows/internal/config/config.go
//
// Package config provides configuration loading for the application.
// This file implements loading a JSON config located next to the executable.
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
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

// LoadNearExe loads config.json from the same directory as the running executable.
// It applies reasonable defaults first, then overwrites them with any JSON fields found,
// and finally validates/sanitizes select values.
func LoadNearExe() (Config, error) {
	// Defaults (used if fields are missing in the JSON or invalid after load).
	cfg := Config{
		PollMS:         1200,
		LargeText:      "Apple Music",
		UseAlbumArt:    true,
		ShowPaused:     true,
		ForceRefreshMS: 0,
	}

	// Resolve the executable path to locate config.json next to it.
	exe, err := os.Executable()
	if err != nil {
		return cfg, err
	}
	p := filepath.Join(filepath.Dir(exe), "config.json")

	// Read and parse JSON config.
	b, err := os.ReadFile(p)
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, err
	}

	// Sanitize critical fields to avoid degenerate runtime behavior.
	if cfg.PollMS <= 0 {
		cfg.PollMS = 1200
	}
	if cfg.LargeText == "" {
		cfg.LargeText = "Apple Music"
	}
	return cfg, nil
}
