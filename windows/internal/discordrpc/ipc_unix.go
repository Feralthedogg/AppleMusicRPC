//go:build darwin || linux || freebsd || openbsd || netbsd

package discordrpc

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func openDiscordIPC(index int) (io.ReadWriteCloser, string, error) {
	name := fmt.Sprintf("discord-ipc-%d", index)
	var lastErr error

	for _, dir := range discordRuntimeDirs() {
		path := filepath.Join(dir, name)
		conn, err := net.DialTimeout("unix", path, 700*time.Millisecond)
		if err == nil {
			return conn, path, nil
		}
		lastErr = err
	}

	if lastErr == nil {
		lastErr = errors.New("discord ipc socket not found")
	}
	return nil, "", lastErr
}

func discordRuntimeDirs() []string {
	candidates := make([]string, 0, 8)
	for _, env := range []string{"XDG_RUNTIME_DIR", "TMPDIR", "TMP", "TEMP"} {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			candidates = append(candidates, v)
		}
	}

	candidates = append(candidates, "/tmp", "/var/tmp", "/usr/tmp")

	out := make([]string, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, dir := range candidates {
		dir = filepath.Clean(dir)
		if dir == "." || dir == "" {
			continue
		}
		if _, ok := seen[dir]; ok {
			continue
		}
		seen[dir] = struct{}{}
		out = append(out, dir)
	}
	return out
}
