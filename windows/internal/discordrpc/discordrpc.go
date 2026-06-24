// windows/internal/discordrpc/discordrpc.go
//
// Discord Rich Presence over Discord IPC.
//
// This implements:
// - connect & handshake to the platform Discord IPC endpoint
// - framed JSON read/write
// - SET_ACTIVITY to publish a Listening activity
//
// UI notes:
// - We use "details" as the top (bold) line (TITLE only).
// - We use "state" as the second line (ARTIST and optionally ALBUM).
// - Only one button is used ("Play On Apple Music").
package discordrpc

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

const (
	opHandshake int32 = 0
	opFrame     int32 = 1
)

const (
	ActivityTypeListening = 2
)

type RPC struct {
	appID      string
	largeImage string
	largeText  string

	pipe io.ReadWriteCloser
	pid  int

	activityType int
	activityName string
}

func Login(appID, largeImage, largeText string) (*RPC, error) {
	r := New(appID, largeImage, largeText)
	if err := r.connect(); err != nil {
		return nil, err
	}
	return r, nil
}

// New creates an RPC client without connecting immediately.
// Calls to SetPlaying, SetPaused, or Clear will attempt to connect lazily.
func New(appID, largeImage, largeText string) *RPC {
	return &RPC{
		appID:        appID,
		largeImage:   largeImage,
		largeText:    largeText,
		pid:          os.Getpid(),
		activityType: ActivityTypeListening,
		activityName: "Apple Music",
	}
}

func (r *RPC) connect() error {
	var lastErr error
	for i := 0; i < 10; i++ {
		conn, _, err := openDiscordIPC(i)
		if err != nil {
			lastErr = err
			continue
		}
		r.pipe = conn

		hs := map[string]any{
			"v":         1,
			"client_id": r.appID,
		}
		if err := r.write(opHandshake, hs); err != nil {
			_ = r.pipe.Close()
			r.pipe = nil
			lastErr = err
			continue
		}

		// Best-effort: discard handshake response.
		_, _, _ = r.readWithTimeout(2 * time.Second)
		return nil
	}
	if lastErr == nil {
		lastErr = errors.New("discord ipc pipe not found")
	}
	return fmt.Errorf("discord ipc connect failed: %w", lastErr)
}

func (r *RPC) ensure() error {
	if r.pipe != nil {
		return nil
	}
	return r.connect()
}

func (r *RPC) Close() {
	if r.pipe != nil {
		_ = r.pipe.Close()
		r.pipe = nil
	}
}

func (r *RPC) Clear() {
	_ = r.ensure()
	_ = r.setActivity(nil)
}

type activityTimestamps struct {
	Start int64 `json:"start,omitempty"`
	End   int64 `json:"end,omitempty"`
}

type activityAssets struct {
	LargeImage string `json:"large_image,omitempty"`
	LargeText  string `json:"large_text,omitempty"`
}

type activityButton struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

type activity struct {
	Name       string              `json:"name"`
	Type       int                 `json:"type,omitempty"`
	Details    string              `json:"details,omitempty"` // Top (bold) line
	State      string              `json:"state,omitempty"`   // Second line
	Timestamps *activityTimestamps `json:"timestamps,omitempty"`
	Assets     *activityAssets     `json:"assets,omitempty"`
	Buttons    []activityButton    `json:"buttons,omitempty"`
}

// buildState constructs the second line text.
// - If both are present: "Artist • Album"
// - If only artist: "Artist"
// - If only album: "Album"
func buildState(artist, album string) string {
	artist = strings.TrimSpace(artist)
	album = strings.TrimSpace(album)

	if artist != "" && album != "" {
		return artist + " • " + album
	}
	if artist != "" {
		return artist
	}
	return album
}

