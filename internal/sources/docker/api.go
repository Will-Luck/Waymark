package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// apiTimeout bounds one Engine API read.
const apiTimeout = 10 * time.Second

// maxAPIBody caps what one container list may weigh.
const maxAPIBody = 8 << 20

// apiContainer is the part of the Engine API's container list Waymark reads.
type apiContainer struct {
	ID      string            `json:"Id"`
	Names   []string          `json:"Names"`
	Image   string            `json:"Image"`
	State   string            `json:"State"`
	Status  string            `json:"Status"`
	Created int64             `json:"Created"`
	Labels  map[string]string `json:"Labels"`
}

// FetchAPI lists every container (running or not) through the Docker Engine
// API. host is unix:///path/to/socket or tcp://host:port (a
// docker-socket-proxy). It sends one read-only request, on the unversioned
// path so any engine version answers, with userAgent.
func FetchAPI(ctx context.Context, host, userAgent string) (*Feed, error) {
	client, base, shown, err := apiClient(host)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, apiTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/containers/json?all=1", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("docker API at %s: %w", shown, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("docker API at %s: %s", shown, resp.Status)
	}
	var list []apiContainer
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxAPIBody)).Decode(&list); err != nil {
		return nil, fmt.Errorf("docker API at %s: unreadable answer: %w", shown, err)
	}
	out := make([]Container, 0, len(list))
	for _, ac := range list {
		if len(ac.Names) == 0 {
			continue
		}
		c := Container{
			ID:     ac.ID,
			Name:   ownName(ac.Names),
			Image:  ac.Image,
			State:  ac.State,
			Status: ac.Status,
			Labels: ac.Labels,
		}
		if c.Labels == nil {
			c.Labels = map[string]string{}
		}
		if ac.Created > 0 {
			c.CreatedAt = time.Unix(ac.Created, 0).UTC()
		}
		out = append(out, c)
	}
	return &Feed{Containers: out, ModTime: time.Now().UTC()}, nil
}

// ownName is the container's own name: the entry with exactly one slash
// ("/web"), as against a link alias ("/other/web"). It falls back to the
// first entry.
func ownName(names []string) string {
	for _, n := range names {
		if strings.Count(n, "/") == 1 && strings.HasPrefix(n, "/") {
			return n[1:]
		}
	}
	return strings.TrimPrefix(names[0], "/")
}

// noRedirect makes a 3xx the answer, so a different request is never sent.
func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// apiClient is an HTTP client for host, the URL prefix to request under and
// a redacted form of host for messages. Errors never repeat host itself: it
// may carry credentials.
func apiClient(host string) (*http.Client, string, string, error) {
	u, err := url.Parse(host)
	if err != nil {
		return nil, "", "", errors.New("docker host is not a URL; use unix:///path or tcp://host:port")
	}
	switch u.Scheme {
	case "unix":
		sock := u.Path
		if sock == "" || u.Host != "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return nil, "", "", errors.New("docker host must be unix:///path/to/socket")
		}
		tr := &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", sock)
			},
			DisableKeepAlives: true,
		}
		return &http.Client{Transport: tr, CheckRedirect: noRedirect}, "http://docker", "unix://" + sock, nil
	case "tcp":
		_, port, herr := net.SplitHostPort(u.Host)
		if herr != nil || port == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
			return nil, "", "", errors.New("docker host must be tcp://host:port, with no credentials, path or query")
		}
		return &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, CheckRedirect: noRedirect}, "http://" + u.Host, "tcp://" + u.Host, nil
	}
	return nil, "", "", errors.New("docker host must start with unix:// or tcp://")
}
