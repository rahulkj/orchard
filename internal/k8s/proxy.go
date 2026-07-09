package k8s

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/rahulkj/orchard/internal/hostproxy"
)

// applyProxyForward gives a node VM the host's network trust and, if set,
// its proxy. Two independent things happen, because they fix two different
// problems (see hostproxy package docs):
//
//  1. Host-trusted root CAs (System.keychain) are installed into the guest
//     and update-ca-certificates is re-run. This is done unconditionally
//     when --proxy-forward is set, even with no HTTP_PROXY, because a
//     transparent TLS-intercepting security agent needs no proxy
//     configuration to affect guest traffic -- only a trusted cert.
//  2. If HTTP_PROXY/HTTPS_PROXY are set on the host, they're forwarded into
//     the guest's /etc/environment and containerd's systemd unit. A
//     loopback address in the proxy URL is rewritten to the guest's
//     default gateway (the host, from the guest's point of view) since
//     127.0.0.1 inside the guest is the guest itself.
func (m *Manager) applyProxyForward(node string) error {
	if certs, err := hostproxy.ExportTrustedCAs(); err != nil {
		fmt.Printf("  warning: could not export host trusted CAs: %v\n", err)
	} else if len(certs) > 0 {
		if err := m.rt.ExecStdin(node, bytes.NewReader(certs), "tee", "/usr/local/share/ca-certificates/host-proxy-cas.crt"); err != nil {
			return fmt.Errorf("installing host trusted CAs: %w", err)
		}
		if _, err := m.rt.Exec(node, "update-ca-certificates"); err != nil {
			return fmt.Errorf("update-ca-certificates: %w", err)
		}
	}

	s := hostproxy.Detect()
	if !s.HasExplicitProxy() {
		return nil
	}

	gateway, err := m.defaultGateway(node)
	if err != nil {
		return fmt.Errorf("determining default gateway for proxy rewrite: %w", err)
	}
	rewrite := strings.NewReplacer("127.0.0.1", gateway, "localhost", gateway).Replace
	httpProxy, httpsProxy, noProxy := rewrite(s.HTTPProxy), rewrite(s.HTTPSProxy), s.NoProxy

	env := fmt.Sprintf(
		"HTTP_PROXY=%s\nHTTPS_PROXY=%s\nNO_PROXY=%s\nhttp_proxy=%s\nhttps_proxy=%s\nno_proxy=%s\n",
		httpProxy, httpsProxy, noProxy, httpProxy, httpsProxy, noProxy)
	if err := m.rt.ExecStdin(node, strings.NewReader(env), "tee", "-a", "/etc/environment"); err != nil {
		return fmt.Errorf("writing /etc/environment: %w", err)
	}

	dropin := fmt.Sprintf(
		"[Service]\nEnvironment=\"HTTP_PROXY=%s\"\nEnvironment=\"HTTPS_PROXY=%s\"\nEnvironment=\"NO_PROXY=%s\"\n",
		httpProxy, httpsProxy, noProxy)
	if _, err := m.rt.Exec(node, "mkdir", "-p", "/etc/systemd/system/containerd.service.d"); err != nil {
		return err
	}
	if err := m.rt.ExecStdin(node, strings.NewReader(dropin), "tee", "/etc/systemd/system/containerd.service.d/http-proxy.conf"); err != nil {
		return fmt.Errorf("writing containerd proxy drop-in: %w", err)
	}
	if _, err := m.rt.Exec(node, "systemctl", "daemon-reload"); err != nil {
		return err
	}
	if _, err := m.rt.Exec(node, "systemctl", "restart", "containerd"); err != nil {
		return err
	}
	return nil
}

func (m *Manager) defaultGateway(node string) (string, error) {
	out, err := m.rt.Exec(node, "sh", "-c", `ip route show default | awk '/default/ {print $3; exit}'`)
	if err != nil {
		return "", err
	}
	gw := strings.TrimSpace(out)
	if gw == "" {
		return "", fmt.Errorf("no default route found on %s", node)
	}
	return gw, nil
}
