// Package update finds out whether a newer Quarry release is published.
// Installs are managed by package managers or by hand, so it only reports
// the release; it never replaces the binary.
package update

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// LatestURL is the GitHub API endpoint of the latest release.
const LatestURL = "https://api.github.com/repos/gxd3v/quarry/releases/latest"

type Release struct {
	Version string // e.g. "v1.2.0"
	URL     string // release page
}

// Latest asks url (LatestURL in production) for the latest release.
func Latest(ctx context.Context, c *http.Client, url string) (Release, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	res, err := c.Do(req)
	if err != nil {
		return Release{}, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("update check: %s", res.Status)
	}
	var body struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		return Release{}, fmt.Errorf("update check: %w", err)
	}
	if _, ok := parse(body.TagName); !ok {
		return Release{}, fmt.Errorf("update check: release tag %q is not a version", body.TagName)
	}
	return Release{Version: body.TagName, URL: body.HTMLURL}, nil
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
