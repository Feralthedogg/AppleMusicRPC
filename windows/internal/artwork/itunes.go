// internal/artwork/itunes.go
//
// Lightweight iTunes Search API lookup for:
// - cover art URL
// - Apple Music track URL
// - track duration (when available)
//
// Album-related outputs are intentionally removed.
package artwork

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Meta struct {
	CoverURL string
	TrackURL string
	Duration time.Duration
}

type ITunes struct {
	hc      *http.Client
	country string // ISO 3166-1 alpha-2, uppercase (e.g. "KR", "US")
}

func NewITunes(country string) *ITunes {
	country = strings.ToUpper(strings.TrimSpace(country))
	if len(country) != 2 {
		country = "KR"
	} else {
		for i := 0; i < 2; i++ {
			if country[i] < 'A' || country[i] > 'Z' {
				country = "KR"
				break
			}
		}
	}
	return &ITunes{
		hc:      &http.Client{Timeout: 2 * time.Second},
		country: country,
	}
}

type itunesResp struct {
	ResultCount int `json:"resultCount"`
	Results     []struct {
		TrackName       string `json:"trackName"`
		ArtistName      string `json:"artistName"`
		ArtworkUrl100   string `json:"artworkUrl100"`
		TrackID         int64  `json:"trackId"`
		CollectionID    int64  `json:"collectionId"`
		TrackTimeMillis int64  `json:"trackTimeMillis"`
	} `json:"results"`
}

func norm(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "’", "'")
	s = strings.ReplaceAll(s, "—", "-")
	s = strings.ReplaceAll(s, "–", "-")
	s = strings.ReplaceAll(s, " ", "")
	return s
}

func upscaleArtwork(u string) string {
	if strings.Contains(u, "100x100bb") {
		return strings.Replace(u, "100x100bb", "512x512bb", 1)
	}
	if strings.Contains(u, "60x60bb") {
		return strings.Replace(u, "60x60bb", "512x512bb", 1)
	}
	if strings.Contains(u, "30x30bb") {
		return strings.Replace(u, "30x30bb", "512x512bb", 1)
	}
	return u
}

func forceHTTPS(u string) string {
	u = strings.TrimSpace(u)
	if strings.HasPrefix(u, "http://") {
		u = "https://" + strings.TrimPrefix(u, "http://")
	}
	return u
}

func musicTrackURL(country string, collectionID, trackID int64) string {
	if collectionID <= 0 || trackID <= 0 {
		return ""
	}
	cc := strings.ToLower(strings.TrimSpace(country))
	if len(cc) != 2 {
		cc = "kr"
	}
	// Apple Music canonical track link pattern:
	// https://music.apple.com/{cc}/album/{collectionId}?i={trackId}
	return fmt.Sprintf("https://music.apple.com/%s/album/%d?i=%d", cc, collectionID, trackID)
}

// Lookup searches "artist title" and returns best-effort cover + track link + duration.
func (it *ITunes) Lookup(ctx context.Context, title, artist string) (Meta, bool) {
	q := strings.TrimSpace(artist + " " + title)
	if q == "" {
		return Meta{}, false
	}

	cc := it.country
	if cc == "" {
		cc = "KR"
	}

	u := "https://itunes.apple.com/search?media=music&entity=song&limit=8&country=" + cc + "&term=" + url.QueryEscape(q)
	req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)

	res, err := it.hc.Do(req)
	if err != nil {
		return Meta{}, false
	}
	defer res.Body.Close()

	var rr itunesResp
	if err := json.NewDecoder(res.Body).Decode(&rr); err != nil {
		return Meta{}, false
	}
	if len(rr.Results) == 0 {
		return Meta{}, false
	}

	nt := norm(title)
	na := norm(artist)

	bestScore := -1
	var best Meta

	for _, r := range rr.Results {
		score := 0
		if nt != "" && strings.Contains(norm(r.TrackName), nt) {
			score += 4
		}
		if na != "" && strings.Contains(norm(r.ArtistName), na) {
			score += 4
		}

		m := Meta{
			CoverURL: forceHTTPS(upscaleArtwork(r.ArtworkUrl100)),
			TrackURL: musicTrackURL(it.country, r.CollectionID, r.TrackID),
		}
		if r.TrackTimeMillis > 0 {
			m.Duration = time.Duration(r.TrackTimeMillis) * time.Millisecond
		}

		if score > bestScore {
			bestScore = score
			best = m
		}
	}

	if bestScore <= 0 {
		return Meta{}, false
	}

	if best.TrackURL != "" && !strings.HasPrefix(best.TrackURL, "https://") {
		best.TrackURL = ""
	}
	if best.CoverURL != "" && !strings.HasPrefix(best.CoverURL, "https://") {
		best.CoverURL = ""
	}

	if best.TrackURL == "" && best.CoverURL == "" && best.Duration <= 0 {
		return Meta{}, false
	}
	return best, true
}
