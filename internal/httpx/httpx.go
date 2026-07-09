// Package httpx provides the shared HTTP client this project uses for the
// small number of host-side fetches (manifest downloads, release/tag
// lookups). A single package keeps the timeout policy in one place instead
// of every call site defaulting to http.Get's unbounded http.DefaultClient.
package httpx

import (
	"net/http"
	"time"
)

// Client is used for API calls and Kubernetes manifest fetches -- all small
// payloads. 30s is generous for those, while still bounding how long a hung
// connection can block the CLI.
var Client = &http.Client{Timeout: 30 * time.Second}

// DownloadClient is used for release-archive downloads (internal/selfupdate),
// which are tens of MB and can legitimately take longer than a manifest
// fetch on a slow connection.
var DownloadClient = &http.Client{Timeout: 5 * time.Minute}
