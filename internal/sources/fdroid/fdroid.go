// Package fdroid reads an F-Droid repo's index-v1.json.
package fdroid

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type Package struct {
	Name        string
	VersionName string
	VersionCode int64
	Added       time.Time
}

type index struct {
	Packages map[string][]struct {
		VersionName string `json:"versionName"`
		VersionCode int64  `json:"versionCode"`
		Added       int64  `json:"added"` // ms since epoch
	} `json:"packages"`
}

// ParseIndex returns the highest versionCode of every package.
func ParseIndex(b []byte) (map[string]Package, error) {
	var idx index
	if err := json.Unmarshal(b, &idx); err != nil {
		return nil, fmt.Errorf("fdroid index: %w", err)
	}
	out := map[string]Package{}
	for name, versions := range idx.Packages {
		for _, v := range versions {
			if cur, ok := out[name]; ok && cur.VersionCode >= v.VersionCode {
				continue
			}
			p := Package{Name: name, VersionName: v.VersionName, VersionCode: v.VersionCode}
			if v.Added > 0 {
				p.Added = time.UnixMilli(v.Added).UTC()
			}
			out[name] = p
		}
	}
	return out, nil
}

// Fetch GETs and parses an index, sending userAgent.
func Fetch(ctx context.Context, c *http.Client, url, userAgent string) (map[string]Package, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fdroid index: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fdroid index %s: HTTP %d", url, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, fmt.Errorf("fdroid index: %w", err)
	}
	return ParseIndex(b)
}
