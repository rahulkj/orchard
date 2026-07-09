// Package k8s turns apple/container VMs into a kubeadm Kubernetes cluster:
// one VM per node, kindnet for pod networking, the node image's bundled
// local-path storage class, and metrics-server on by default.
package k8s

import (
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rahulkj/orchard/internal/containerrt"
	"github.com/rahulkj/orchard/internal/state"
)

const metricsServerManifestURL = "https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml"

// DefaultImage is a kindest/node build pinned by digest, already verified to
// boot and reach Ready under apple/container on this host.
const DefaultImage = "docker.io/kindest/node@sha256:3489c7674813ba5d8b1a9977baea8a6e553784dab7b84759d1014dbd78f7ebd5"

const adminConf = "/etc/kubernetes/admin.conf"
const podCIDR = "10.244.0.0/16"

// CreateConfig describes a cluster to create.
type CreateConfig struct {
	Name         string
	Workers      int
	Image        string
	CPCPUs       string // control-plane CPU allocation
	CPMemory     string // control-plane memory; etcd+apiserver need headroom
	WorkerCPUs   string
	WorkerMem    string
	CNI          string // kindnet (default), flannel, or calico
	NoMetrics    bool
	NoStorage    bool
	Headlamp     bool
	ProxyForward bool
	WaitTimeout  time.Duration
}

func (c *CreateConfig) applyDefaults() {
	if c.Image == "" {
		c.Image = DefaultImage
	}
	if c.CPCPUs == "" {
		c.CPCPUs = "4"
	}
	if c.CPMemory == "" {
		c.CPMemory = "4096M"
	}
	if c.WorkerCPUs == "" {
		c.WorkerCPUs = "4"
	}
	if c.WorkerMem == "" {
		c.WorkerMem = "2048M"
	}
	if c.CNI == "" {
		c.CNI = "kindnet"
	}
	if c.WaitTimeout == 0 {
		c.WaitTimeout = 180 * time.Second
	}
}

func (c *CreateConfig) toState() state.Cluster {
	return state.Cluster{
		Name: c.Name, Image: c.Image, CPCPUs: c.CPCPUs, CPMemory: c.CPMemory,
		WorkerCPUs: c.WorkerCPUs, WorkerMem: c.WorkerMem, CNI: c.CNI,
		NoMetrics: c.NoMetrics, NoStorage: c.NoStorage, Headlamp: c.Headlamp,
		ProxyForward: c.ProxyForward,
	}
}

// Manager orchestrates cluster lifecycle on top of apple/container.
type Manager struct {
	rt *containerrt.Client
}

func NewManager() *Manager { return &Manager{rt: &containerrt.Client{}} }

// step prints a progress line, runs fn, and reports failure inline.
func step(label string, fn func() error) error {
	fmt.Printf("- %s ... ", label)
	if err := fn(); err != nil {
		fmt.Println("FAILED")
		return fmt.Errorf("%s: %w", label, err)
	}
	fmt.Println("done")
	return nil
}

// Preflight validates the host before any VM boots.
func (m *Manager) Preflight() error {
	if !containerrt.Available() {
		return fmt.Errorf("apple/container CLI not found on PATH; see https://github.com/apple/container")
	}
	if !containerrt.SystemRunning() {
		if err := containerrt.SystemStart(); err != nil {
			return fmt.Errorf("container system service is not running and could not be started: %w", err)
		}
	}
	return nil
}

