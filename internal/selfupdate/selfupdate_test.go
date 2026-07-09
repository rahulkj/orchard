package selfupdate

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPickAsset(t *testing.T) {
	assets := []releaseAsset{
		{Name: "orchard_linux_amd64.tar.gz", BrowserDownloadURL: "https://example.com/linux_amd64"},
		{Name: "orchard_darwin_arm64.tar.gz", BrowserDownloadURL: "https://example.com/darwin_arm64"},
		{Name: "orchard_darwin_amd64.tar.gz", BrowserDownloadURL: "https://example.com/darwin_amd64"},
	}

	rel, ok := pickAsset("v1.2.3", assets, "darwin", "arm64")
	if !ok {
		t.Fatal("pickAsset did not find the darwin/arm64 asset")
	}
	if rel.AssetName != "orchard_darwin_arm64.tar.gz" || rel.AssetURL != "https://example.com/darwin_arm64" || rel.Tag != "v1.2.3" {
		t.Errorf("pickAsset = %+v, want asset orchard_darwin_arm64.tar.gz at https://example.com/darwin_arm64, tag v1.2.3", rel)
	}

	if _, ok := pickAsset("v1.2.3", assets, "windows", "amd64"); ok {
		t.Error("pickAsset found a match for a platform with no asset")
	}
}

func TestLatestReleaseFindsPlatformAsset(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"tag_name":"v9.9.9","assets":[
			{"name":"orchard_darwin_arm64.tar.gz","browser_download_url":"https://example.com/asset"},
			{"name":"orchard_darwin_amd64.tar.gz","browser_download_url":"https://example.com/other"}
		]}`)
	}))
	defer srv.Close()

	orig := githubAPIBase
	githubAPIBase = srv.URL
	defer func() { githubAPIBase = orig }()

	rel, err := LatestRelease("owner/repo")
	if err != nil {
		t.Fatalf("LatestRelease: %v", err)
	}
	if rel.Tag != "v9.9.9" {
		t.Errorf("LatestRelease().Tag = %q, want %q", rel.Tag, "v9.9.9")
	}
}

func TestLatestReleaseNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	orig := githubAPIBase
	githubAPIBase = srv.URL
	defer func() { githubAPIBase = orig }()

	if _, err := LatestRelease("owner/repo"); err == nil {
		t.Error("LatestRelease() = nil error on a 404, want a \"no releases found\" error")
	}
}

func TestLatestReleaseNoMatchingAsset(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"tag_name":"v9.9.9","assets":[{"name":"orchard_linux_amd64.tar.gz","browser_download_url":"https://example.com/x"}]}`)
	}))
	defer srv.Close()

	orig := githubAPIBase
	githubAPIBase = srv.URL
	defer func() { githubAPIBase = orig }()

	if _, err := LatestRelease("owner/repo"); err == nil {
		t.Error("LatestRelease() = nil error when no asset matches this platform, want an error")
	}
}
