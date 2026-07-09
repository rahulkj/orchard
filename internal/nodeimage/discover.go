// Package nodeimage finds the latest available kindest/node build via
// Docker Hub's public tags API, so orchard can tell a user their cluster is
// on an older Kubernetes release than what's available.
package nodeimage

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"

	"github.com/rahulkj/orchard/internal/httpx"
)

// firstPageURL is overridden in tests to point at an httptest server.
var firstPageURL = "https://hub.docker.com/v2/repositories/kindest/node/tags?page_size=100"

// maxPages bounds pagination so a malformed or adversarial "next" link (e.g.
// a self-referential loop) can't hang this in an infinite fetch loop. The
// real tag list is ~2 pages at 100 tags/page; 50 pages (5000 tags) is far
// more headroom than the repository will plausibly ever need.
const maxPages = 50

// Release is one discovered kindest/node build.
type Release struct {
	Version string // e.g. "v1.36.2"
	Image   string // docker.io/kindest/node@sha256:...
}

var semverTag = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)$`)

// Latest returns the highest semver-tagged kindest/node release across
// every tag in the repository. The API's `ordering` parameter is not
// honored for this (org-owned) repository and `last_updated` comes back
// empty, so there's no reliable way to ask the API for "newest" -- this
// pages through the full tag list (currently ~175 tags, 2 pages) and picks
// the true maximum by parsing each name as semver.
func Latest() (Release, error) {
	var best Release
	var bestV [3]int
	url := firstPageURL
	for pages := 0; url != ""; pages++ {
		if pages >= maxPages {
			return Release{}, fmt.Errorf("querying Docker Hub for kindest/node tags: exceeded %d pages", maxPages)
		}
		var page struct {
			Next    string `json:"next"`
			Results []struct {
				Name   string `json:"name"`
				Digest string `json:"digest"`
			} `json:"results"`
		}
		resp, err := httpx.Client.Get(url)
		if err != nil {
			return Release{}, fmt.Errorf("querying Docker Hub for kindest/node tags: %w", err)
		}
		err = func() error {
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != 200 {
				return fmt.Errorf("querying Docker Hub for kindest/node tags: HTTP %d", resp.StatusCode)
			}
			return json.NewDecoder(resp.Body).Decode(&page)
		}()
		if err != nil {
			return Release{}, err
		}

		for _, t := range page.Results {
			m := semverTag.FindStringSubmatch(t.Name)
			if m == nil || t.Digest == "" {
				continue
			}
			v := [3]int{atoi(m[1]), atoi(m[2]), atoi(m[3])}
			if best.Version == "" || greater(v, bestV) {
				bestV = v
				best = Release{Version: t.Name, Image: "docker.io/kindest/node@" + t.Digest}
			}
		}
		url = page.Next
	}
	if best.Version == "" {
		return Release{}, fmt.Errorf("no semver-tagged kindest/node releases found")
	}
	return best, nil
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

func greater(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}