// Create boots the node VMs and brings up Kubernetes.
func (m *Manager) Create(cfg CreateConfig) error {
	cfg.applyDefaults()
	start := time.Now()

	if !ValidName(cfg.Name) {
		return fmt.Errorf("invalid cluster name %q: use lowercase letters, digits, and dashes", cfg.Name)
	}
	if err := CheckNameLength(cfg.Name); err != nil {
		return err
	}
	if !validCNI(cfg.CNI) {
		return fmt.Errorf("unknown --cni %q (supported: %v)", cfg.CNI, ValidCNIs)
	}
	if cfg.CNI == "flannel" {
		fmt.Println("note: flannel is confirmed broken on the stock kindest/node image: its config delegates pod interface setup to the standard CNI \"bridge\" plugin, which the image does not ship (only kindnet's plugins are present). Pods will sit in ContainerCreating with \"failed to find plugin \\\"bridge\\\"\". Use kindnet (default) unless you know your node image ships /opt/cni/bin/bridge.")
	} else if cfg.CNI == "calico" {
		fmt.Println("note: calico is untested here. Its CNI plugin wires pod interfaces itself (unlike flannel, it does not delegate to the standard \"bridge\" plugin), so it may not hit the same failure flannel does, but it hasn't been verified against the default apple/container kernel/node image.")
	}
	cp := ControlPlane(cfg.Name)

	existing, err := containerrt.List(prefix(cfg.Name))
	if err == nil && len(existing) > 0 {
		return fmt.Errorf("cluster %q already exists; delete it first with: orchard delete --name %s", cfg.Name, cfg.Name)
	}

	if err := step("preflight checks", m.Preflight); err != nil {
		return err
	}

	if err := step(fmt.Sprintf("pulling node image %s", shortImage(cfg.Image)), func() error {
		return containerrt.ImagePull(cfg.Image)
	}); err != nil {
		return err
	}

	nodes := []string{cp}
	for i := 1; i <= cfg.Workers; i++ {
		nodes = append(nodes, Worker(cfg.Name, i))
	}

	if err := step(fmt.Sprintf("booting %d node VM(s)", len(nodes)), func() error {
		for _, n := range nodes {
			spec := containerrt.NodeSpec{Name: n, Image: cfg.Image, CPUs: cfg.WorkerCPUs, Memory: cfg.WorkerMem}
			if n == cp {
				spec.CPUs, spec.Memory = cfg.CPCPUs, cfg.CPMemory
			}
			if err := m.rt.RunDetached(spec); err != nil {
				return err
			}
		}
		return inParallel(len(nodes), func(i int) error {
			return m.bootstrapNode(nodes[i], cfg.WaitTimeout, cfg.ProxyForward)
		})
	}); err != nil {
		m.cleanup(cfg.Name)
		return err
	}

	if err := step("initializing Kubernetes control plane", func() error {
		version, err := m.nodeKubernetesVersion(cp)
		if err != nil {
			return err
		}
		_, err = m.rt.Exec(cp, "kubeadm", "init",
			"--pod-network-cidr="+podCIDR,
			"--node-name", cp,
			"--kubernetes-version="+version,
			"--ignore-preflight-errors=all")
		return err
	}); err != nil {
		m.cleanup(cfg.Name)
		return err
	}

	if cfg.Workers > 0 {
		if err := step(fmt.Sprintf("joining %d worker(s)", cfg.Workers), func() error {
			return m.joinWorkers(cfg.Name, cp, indexRange(1, cfg.Workers))
		}); err != nil {
			m.cleanup(cfg.Name)
			return err
		}
	}

	if err := step(fmt.Sprintf("installing CNI (%s)", cfg.CNI), func() error { return m.installCNI(cp, cfg.CNI) }); err != nil {
		m.cleanup(cfg.Name)
		return err
	}

	if cfg.Workers == 0 {
		if err := step("untainting control plane for workloads", func() error {
			_, err := m.rt.Exec(cp, "kubectl", "--kubeconfig", adminConf,
				"taint", "nodes", "--all", "node-role.kubernetes.io/control-plane-")
			return err
		}); err != nil {
			m.cleanup(cfg.Name)
			return err
		}
	}

	if !cfg.NoStorage {
		if err := step("installing default storage class", func() error {
			_, err := m.rt.Exec(cp, "kubectl", "--kubeconfig", adminConf,
				"apply", "-f", "/kind/manifests/default-storage.yaml")
			return err
		}); err != nil {
			m.cleanup(cfg.Name)
			return err
		}
	}

	if !cfg.NoMetrics {
		if err := step("installing metrics-server", func() error { return m.installMetricsServer(cp) }); err != nil {
			// metrics-server is nice-to-have; don't tear down a healthy cluster for it.
			fmt.Fprintf(os.Stderr, "  warning: metrics-server install failed: %v\n", err)
		}
	}

	if cfg.Headlamp {
		if err := step("installing Headlamp", func() error { return m.installHeadlamp(cp) }); err != nil {
			fmt.Fprintf(os.Stderr, "  warning: Headlamp install failed: %v\n", err)
		}
	}

	if err := step("waiting for nodes to be Ready", func() error {
		_, err := m.rt.Exec(cp, "kubectl", "--kubeconfig", adminConf,
			"wait", "--for=condition=Ready", "nodes", "--all",
			fmt.Sprintf("--timeout=%ds", int(cfg.WaitTimeout.Seconds())))
		return err
	}); err != nil {
		m.cleanup(cfg.Name)
		return err
	}

	var kubeconfigPath string
	if err := step("writing kubeconfig", func() error {
		raw, err := m.rt.Exec(cp, "cat", adminConf)
		if err != nil {
			return err
		}
		ip, err := m.rt.IP(cp)
		if err != nil {
			return err
		}
		kubeconfigPath, err = MergeKubeconfig(cfg.Name, raw, ip)
		return err
	}); err != nil {
		return err
	}

	if err := state.Save(cfg.toState()); err != nil {
		fmt.Fprintf(os.Stderr, "  warning: could not persist cluster state: %v\n", err)
	}

	fmt.Printf("\nCluster %q is ready in %s. Every node is its own apple/container VM.\n", cfg.Name, time.Since(start).Round(time.Second))
	fmt.Printf("context orchard-%s merged into %s\n", cfg.Name, kubeconfigPath)
	fmt.Println("  kubectl get nodes")
	if cfg.Headlamp {
		fmt.Println("  orchard headlamp token --name " + cfg.Name + "   # then: kubectl port-forward -n kube-system service/headlamp 8080:80")
	}
	return nil
}

