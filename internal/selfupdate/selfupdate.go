// Package selfupdate checks GitHub releases for a newer orchard build and
// replaces the running binary in place.
//
// This expects a goreleaser-style release: a tag, and an asset named
// orchard_<GOOS>_<GOARCH>.tar.gz containing an `orchard` binary at its root.
// Until this project has a published repo with releases in that shape,
// LatestRelease returns a clear "no releases found" error rather than
// failing confusingly.
package selfupdate

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/rahulkj/orchard/internal/httpx"
)

// Release is a discovered GitHub release with an asset for this platform.
type Release struct {
	Tag       string
	AssetURL  string
	AssetName string
}

// releaseAsset is one entry in a GitHub release's "assets" array.
type releaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// githubAPIBase is overridden in tests to point at an httptest server.
var githubAPIBase = "https://api.github.com"

// LatestRelease queries the GitHub API for repo's (owner/name) latest
// release and finds the asset matching this platform.
func LatestRelease(repo string) (Release, error) {
	url := fmt.Sprintf("%s/repos/%s/releases/latest", githubAPIBase, repo)
	resp, err := httpx.Client.Get(url)
	if err != nil {
		return Release{}, fmt.Errorf("querying GitHub releases for %s: %w", repo, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == 404 {
		return Release{}, fmt.Errorf("no releases found for %s (has this project been published with releases yet?)", repo)
	}
	if resp.StatusCode != 200 {
		return Release{}, fmt.Errorf("querying GitHub releases for %s: HTTP %d", repo, resp.StatusCode)
	}

	var body struct {
		TagName string         `json:"tag_name"`
		Assets  []releaseAsset `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return Release{}, fmt.Errorf("parsing GitHub release response: %w", err)
	}

	rel, ok := pickAsset(body.TagName, body.Assets, runtime.GOOS, runtime.GOARCH)
	if !ok {
		return Release{}, fmt.Errorf("release %s has no asset named %s for this platform", body.TagName, assetName(runtime.GOOS, runtime.GOARCH))
	}
	return rel, nil
}

func assetName(goos, goarch string) string {
	return fmt.Sprintf("orchard_%s_%s.tar.gz", goos, goarch)
}

// pickAsset finds the release asset matching goos/goarch's goreleaser-style
// name. Split out from LatestRelease so the matching logic can be tested
// without a network call.
func pickAsset(tag string, assets []releaseAsset, goos, goarch string) (Release, bool) {
	want := assetName(goos, goarch)
	for _, a := range assets {
		if a.Name == want {
			return Release{Tag: tag, AssetURL: a.BrowserDownloadURL, AssetName: a.Name}, true
		}
	}
	return Release{}, false
}

// Apply downloads a release asset (a .tar.gz containing an `orchard`
// binary), and atomically replaces the currently running executable with
// it. The replacement file is created in the same directory as the running
// binary so the final rename is on one filesystem and therefore atomic.
func Apply(assetURL string) error {
	resp, err := httpx.DownloadClient.Get(assetURL)
	if err != nil {
		return fmt.Errorf("downloading %s: %w", assetURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		return fmt.Errorf("downloading %s: HTTP %d", assetURL, resp.StatusCode)
	}

	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return fmt.Errorf("reading release archive: %w", err)
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	var bin []byte
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("reading release archive: %w", err)
		}
		if filepath.Base(hdr.Name) == "orchard" {
			bin, err = io.ReadAll(tr)
			if err != nil {
				return fmt.Errorf("reading orchard binary from archive: %w", err)
			}
			break
		}
	}
	if bin == nil {
		return fmt.Errorf("no orchard binary found in release archive")
	}

	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locating running executable: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(exePath), "orchard-update-*")
	if err != nil {
		return fmt.Errorf("creating temp file next to %s: %w", exePath, err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()

	if _, err := tmp.Write(bin); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing new binary: %w", err)
	}
	if err := tmp.Chmod(0o755); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), exePath); err != nil {
		return fmt.Errorf("replacing %s: %w", exePath, err)
	}
	return nil
}
