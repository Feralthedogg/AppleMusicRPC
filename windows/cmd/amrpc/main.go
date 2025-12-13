//go:build windows

package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"amrpc/internal/artwork"
	"amrpc/internal/config"
	drpc "amrpc/internal/discordrpc"
	"amrpc/internal/nowplaying"
	"amrpc/internal/nowplaying/smtcwinrt"
	"amrpc/internal/singleinst"
	"amrpc/internal/ui"

	"github.com/getlantern/systray"
	"golang.org/x/sys/windows"
)

var (
	kernel32        = windows.NewLazySystemDLL("kernel32.dll")
	procFreeConsole = kernel32.NewProc("FreeConsole")
)

// detachConsole detaches this process from the current console (if any).
// This makes the app behave like a background/tray app even when launched from a terminal.
// Note: If the process has no console, FreeConsole simply fails; we ignore it.
func detachConsole() {
	_, _, _ = procFreeConsole.Call()
}

// normalizeText collapses all whitespace (including newlines) into single spaces.
// This prevents false "trackChanged" when providers emit "Bon \nJovi" or similar.
func normalizeText(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	return strings.Join(strings.Fields(s), " ")
}

// cleanArtist removes rare "Artist — Album" leakage and normalizes whitespace.
func cleanArtist(artist string) string {
	artist = normalizeText(artist)
	if strings.Contains(artist, " — ") {
		parts := strings.SplitN(artist, " — ", 2)
		return strings.TrimSpace(parts[0])
	}
	return artist
}

// sameTrack determines whether two tracks represent the same "identity" for presence.
// Album is intentionally ignored (the UI removed album usage).
func sameTrack(a, b nowplaying.Track) bool {
	return a.Title == b.Title && a.Artist == b.Artist && a.Source == b.Source
}