// nodeKubernetesVersion reads the Kubernetes version baked into a node
// image. kubeadm init defaults to the newest upstream stable release when
// --kubernetes-version is omitted, which needs internet access to resolve
// and, worse, may not match the images already pre-loaded in the node
// image -- forcing a network image pull that fails here (node VMs have no
// general internet egress; see installMetricsServer). Pinning to the
// image's own baked version keeps init entirely offline.
func (m *Manager) nodeKubernetesVersion(node string) (string, error) {
	out, err := m.rt.Exec(node, "cat", "/kind/version")
	if err != nil {
		return "", fmt.Errorf("reading /kind/version from %s: %w", node, err)
	}
	v := strings.TrimSpace(out)
	if v == "" {
		return "", fmt.Errorf("empty /kind/version on %s", node)
	}
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	return v, nil
}

// bootstrapNode waits for a fresh node VM to answer, enables IP forwarding
// (which kubeadm and the CNI both require), and optionally forwards the
// host's proxy/trust settings before anything else touches the network.
func (m *Manager) bootstrapNode(node string, timeout time.Duration, proxyForward bool) error {
	if err := m.rt.WaitReady(node, timeout); err != nil {
		return err
	}
	if _, err := m.rt.Exec(node, "sysctl", "-w", "net.ipv4.ip_forward=1"); err != nil {
		return err
	}
	if proxyForward {
		if err := m.applyProxyForward(node); err != nil {
			return fmt.Errorf("proxy-forward on %s: %w", node, err)
		}
	}
	return nil
}

// joinWorkers fetches one kubeadm join command from the control plane and
// runs it concurrently against each worker index in indices.
func (m *Manager) joinWorkers(cluster, cp string, indices []int) error {
	joinOut, err := m.rt.Exec(cp, "kubeadm", "token", "create", "--print-join-command")
	if err != nil {
		return err
	}
	join := strings.Fields(lastJoinLine(joinOut))
	if len(join) == 0 || join[0] != "kubeadm" {
		return fmt.Errorf("unexpected kubeadm join command: %q", joinOut)
	}
	join = append(join, "--ignore-preflight-errors=all")

	return inParallel(len(indices), func(i int) error {
		w := Worker(cluster, indices[i])
		args := append(append([]string{}, join...), "--node-name", w)
		_, err := m.rt.Exec(w, args...)
		return err
	})
}

