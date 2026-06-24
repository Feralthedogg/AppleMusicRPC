//go:build windows

package backend

import (
	"amrpc/internal/nowplaying"
	"amrpc/internal/nowplaying/smtcwinrt"
)

// New returns the Windows SMTC provider.
func New(prefer []string) (nowplaying.Provider, bool) {
	return smtcwinrt.New(prefer)
}
