// internal/config/geoip.go

package config

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

func normalizeCountryISO2(s string) (string, bool) {
	s = strings.TrimSpace(strings.ToUpper(s))
	if len(s) != 2 {
		return "", false
	}
	for i := 0; i < 2; i++ {
		if s[i] < 'A' || s[i] > 'Z' {
			return "", false
		}
	}
	// Some providers may return "ZZ" for unknown.
	if s == "ZZ" {
		return "", false
	}
	return s, true
}

// DetectCountryISO tries a few lightweight endpoints and returns ISO 2-letter country code.
// This uses external services; if you want zero external calls, set Config.Country explicitly.
func DetectCountryISO(ctx context.Context) (string, error) {
	// Each endpoint returns plain text like "KR\n" or "US".
	endpoints := []string{
		"https://ipinfo.io/country",
		"https://ifconfig.co/country-iso",
		"https://ipapi.co/country/",
	}

	hc := &http.Client{Timeout: 1800 * time.Millisecond}

	var lastErr error
	for _, ep := range endpoints {
		req, _ := http.NewRequestWithContext(ctx, "GET", ep, nil)
		req.Header.Set("User-Agent", "amrpc/1.0 (+https://example.invalid)")

		res, err := hc.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		b, _ := io.ReadAll(io.LimitReader(res.Body, 64))
		_ = res.Body.Close()

		if res.StatusCode < 200 || res.StatusCode >= 300 {
			lastErr = errors.New(res.Status)
			continue
		}

		if cc, ok := normalizeCountryISO2(string(b)); ok {
			return cc, nil
		}
		lastErr = errors.New("invalid country response")
	}

	if lastErr == nil {
		lastErr = errors.New("geoip lookup failed")
	}
	return "", lastErr
}