// installMetricsServer applies the upstream manifest and patches in
// --kubelet-insecure-tls, required because node kubelets here (as in kind)
// present certificates metrics-server's default CA trust won't validate.
//
// Node VMs have no general internet egress -- apple/container pulls images
// host-side, so kindest/node ships its core images pre-loaded and never
// needs the guest to reach the network itself. A manifest URL is different:
// kubectl fetches it from inside the guest, which fails. So this fetches
// the manifest on the host (which does have internet) and pipes it in.
func (m *Manager) installMetricsServer(cp string) error {
	resp, err := http.Get(metricsServerManifestURL)
	if err != nil {
		return fmt.Errorf("fetching metrics-server manifest: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("fetching metrics-server manifest: HTTP %d", resp.StatusCode)
	}
	if err := m.rt.ExecStdin(cp, resp.Body, "kubectl", "--kubeconfig", adminConf, "apply", "-f", "-"); err != nil {
		return err
	}
	_, err = m.rt.Exec(cp, "kubectl", "--kubeconfig", adminConf,
		"patch", "deployment", "metrics-server", "-n", "kube-system", "--type=json",
		`-p=[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--kubelet-insecure-tls"}]`)
	return err
}

// cleanup tears down partially-created node VMs so a failed Create never
// leaves the host dirty. Errors are deliberately ignored.
func (m *Manager) cleanup(cluster string) {
	nodes, err := containerrt.List(prefix(cluster))
	if err != nil {
		return
	}
	names := make([]string, len(nodes))
	for i, n := range nodes {
		names[i] = n.Name
	}
	_ = containerrt.Remove(names...)
	if len(names) > 0 {
		fmt.Fprintf(os.Stderr, "cleaned up partial cluster %q\n", cluster)
	}
}

// Delete removes every node VM for a cluster and its kubeconfig entries.
func (m *Manager) Delete(cluster string) error {
	nodes, err := containerrt.List(prefix(cluster))
	if err != nil {
		return err
	}
	if len(nodes) == 0 {
		return fmt.Errorf("no cluster named %q found", cluster)
	}
	names := make([]string, len(nodes))
	for i, n := range nodes {
		names[i] = n.Name
	}
	if err := step(fmt.Sprintf("deleting %d node VM(s)", len(names)), func() error {
		return containerrt.Remove(names...)
	}); err != nil {
		return err
	}
	if err := step("removing kubeconfig entries", func() error {
		return RemoveKubeconfig(cluster)
	}); err != nil {
		return err
	}
	if err := state.Delete(cluster); err != nil {
		fmt.Fprintf(os.Stderr, "  warning: could not remove persisted cluster state: %v\n", err)
	}
	fmt.Printf("cluster %q deleted.\n", cluster)
	return nil
}

// Clusters lists cluster names derived from running orchard- containers.
func Clusters() ([]string, error) {
	nodes, err := containerrt.List("orchard-")
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, n := range nodes {
		rest := strings.TrimPrefix(n.Name, "orchard-")
		var name string
		if idx := strings.LastIndex(rest, "-control-plane"); idx >= 0 {
			name = rest[:idx]
		} else if idx := strings.LastIndex(rest, "-worker-"); idx >= 0 {
			name = rest[:idx]
		} else {
			continue
		}
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out, nil
}

// Nodes lists node containers belonging to one cluster.
func Nodes(cluster string) ([]containerrt.Node, error) {
	return containerrt.List(prefix(cluster))
}

func inParallel(n int, fn func(i int) error) error {
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = fn(i)
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func indexRange(from, to int) []int {
	out := make([]int, 0, to-from+1)
	for i := from; i <= to; i++ {
		out = append(out, i)
	}
	return out
}

func shortImage(image string) string {
	s := image
	if idx := strings.Index(s, "@"); idx >= 0 {
		s = s[:idx]
	}
	return strings.TrimPrefix(s, "docker.io/")
}

func lastJoinLine(s string) string {
	for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
		if l = strings.TrimSpace(l); strings.Contains(l, "kubeadm join") {
			return l
		}
	}
	return strings.TrimSpace(s)
}