// SetPlaying publishes a Listening activity with a progress bar.
// - details: title only
// - state: artist (+ album)
func (r *RPC) SetPlaying(artist, title, album string, pos, dur time.Duration, coverURL, trackURL, _albumURL string) error {
	if err := r.ensure(); err != nil {
		return err
	}

	now := time.Now()
	start := now.Add(-pos)

	var ts *activityTimestamps
	if dur > 0 {
		end := start.Add(dur)
		ts = &activityTimestamps{Start: start.Unix(), End: end.Unix()}
	} else {
		// If duration unknown, sending only start still gives a "started at" style timestamp.
		ts = &activityTimestamps{Start: start.Unix()}
	}

	large := r.largeImage
	if coverURL != "" {
		large = coverURL
	}
	lt := r.largeText
	if lt == "" {
		lt = "Apple Music"
	}

	btns := make([]activityButton, 0, 1)
	if trackURL != "" {
		btns = append(btns, activityButton{Label: "Play On Apple Music", URL: trackURL})
	}

	a := &activity{
		Name:       r.activityName,
		Type:       r.activityType,
		Details:    strings.TrimSpace(title),  // TITLE only
		State:      buildState(artist, album), // ARTIST (+ ALBUM)
		Timestamps: ts,
		Assets:     &activityAssets{LargeImage: large, LargeText: lt},
		Buttons:    btns,
	}
	return r.setActivity(a)
}

// SetPaused publishes a non-progressing activity (timestamps omitted).
// - details: title only
// - state: artist (+ album)
func (r *RPC) SetPaused(artist, title, album string, coverURL, trackURL, _albumURL string) error {
	if err := r.ensure(); err != nil {
		return err
	}

	large := r.largeImage
	if coverURL != "" {
		large = coverURL
	}
	lt := r.largeText
	if lt == "" {
		lt = "Apple Music"
	}

	btns := make([]activityButton, 0, 1)
	if trackURL != "" {
		btns = append(btns, activityButton{Label: "Play On Apple Music", URL: trackURL})
	}

	a := &activity{
		Name:    r.activityName,
		Type:    r.activityType,
		Details: strings.TrimSpace(title),  // TITLE only
		State:   buildState(artist, album), // ARTIST (+ ALBUM)
		Assets:  &activityAssets{LargeImage: large, LargeText: lt},
		Buttons: btns,
	}
	return r.setActivity(a)
}

func (r *RPC) setActivity(a *activity) error {
	nonce := randNonce()

	payload := map[string]any{
		"cmd":   "SET_ACTIVITY",
		"nonce": nonce,
		"args": map[string]any{
			"pid":      r.pid,
			"activity": a,
		},
	}

	if err := r.write(opFrame, payload); err != nil {
		// If pipe broke, reconnect once and retry.
		r.Close()
		if err2 := r.connect(); err2 != nil {
			return err2
		}
		if err2 := r.write(opFrame, payload); err2 != nil {
			return err2
		}
	}

	// Best-effort: discard response (Discord replies with a frame).
	_, _, _ = r.readWithTimeout(2 * time.Second)
	return nil
}

func (r *RPC) write(op int32, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}

	var hdr [8]byte
	binary.LittleEndian.PutUint32(hdr[0:4], uint32(op))
	binary.LittleEndian.PutUint32(hdr[4:8], uint32(len(b)))

	if err := writeFull(r.pipe, hdr[:]); err != nil {
		return err
	}
	return writeFull(r.pipe, b)
}

func (r *RPC) read() (op int32, data []byte, err error) {
	var hdr [8]byte
	if _, err = readFull(r.pipe, hdr[:]); err != nil {
		return 0, nil, err
	}

	op = int32(binary.LittleEndian.Uint32(hdr[0:4]))
	n := int(binary.LittleEndian.Uint32(hdr[4:8]))
	if n < 0 || n > 10*1024*1024 {
		return 0, nil, fmt.Errorf("discord ipc invalid frame length: %d", n)
	}

	buf := make([]byte, n)
	if _, err = readFull(r.pipe, buf); err != nil {
		return 0, nil, err
	}
	return op, buf, nil
}

type readDeadliner interface {
	SetReadDeadline(time.Time) error
}

func (r *RPC) readWithTimeout(timeout time.Duration) (op int32, data []byte, err error) {
	if timeout > 0 {
		if d, ok := r.pipe.(readDeadliner); ok {
			_ = d.SetReadDeadline(time.Now().Add(timeout))
			defer d.SetReadDeadline(time.Time{})
		}
	}
	return r.read()
}

func writeFull(w io.Writer, b []byte) error {
	for len(b) > 0 {
		n, err := w.Write(b)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		b = b[n:]
	}
	return nil
}

func readFull(r io.Reader, b []byte) (int, error) {
	total := 0
	for total < len(b) {
		n, err := r.Read(b[total:])
		if n > 0 {
			total += n
		}
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

func randNonce() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func _prettyJSON(in []byte) string {
	var out bytes.Buffer
	_ = json.Indent(&out, in, "", "  ")
	return out.String()
}
