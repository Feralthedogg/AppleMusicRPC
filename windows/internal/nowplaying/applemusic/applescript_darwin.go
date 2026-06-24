//go:build darwin

package applemusic

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"amrpc/internal/nowplaying"
)

const sourceName = "Music"

const pollScript = `
set outputDelim to ASCII character 30
tell application "Music"
	set stateText to player state as string
	if stateText is "stopped" then
		return "STOPPED"
	end if

	set trackName to ""
	set artistName to ""
	set albumName to ""
	set posText to "0"
	set durText to "0"

	try
		set currentTrack to current track
		try
			set trackName to name of currentTrack as text
		end try
		try
			set artistName to artist of currentTrack as text
		end try
		try
			set albumName to album of currentTrack as text
		end try
		try
			set durText to (duration of currentTrack as real) as text
		end try
	end try

	try
		set posText to (player position as real) as text
	end try

	return stateText & outputDelim & trackName & outputDelim & artistName & outputDelim & albumName & outputDelim & posText & outputDelim & durText
end tell
`

type Provider struct {
	timeout time.Duration
}

func New(_ []string) (*Provider, bool) {
	return &Provider{timeout: 2 * time.Second}, true
}

func (p *Provider) Close() error {
	return nil
}

func (p *Provider) Poll() (nowplaying.Track, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), p.timeout)
	defer cancel()

	if err := exec.CommandContext(ctx, "/usr/bin/pgrep", "-x", "Music").Run(); err != nil {
		return nowplaying.Track{}, false, nil
	}

	out, err := exec.CommandContext(ctx, "/usr/bin/osascript", "-e", pollScript).Output()
	if err != nil {
		return nowplaying.Track{}, false, err
	}

	raw := strings.TrimRight(string(out), "\r\n")
	if raw == "" {
		return nowplaying.Track{}, false, nil
	}
	if raw == "STOPPED" {
		return nowplaying.Track{Source: sourceName, Status: nowplaying.Stopped}, true, nil
	}

	parts := strings.Split(raw, string(rune(30)))
	if len(parts) < 6 {
		return nowplaying.Track{}, false, nil
	}

	status := nowplaying.Stopped
	switch strings.ToLower(strings.TrimSpace(parts[0])) {
	case "playing":
		status = nowplaying.Playing
	case "paused":
		status = nowplaying.Paused
	}

	t := nowplaying.Track{
		Source:   sourceName,
		Status:   status,
		Title:    strings.TrimSpace(parts[1]),
		Artist:   strings.TrimSpace(parts[2]),
		Album:    strings.TrimSpace(parts[3]),
		Position: parseSeconds(parts[4]),
		Duration: parseSeconds(parts[5]),
	}
	if t.Title == "" && t.Artist == "" && t.Status != nowplaying.Stopped {
		return nowplaying.Track{}, false, nil
	}
	if t.Duration > 0 && t.Position > t.Duration {
		t.Position = t.Duration
	}
	return t, true, nil
}

func parseSeconds(s string) time.Duration {
	s = strings.TrimSpace(s)
	if s == "" || strings.EqualFold(s, "missing value") {
		return 0
	}
	s = strings.ReplaceAll(s, ",", ".")

	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v <= 0 {
		return 0
	}
	return time.Duration(v * float64(time.Second))
}