func absDur(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// clampDur clamps v to [lo, hi]. If hi <= 0, hi is treated as "no upper bound".
func clampDur(v, lo, hi time.Duration) time.Duration {
	if v < lo {
		return lo
	}
	if hi > 0 && v > hi {
		return hi
	}
	return v
}

func main() {
	// Detach from console so we don't keep a visible terminal session.
	detachConsole()

	// --- Single instance lock (critical) ---
	// Keep the returned handle alive for the entire process lifetime.
	lock, already, err := singleinst.Acquire("amrpc.applemusic")
	if err != nil {
		ui.Error("Apple Music RPC", "Failed to acquire single-instance lock:\n"+err.Error())
		return
	}
	defer lock.Release()

	// If another instance is running, show a message and exit.
	if already {
		ui.Info("Apple Music RPC", "It's already running.")
		return
	}
	// --------------------------------------

	// Load configuration (DPAPI-encrypted config.enc preferred; fallback to config.json on first run).
	cfg, err := config.LoadNearExeSecure()
	if err != nil {
		ui.Error("Apple Music RPC", "Failed to load configuration file (config.enc / config.json):\n"+err.Error())
		return
	}
	if cfg.DiscordAppID == "" {
		ui.Error("Apple Music RPC", "DiscordAppID is empty.")
		return
	}

	// Determine storefront country for iTunes Search API.
	country := strings.ToUpper(strings.TrimSpace(cfg.Country))
	if country == "" {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		cc, err := config.DetectCountryISO(ctx)
		cancel()
		if err != nil || cc == "" {
			cc = "KR"
		}
		country = cc
	}

	// Connect to Discord IPC.
	rpc, err := drpc.Login(cfg.DiscordAppID, cfg.LargeImage, cfg.LargeText)
	if err != nil {
		ui.Error("Apple Music RPC", "Discord login failed:\n"+err.Error())
		return
	}

	// Initialize SMTC provider (WinRT).
	prov, ok := smtcwinrt.New(cfg.PreferSources)
	if !ok {
		ui.Error("Apple Music RPC", "SMTC WinRT provider init failed.")
		return
	}

	// Optional iTunes lookup used for cover art + track URL + duration.
	var it *artwork.ITunes
	if cfg.UseAlbumArt {
		it = artwork.NewITunes(country)
	}

	// Optional forced refresh interval (0 disables).
	var forceRefresh time.Duration
	if cfg.ForceRefreshMS > 0 {
		forceRefresh = time.Duration(cfg.ForceRefreshMS) * time.Millisecond
	}

	quit := make(chan struct{})

	// Poll loop runs in the background. systray.Run() keeps the main thread alive.
	go func() {
		pollTicker := time.NewTicker(time.Duration(cfg.PollMS) * time.Millisecond)
		defer pollTicker.Stop()

		const (
			// Apple Music / SMTC can briefly return "no session" while seeking or switching tracks.
			// During this grace period, we keep the previous presence and avoid anchor resets.
			noSessionGrace = 12 * time.Second

			// Seek detection thresholds:
			// - A big jump triggers a seek immediately.
			// - A small jump requires repeated confirmation to avoid false positives.
			seekBigJump      = 4 * time.Second
			seekSmallJump    = 1500 * time.Millisecond
			seekConfirmCount = 2

			// Ignore seek detection near the beginning, where metadata/position may still settle.
			ignoreSeekBefore = 2 * time.Second

			// Apple Music sometimes reports rawPos==0 transiently even while playing.
			// We consider rawPos invalid when it suddenly becomes 0 shortly after we were past 5s.
			zeroGlitchMinPrevPos = 5 * time.Second
			zeroGlitchMaxAge     = 6 * time.Second

			// Avoid spamming Discord IPC while scrubbing.
			updateThrottle = 250 * time.Millisecond
		)

		var last nowplaying.Track
		var lastStatus nowplaying.Status = nowplaying.Stopped

		// Anchor definition:
		// We store a unix-seconds "start time" and compute current playback position from it.
		// This creates stable timestamps for Discord (avoids jitter).
		var haveAnchor bool
		var anchorStartUnix int64

		// Last good provider position and its timestamp.
		// Used to predict expected position and filter glitches/jitter.
		var lastGoodPos time.Duration
		var lastGoodAt time.Time

		// If we anchored a new track when rawPos==0, allow a single later correction once rawPos stabilizes.
		var pendingAnchorFix bool

		// Small seek confirmation streak counter.
		var seekStreak int

		// Bookkeeping for throttling and for iTunes duration fallback.
		var lastUpdated time.Time
		var lastMeta artwork.Meta
		var lastSentDur time.Duration

		// No-session debounce timer.
		var noSessionSince time.Time

		clearAll := func() {
			rpc.Clear()

			last = nowplaying.Track{}
			lastStatus = nowplaying.Stopped

			haveAnchor = false
			anchorStartUnix = 0

			lastGoodPos = 0
			lastGoodAt = time.Time{}

			pendingAnchorFix = false
			seekStreak = 0

			lastUpdated = time.Time{}
			lastMeta = artwork.Meta{}
			lastSentDur = 0

			noSessionSince = time.Time{}
		}

		// anchoredPosNow computes the current position based on the anchorStartUnix.
		// The anchor uses second precision intentionally (Discord timestamps are unix seconds anyway).
		anchoredPosNow := func(now time.Time, dur time.Duration) time.Duration {
			if !haveAnchor || anchorStartUnix <= 0 {
				return 0
			}
			sec := now.Unix() - anchorStartUnix
			if sec < 0 {
				sec = 0
			}
			return clampDur(time.Duration(sec)*time.Second, 0, dur)
		}

		// reAnchor adjusts anchorStartUnix so that the anchored position matches rawPos at "now".
		reAnchor := func(now time.Time, rawPos time.Duration) {
			anchorStartUnix = now.Add(-rawPos).Unix()
			haveAnchor = true
		}

		for {
			select {
			case <-quit:
				clearAll()
				if c, ok := any(rpc).(interface{ Close() }); ok {
					c.Close()
				}
				_ = prov.Close()
				return

			case <-pollTicker.C:
				now := time.Now()

				t, ok, err := prov.Poll()
				if err != nil {
					// Treat provider errors as transient: keep presence briefly.
					if noSessionSince.IsZero() {
						noSessionSince = now
					}
					continue
				}

				if !ok {
					// Debounce "no session": keep presence for a grace period.
					if noSessionSince.IsZero() {
						noSessionSince = now
					}
					if now.Sub(noSessionSince) <= noSessionGrace {
						continue
					}
					clearAll()
					continue
				}

				// Session is available again.
				noSessionSince = time.Time{}

				// Normalize fields to reduce false track changes.
				tClean := t
				tClean.Title = normalizeText(tClean.Title)
				tClean.Artist = cleanArtist(tClean.Artist)
				tClean.Album = "" // album intentionally removed

				// If metadata is empty, treat as unusable session (debounce via noSessionSince).
				if tClean.Title == "" && tClean.Artist == "" {
					if noSessionSince.IsZero() {
						noSessionSince = now
					}
					continue
				}

				// Apply user policy: if ShowPaused=false, treat Paused as Stopped.
				effectiveStatus := tClean.Status
				if !cfg.ShowPaused && tClean.Status == nowplaying.Paused {
					effectiveStatus = nowplaying.Stopped
				}

				trackChanged := !sameTrack(last, tClean)

				// iTunes lookup only when the track identity changes.
				if trackChanged && it != nil {
					ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
					m, ok := it.Lookup(ctx, tClean.Title, tClean.Artist)
					cancel()
					if ok {
						lastMeta = m
					} else {
						lastMeta = artwork.Meta{}
					}
				}

				// Duration preference: provider duration -> iTunes duration -> last sent duration.
				durForRPC := tClean.Duration
				if durForRPC <= 0 && lastMeta.Duration > 0 {
					durForRPC = lastMeta.Duration
				}
				if durForRPC <= 0 && lastSentDur > 0 {
					durForRPC = lastSentDur
				}

				rawPos := tClean.Position
				if rawPos < 0 {
					rawPos = 0
				}

				// Anchor management on track change and when entering Playing.
				reAnchored := false
				if trackChanged {
					seekStreak = 0

					if effectiveStatus == nowplaying.Playing {
						// Anchor even if rawPos is 0; later correction is allowed once.
						reAnchor(now, rawPos)
						reAnchored = true
						pendingAnchorFix = (rawPos == 0)
					} else {
						haveAnchor = false
						anchorStartUnix = 0
						pendingAnchorFix = false
					}

					// Reset last-good baseline at track boundaries.
					lastGoodPos = rawPos
					lastGoodAt = now

				} else if effectiveStatus == nowplaying.Playing && (!haveAnchor || lastStatus != nowplaying.Playing) {
					// Starting / resuming playback: anchor from current rawPos.
					reAnchor(now, rawPos)
					reAnchored = true
					pendingAnchorFix = (rawPos == 0)

					lastGoodPos = rawPos
					lastGoodAt = now
					seekStreak = 0
				}

				// If stopped, clear immediately.
				if effectiveStatus == nowplaying.Stopped {
					rpc.Clear()
					lastSentDur = 0
					haveAnchor = false
					anchorStartUnix = 0
					pendingAnchorFix = false
					seekStreak = 0
					last = tClean
					lastStatus = effectiveStatus
					continue
				}

				// Compute stable anchored position.
				anchoredPos := anchoredPosNow(now, durForRPC)

				// Predict expected position from lastGoodPos + wall time,
				// to detect raw glitches and reduce jitter.
				predPos := rawPos
				if !lastGoodAt.IsZero() && lastStatus == nowplaying.Playing {
					predPos = lastGoodPos + now.Sub(lastGoodAt)
					predPos = clampDur(predPos, 0, durForRPC)
				}

				// Detect transient rawPos==0 glitch.
				rawValid := true
				if effectiveStatus == nowplaying.Playing && rawPos == 0 && lastGoodPos >= zeroGlitchMinPrevPos {
					if !lastGoodAt.IsZero() && now.Sub(lastGoodAt) <= zeroGlitchMaxAge {
						rawValid = false
					}
				}

				// Update last-good baseline only when raw position is valid.
				if rawValid {
					lastGoodPos = rawPos
					lastGoodAt = now
				}

				// Conservative seek detection:
				// - Ignore early settling window.
				// - Require confirmation for small jumps.
				seek := false
				if effectiveStatus == nowplaying.Playing && haveAnchor && !trackChanged && rawValid {
					if anchoredPos >= ignoreSeekBefore {
						deltaPred := absDur(rawPos - predPos)
						deltaAnch := absDur(rawPos - anchoredPos)

						if deltaPred > seekBigJump || deltaAnch > seekBigJump {
							seek = true
						} else if deltaPred > seekSmallJump || deltaAnch > seekSmallJump {
							seekStreak++
							if seekStreak >= seekConfirmCount {
								seek = true
							}
						} else {
							seekStreak = 0
						}
					} else {
						seekStreak = 0
					}
				} else {
					seekStreak = 0
				}

				// One-time anchor correction after track start (if we anchored from rawPos==0).
				if pendingAnchorFix && effectiveStatus == nowplaying.Playing && rawValid && rawPos >= 2*time.Second {
					if absDur(rawPos-anchoredPos) > seekSmallJump {
						seek = true
					}
					pendingAnchorFix = false
				}

				// Apply seek: re-anchor to raw position.
				if seek && effectiveStatus == nowplaying.Playing {
					reAnchor(now, rawPos)
					reAnchored = true
				}

				// Position sent to Discord is always the anchored position.
				posForRPC := anchoredPosNow(now, durForRPC)

				// Decide whether to send an IPC update.
				need := trackChanged || lastStatus != effectiveStatus || reAnchored

				if effectiveStatus == nowplaying.Playing {
					// If duration becomes known later, resend so progress bar appears.
					if (durForRPC > 0 || lastSentDur > 0) && durForRPC != lastSentDur {
						need = true
					}
					// Optional periodic forced refresh.
					if forceRefresh > 0 && time.Since(lastUpdated) > forceRefresh {
						need = true
					}
				}

				// Throttle updates to reduce IPC spam, but never throttle track change or re-anchor.
				if need && !trackChanged && !reAnchored && lastStatus == effectiveStatus {
					if time.Since(lastUpdated) < updateThrottle {
						need = false
					}
				}

				if need {
					switch effectiveStatus {
					case nowplaying.Playing:
						_ = rpc.SetPlaying(tClean.Artist, tClean.Title, "", posForRPC, durForRPC,
							lastMeta.CoverURL, lastMeta.TrackURL, "")
						if durForRPC > 0 {
							lastSentDur = durForRPC
						}
					case nowplaying.Paused:
						_ = rpc.SetPaused(tClean.Artist, tClean.Title, "",
							lastMeta.CoverURL, lastMeta.TrackURL, "")
					}
					lastUpdated = now
				}

				last = tClean
				lastStatus = effectiveStatus
			}
		}
	}()

	// Tray UI. This keeps the program alive and provides a Quit button.
	systray.Run(func() {
		systray.SetTooltip("Apple Music → Discord Rich Presence")

		// Shows the backend type (useful if multiple providers exist).
		mBackend := systray.AddMenuItem(fmt.Sprintf("Backend: %T", prov), "Current backend")
		mBackend.Disable()

		systray.AddSeparator()

		// Quit item stops the poll loop and exits.
		mQuit := systray.AddMenuItem("Quit", "Exit")
		go func() {
			<-mQuit.ClickedCh
			close(quit)
			systray.Quit()
		}()
	}, func() {})
}
