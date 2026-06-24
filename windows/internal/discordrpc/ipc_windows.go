//go:build windows

package discordrpc

import (
	"fmt"
	"io"
	"os"
)

func openDiscordIPC(index int) (io.ReadWriteCloser, string, error) {
	path := fmt.Sprintf(`\\.\pipe\discord-ipc-%d`, index)
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, path, err
	}
	return f, path, nil
}
