// internal/nowplaying/nowplaying.go
//
// Package nowplaying defines the common model and interface used to retrieve
// "now playing" metadata from different backends (e.g., Windows SMTC).
package nowplaying

import "time"

// Status represents the playback state of the current media session.
type Status int

const (
	// Stopped indicates there is no active session, or playback should be treated as stopped.
	Stopped Status = iota

	// Playing indicates media is actively playing.
	Playing

	// Paused indicates media is paused but an active session exists.
	Paused
)

// Track describes the currently playing item and its playback position.
// Position and Duration use time.Duration for precise progress reporting.
type Track struct {
	Source   string        // Provider/backend identifier (useful when multiple sessions exist).
	Status   Status        // Playback status (Stopped/Playing/Paused).
	Title    string        // Track title.
	Artist   string        // Track artist.
	Album    string        // Track album/collection name.
	Position time.Duration // Current playback position.
	Duration time.Duration // Total duration (0 if unknown).
}

// Provider is a backend that can poll the currently playing track.
// Poll returns:
// - Track: the current track metadata (may be zero-value if ok==false)
// - ok: whether a media session was available
// - error: fatal/temporary errors encountered during polling
type Provider interface {
	Poll() (Track, bool, error)
	Close() error
}
