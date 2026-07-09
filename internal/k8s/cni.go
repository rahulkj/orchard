package k8s

import (
	"fmt"

	"github.com/rahulkj/orchard/internal/httpx"
)

const (
	flannelManifestURL = "https://github.com/flannel-io/flannel/releases/latest/download/kube-flannel.yml"
	calicoManifestURL  = "https://raw.githubusercontent.com/projectcalico/calico/master/manifests/calico.yaml"
)

// ValidCNIs are the --cni values Create/Scale accept.
var ValidCNIs = []string{"kindnet", "flannel", "calico"}

func validCNI(cni string) bool {
	for _, v := range ValidCNIs {
		if v == cni {
			return true
		}
	}
	return false
}

// installCNI applies the pod network. kindnet ships inside the node image
// and needs no internet access from the guest; flannel and calico are
// fetched on the host (which has internet) and piped into the guest, the
// same workaround installMetricsServer uses.
func (m *Manager) installCNI(cp, cni string) error {
	switch cni {
	case "", "kindnet":
		_, err := m.rt.Exec(cp, "sh", "-euc",
			fmt.Sprintf(`sed -e 's@{{ .PodSubnet }}@%s@' /kind/manifests/default-cni.yaml | kubectl --kubeconfig %s apply -f -`, podCIDR, adminConf))
		return err
	case "flannel":
		return m.applyManifestFromHost(cp, flannelManifestURL)
	case "calico":
		return m.applyManifestFromHost(cp, calicoManifestURL)
	default:
		return fmt.Errorf("unknown --cni %q (supported: %v)", cni, ValidCNIs)
	}
}

// applyManifestFromHost fetches a manifest URL on the host and pipes it
// into the guest's kubectl. See installMetricsServer for why: node VMs
// have no general internet egress, so kubectl inside the guest can't fetch
// a URL itself.
func (m *Manager) applyManifestFromHost(cp, url string) error {
	resp, err := httpx.Client.Get(url)
	if err != nil {
		return fmt.Errorf("fetching %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		return fmt.Errorf("fetching %s: HTTP %d", url, resp.StatusCode)
	}
	return m.rt.ExecStdin(cp, resp.Body, "kubectl", "--kubeconfig", adminConf, "apply", "-f", "-")
}
