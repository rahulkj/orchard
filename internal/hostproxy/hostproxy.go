// Package hostproxy detects the Mac's proxy configuration and trusted
// certificates so orchard can forward them into node VMs. Node VMs get
// their network from apple/container's NAT and inherit whatever the host's
// network path does to outbound traffic -- including a transparent
// TLS-intercepting security agent, which is common on managed corporate
// Macs and is a different problem than an explicit HTTP_PROXY: the guest
// needs the interception root CA trusted, not a proxy pointed anywhere.
package hostproxy

import (
	"fmt"
	"os"
	"os/exec"
)

// Settings is what was found on the host.
type Settings struct {
	HTTPProxy  string
	HTTPSProxy string
	NoProxy    string
}

// Detect reads the standard proxy environment variables. macOS's
// `scutil --proxy` output is deliberately not consulted: on managed Macs it
// commonly reports a PAC script served from a loopback port bound by a
// local security agent, which a guest VM cannot reach and which can't be
// evaluated without a JS engine. HTTP_PROXY/HTTPS_PROXY/NO_PROXY are the
// portable mechanism this package forwards.
func Detect() Settings {
	return Settings{
		HTTPProxy:  firstEnv("HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"),
		HTTPSProxy: firstEnv("HTTPS_PROXY", "https_proxy"),
		NoProxy:    firstEnv("NO_PROXY", "no_proxy"),
	}
}

// HasExplicitProxy reports whether an HTTP(S)_PROXY env var was found.
func (s Settings) HasExplicitProxy() bool {
	return s.HTTPProxy != "" || s.HTTPSProxy != ""
}

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

// ExportTrustedCAs exports certificates from the macOS login/System
// keychain as a PEM bundle. It deliberately does not export
// SystemRootCertificates.keychain (Apple's ~150 default roots): those are
// already in every Linux distro's default trust bundle. System.keychain
// is where MDM profiles and corporate security agents install their own
// root CAs, which is the one a guest VM actually needs to gain.
func ExportTrustedCAs() ([]byte, error) {
	out, err := exec.Command("security", "find-certificate", "-a", "-p", "/Library/Keychains/System.keychain").Output()
	if err != nil {
		return nil, fmt.Errorf("exporting certificates from System.keychain: %w", err)
	}
	return out, nil
}
