//go:build !windows && !darwin

package backend

import "amrpc/internal/nowplaying"

// New returns no provider on unsupported platforms.
func New(_ []string) (nowplaying.Provider, bool) {
	return nil, false
}
