package k8s

import (
	"fmt"

	"github.com/rahulkj/orchard/internal/containerrt"
	"github.com/rahulkj/orchard/internal/nodeimage"
	"github.com/rahulkj/orchard/internal/state"
)

// CurrentVersion reports the Kubernetes version a cluster's control plane
// is running, read from the node image's own version marker.
func (m *Manager) CurrentVersion(cluster string) (string, error) {
	return m.nodeKubernetesVersion(ControlPlane(cluster))
}

// LatestAvailable reports the newest kindest/node release found on Docker
// Hub, regardless of what any local cluster is running.
func LatestAvailable() (nodeimage.Release, error) {
	return nodeimage.Latest()
}

// UpgradeResult summarizes what Upgrade did.
type UpgradeResult struct {
	FromVersion string
	ToVersion   string
	Image       string
	Changed     bool // false if the cluster was already on the target image
}

// Upgrade replaces a cluster's node VMs with a newer node image.
//
// This is a destroy-and-recreate, not an in-place kubeadm upgrade: kubeadm
// only supports upgrading one minor version at a time using kubeadm/kubelet
// binaries already present on the node, and these node images are immutable,
// single-version builds with no general internet egress to fetch different
// ones (see nodeKubernetesVersion). Upstream kind has the same limitation
// and takes the same approach -- there is no "kind upgrade cluster". This
// recreates the cluster with the same settings it was created with (from
// persisted state) but a newer image. Workloads are not preserved.
func (m *Manager) Upgrade(name, image string) (UpgradeResult, error) {
	st, err := state.Load(name)
	if err != nil {
		return UpgradeResult{}, fmt.Errorf("no saved configuration for cluster %q (created before state tracking existed, or its state file was removed) -- recreate it with `orchard create` instead: %w", name, err)
	}

	from, err := m.CurrentVersion(name)
	if err != nil {
		return UpgradeResult{}, fmt.Errorf("reading current cluster version: %w", err)
	}

	to := image
	if to == "" {
		rel, err := nodeimage.Latest()
		if err != nil {
			return UpgradeResult{}, err
		}
		image, to = rel.Image, rel.Version
	}

	if image == st.Image {
		return UpgradeResult{FromVersion: from, ToVersion: to, Image: image, Changed: false}, nil
	}

	nodes, err := containerrt.List(prefix(name))
	if err != nil {
		return UpgradeResult{}, err
	}
	workers := 0
	for _, n := range nodes {
		if _, ok := WorkerIndex(name, n.Name); ok {
			workers++
		}
	}

	cfg := CreateConfig{
		Name: name, Workers: workers, Image: image,
		CPCPUs: st.CPCPUs, CPMemory: st.CPMemory,
		WorkerCPUs: st.WorkerCPUs, WorkerMem: st.WorkerMem,
		CNI: st.CNI, NoMetrics: st.NoMetrics, NoStorage: st.NoStorage,
		Headlamp: st.Headlamp, ProxyForward: st.ProxyForward,
	}

	if err := m.Delete(name); err != nil {
		return UpgradeResult{}, fmt.Errorf("deleting old cluster: %w", err)
	}
	if err := m.Create(cfg); err != nil {
		return UpgradeResult{}, fmt.Errorf("creating upgraded cluster (old cluster is already gone): %w", err)
	}
	return UpgradeResult{FromVersion: from, ToVersion: to, Image: image, Changed: true}, nil
}
