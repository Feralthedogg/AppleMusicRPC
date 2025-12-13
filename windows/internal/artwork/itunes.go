// windows/internal/artwork/itunes.go
//
// iTunes Search API lookup for:
// - cover art URL
// - Apple Music track URL
// - track duration
// - album title (collectionName)
//
// Improvements:
// - Stronger matching using duration + optional album hint
// - Reject candidates with duration too far away (prevents wrong album/version)
// - Score threshold to avoid "random" picks
package artwork

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
)

type Meta struct {
	CoverURL string
	TrackURL string
	Duration time.Duration
	Album    string

	// Optional: debug/decision aid (0 if not set)
	Score int
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
		CollectionName  string `json:"collectionName"`
		ArtworkUrl100   string `json:"artworkUrl100"`
		TrackViewUrl    string `json:"trackViewUrl"` // sometimes already music.apple.com
		TrackID         int64  `json:"trackId"`
		CollectionID    int64  `json:"collectionId"`
		TrackTimeMillis int64  `json:"trackTimeMillis"`
	} `json:"results"`
}

func absDur(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// key normalizes text for fuzzy matching (keep unicode letters/digits only).
func key(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "’", "'")
	s = strings.ReplaceAll(s, "—", "-")
	s = strings.ReplaceAll(s, "–", "-")

	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
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
	return fmt.Sprintf("https://music.apple.com/%s/album/%d?i=%d", cc, collectionID, trackID)
}

func normalizeTrackURL(itCountry string, rTrackViewURL string, collectionID, trackID int64) string {
	u := strings.TrimSpace(rTrackViewURL)
	if strings.HasPrefix(u, "http://") {
		u = "https://" + strings.TrimPrefix(u, "http://")
	}
	// Prefer a music.apple.com link when available
	if strings.HasPrefix(u, "https://music.apple.com/") {
		return u
	}
	// Otherwise build canonical Apple Music link
	return musicTrackURL(itCountry, collectionID, trackID)
}

// Lookup searches and returns best-effort cover + track link + duration + album.
// albumHint and durHint are used to disambiguate same-title tracks across different albums/versions.
//
// Important behavior:
// - If durHint > 0 and candidate has duration, reject if too far (prevents random albums).
func (it *ITunes) Lookup(ctx context.Context, title, artist, albumHint string, durHint time.Duration) (Meta, bool) {
	q := strings.TrimSpace(artist + " " + title)
	if q == "" {
		return Meta{}, false
	}

	cc := it.country
	if cc == "" {
		cc = "KR"
	}

	// More results helps when there are many variants, but we must score strictly.
	u := "https://itunes.apple.com/search?media=music&entity=song&limit=25&country=" + cc + "&term=" + url.QueryEscape(q)
	req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)

	res, err := it.hc.Do(req)
	if err != nil {
		return Meta{}, false
	}
	defer res.Body.Close()

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return Meta{}, false
	}

	var rr itunesResp
	if err := json.NewDecoder(res.Body).Decode(&rr); err != nil {
		return Meta{}, false
	}
	if len(rr.Results) == 0 {
		return Meta{}, false
	}

	kt := key(title)
	ka := key(artist)
	kalb := key(albumHint)

	if kt == "" || ka == "" {
		return Meta{}, false
	}

	// Duration tolerance: Apple Music/SMTC duration vs iTunes duration can differ slightly.
	// If the diff is huge, it's almost certainly a different version.
	const (
		durTight = 1500 * time.Millisecond
		durMid   = 4 * time.Second
		durLoose = 9 * time.Second // reject beyond this when both durations are known
	)

	bestScore := -1
	bestDurDiff := time.Duration(1<<63 - 1)
	var best Meta

	for _, r := range rr.Results {
		rt := key(r.TrackName)
		ra := key(r.ArtistName)
		rcalb := key(r.CollectionName)

		// Must match title & artist at least somewhat.
		titleOK := (rt == kt) || strings.Contains(rt, kt) || strings.Contains(kt, rt)
		artistOK := (ra == ka) || strings.Contains(ra, ka) || strings.Contains(ka, ra)
		if !titleOK || !artistOK {
			continue
		}

		var candDur time.Duration
		if r.TrackTimeMillis > 0 {
			candDur = time.Duration(r.TrackTimeMillis) * time.Millisecond
		}

		// If we have both durations, reject candidates that are too far away.
		durDiff := time.Duration(0)
		if durHint > 0 && candDur > 0 {
			durDiff = absDur(durHint - candDur)
			if durDiff > durLoose {
				continue
			}
		}

		score := 0

		// Title scoring
		if rt == kt {
			score += 110
		} else if strings.Contains(rt, kt) || strings.Contains(kt, rt) {
			score += 70
		} else {
			score += 25
		}

		// Artist scoring
		if ra == ka {
			score += 110
		} else if strings.Contains(ra, ka) || strings.Contains(ka, ra) {
			score += 70
		} else {
			score += 25
		}

		// Duration scoring (when available)
		if durHint > 0 && candDur > 0 {
			switch {
			case durDiff <= durTight:
				score += 140
			case durDiff <= durMid:
				score += 80
			default:
				score += 20
			}
		} else if durHint > 0 && candDur == 0 {
			// we expected duration but candidate lacks it -> slight penalty
			score -= 10
		}

		// Album hint scoring (optional)
		if kalb != "" {
			if rcalb == kalb {
				score += 90
			} else if rcalb != "" && (strings.Contains(rcalb, kalb) || strings.Contains(kalb, rcalb)) {
				score += 45
			} else {
				score -= 35
			}
		}

		// Mild penalties for common “wrong” results when searching popular titles
		lcAlbum := strings.ToLower(strings.TrimSpace(r.CollectionName))
		if strings.Contains(lcAlbum, "karaoke") || strings.Contains(lcAlbum, "tribute") || strings.Contains(lcAlbum, "cover") {
			score -= 25
		}

		m := Meta{
			CoverURL: forceHTTPS(upscaleArtwork(r.ArtworkUrl100)),
			TrackURL: normalizeTrackURL(it.country, r.TrackViewUrl, r.CollectionID, r.TrackID),
			Album:    strings.TrimSpace(r.CollectionName),
			Duration: candDur,
			Score:    score,
		}

		// Pick best score; if tie, prefer closer duration (when both known)
		better := score > bestScore
		if !better && score == bestScore && durHint > 0 && candDur > 0 && durDiff < bestDurDiff {
			better = true
		}
		if better {
			bestScore = score
			bestDurDiff = durDiff
			best = m
		}
	}

	// Score threshold: if we have duration hint, demand higher confidence.
	minScore := 170
	if durHint > 0 {
		minScore = 230
	}
	if kalb != "" {
		minScore += 15
	}

	if bestScore < minScore {
		return Meta{}, false
	}

	if best.TrackURL != "" && !strings.HasPrefix(best.TrackURL, "https://") {
		best.TrackURL = ""
	}
	if best.CoverURL != "" && !strings.HasPrefix(best.CoverURL, "https://") {
		best.CoverURL = ""
	}

	if best.TrackURL == "" && best.CoverURL == "" && best.Duration <= 0 && best.Album == "" {
		return Meta{}, false
	}
	return best, true
}
