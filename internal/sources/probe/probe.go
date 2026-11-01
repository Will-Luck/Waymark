// Package probe checks a URL with one GET: 2xx, 3xx, 401 and 403 count as up.
package probe

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Timeout is the per-check limit.
const Timeout = 5 * time.Second

type Result struct {
	URL       string
	Up        bool
	Code      int
	LatencyMS int
	At        time.Time
	Err       string
	// Refused: a public-only client would not connect (ErrNotPublic), so the
	// URL was not checked; it is neither up nor down.
	Refused bool
}

// NewClient returns a client that does not follow redirects (a redirect is an
// answer) and gives up after Timeout.
func NewClient() *http.Client {
	return &http.Client{
		Timeout: Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// IsUp is the up rule for a status code.
func IsUp(code int) bool {
	return (code >= 200 && code < 400) || code == http.StatusUnauthorized || code == http.StatusForbidden
}

// Check GETs u once, as waymark-probe/<version>.
func Check(ctx context.Context, c *http.Client, u, version string) Result {
	r := Result{URL: u, At: time.Now().UTC()}
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		r.Err = err.Error()
		return r
	}
	req.Header.Set("User-Agent", "waymark-probe/"+version)
	start := time.Now()
	resp, err := c.Do(req)
	r.LatencyMS = int(time.Since(start).Milliseconds())
	if err != nil {
		r.Refused = errors.Is(err, ErrNotPublic)
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		r.Err = err.Error()
		return r
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	r.Code = resp.StatusCode
	r.Up = IsUp(resp.StatusCode)
	return r
}
