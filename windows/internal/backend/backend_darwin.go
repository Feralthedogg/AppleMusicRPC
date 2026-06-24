//go:build darwin

package backend

import (
	"amrpc/internal/nowplaying"
	"amrpc/internal/nowplaying/applemusic"
)

// New returns the macOS Music.app provider.
func New(prefer []string) (nowplaying.Provider, bool) {
	return applemusic.New(prefer)
}
