package nodeimage

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLatestPicksHighestSemver(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"next":"","results":[
			{"name":"v1.30.0","digest":"sha256:aaa"},
			{"name":"v1.31.2","digest":"sha256:bbb"},
			{"name":"v1.9.9","digest":"sha256:ccc"},
			{"name":"latest","digest":"sha256:ddd"},
			{"name":"v1.31.10","digest":"sha256:eee"},
			{"name":"v1.31.9","digest":""}
		]}`)
	}))
	defer srv.Close()

	orig := firstPageURL
	firstPageURL = srv.URL
	defer func() { firstPageURL = orig }()

	rel, err := Latest()
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if rel.Version != "v1.31.10" {
		t.Errorf("Latest().Version = %q, want %q (must pick numeric max, not lexical, and skip untagged/no-digest entries)", rel.Version, "v1.31.10")
	}
	if rel.Image != "docker.io/kindest/node@sha256:eee" {
		t.Errorf("Latest().Image = %q, want the image matching the winning tag's digest", rel.Image)
	}
}

func TestLatestFollowsPagination(t *testing.T) {
	var page2URL string
	mux := http.NewServeMux()
	mux.HandleFunc("/page1", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, `{"next":%q,"results":[{"name":"v1.30.0","digest":"sha256:aaa"}]}`, page2URL)
	})
	mux.HandleFunc("/page2", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"next":"","results":[{"name":"v1.32.0","digest":"sha256:bbb"}]}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	page2URL = srv.URL + "/page2"

	orig := firstPageURL
	firstPageURL = srv.URL + "/page1"
	defer func() { firstPageURL = orig }()

	rel, err := Latest()
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if rel.Version != "v1.32.0" {
		t.Errorf("Latest().Version = %q, want %q from the second page", rel.Version, "v1.32.0")
	}
}

func TestLatestNoTagsIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"next":"","results":[{"name":"latest","digest":"sha256:aaa"}]}`)
	}))
	defer srv.Close()

	orig := firstPageURL
	firstPageURL = srv.URL
	defer func() { firstPageURL = orig }()

	if _, err := Latest(); err == nil {
		t.Error("Latest() = nil error with no semver-tagged results, want an error")
	}
}
