// Package update finds newer Quarry releases on GitHub and installs them
// over the running copy (see Apply).
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// LatestURL is the GitHub API endpoint of the latest release.
const LatestURL = "https://api.github.com/repos/gxd3v/quarry/releases/latest"

type Release struct {
	Version string            // e.g. "v1.2.0"
	URL     string            // release page
	Notes   string            // release notes, Markdown
	Date    time.Time         // publication time
	Assets  map[string]string // file name -> download URL
}

// ErrRateLimited means GitHub refused the check until the given time.
type ErrRateLimited struct{ Until time.Time }

func (e *ErrRateLimited) Error() string {
	return "update check rate limited until " + e.Until.Format(time.Kitchen)
}

// Checker asks for the latest release with conditional requests: GitHub
// does not count "304 Not Modified" answers against its rate limit, which
// keeps frequent checks within the anonymous quota.
type Checker struct {
	URL    string
	Client *http.Client

	mu      sync.Mutex
	etag    string
	latest  *Release
	blocked time.Time
}

func NewChecker(url string) *Checker {
	return &Checker{URL: url, Client: &http.Client{Timeout: 15 * time.Second}}
}

// Latest returns the latest release, from the cache when GitHub reports no
// change.
func (c *Checker) Latest(ctx context.Context) (Release, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Now().Before(c.blocked) {
		return c.cached(&ErrRateLimited{Until: c.blocked})
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL, nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if c.etag != "" && c.latest != nil {
		req.Header.Set("If-None-Match", c.etag)
	}
	res, err := c.Client.Do(req)
	if err != nil {
		return c.cached(fmt.Errorf("update check: %w", err))
	}
	defer res.Body.Close()
	switch {
	case res.StatusCode == http.StatusNotModified && c.latest != nil:
		return *c.latest, nil
	case res.StatusCode == http.StatusForbidden || res.StatusCode == http.StatusTooManyRequests:
		c.blocked = resetTime(res.Header)
		return c.cached(&ErrRateLimited{Until: c.blocked})
	case res.StatusCode == http.StatusNotFound:
		return Release{}, errors.New("no release published")
	case res.StatusCode != http.StatusOK:
		return c.cached(fmt.Errorf("update check: %s", res.Status))
	}
	r, err := decode(res)
	if err != nil {
		return Release{}, err
	}
	c.etag, c.latest = res.Header.Get("ETag"), &r
	return r, nil
}

// cached returns the last release alongside err, so a failed check still
// knows what was found before.
func (c *Checker) cached(err error) (Release, error) {
	if c.latest != nil {
		return *c.latest, err
	}
	return Release{}, err
}

func resetTime(h http.Header) time.Time {
	if s, err := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64); err == nil {
		return time.Unix(s, 0)
	}
	if s, err := strconv.Atoi(h.Get("Retry-After")); err == nil {
		return time.Now().Add(time.Duration(s) * time.Second)
	}
	return time.Now().Add(10 * time.Minute)
}

func decode(res *http.Response) (Release, error) {
	var body struct {
		TagName     string    `json:"tag_name"`
		HTMLURL     string    `json:"html_url"`
		Body        string    `json:"body"`
		PublishedAt time.Time `json:"published_at"`
		Assets      []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		return Release{}, fmt.Errorf("update check: %w", err)
	}
	if _, ok := parse(body.TagName); !ok {
		return Release{}, fmt.Errorf("update check: release tag %q is not a version", body.TagName)
	}
	r := Release{Version: body.TagName, URL: body.HTMLURL, Notes: body.Body, Date: body.PublishedAt, Assets: map[string]string{}}
	for _, a := range body.Assets {
		r.Assets[a.Name] = a.URL
	}
	return r, nil
}

// Newer reports whether version b is newer than a ("v1.2.3", optionally
// with a "-pre" suffix, which sorts before the release). Unparsable
// versions are never newer.
func Newer(a, b string) bool {
	va, okA := parse(a)
	vb, okB := parse(b)
	if !okA || !okB {
		return false
	}
	for i := range 3 {
		if va.n[i] != vb.n[i] {
			return vb.n[i] > va.n[i]
		}
	}
	switch {
	case va.pre == vb.pre:
		return false
	case vb.pre == "":
		return true
	case va.pre == "":
		return false
	}
	return vb.pre > va.pre
}

type version struct {
	n   [3]int
	pre string
}

func parse(s string) (version, bool) {
	var v version
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	s, v.pre, _ = strings.Cut(s, "-")
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return v, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return v, false
		}
		v.n[i] = n
	}
	return v, true
}
