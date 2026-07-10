// Package k8s turns apple/container VMs into a kubeadm Kubernetes cluster:
// one VM per node, kindnet for pod networking, the node image's bundled
// local-path storage class, and metrics-server on by default.
package k8s

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rahulkj/orchard/internal/containerrt"
	"github.com/rahulkj/orchard/internal/httpx"
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
	if cfg.Workers < 0 {
		return fmt.Errorf("worker count cannot be negative")
	}
	if !validCNI(cfg.CNI) {
		return fmt.Errorf("unknown --cni %q (supported: %v)", cfg.CNI, ValidCNIs)
	}
	switch cfg.CNI {
	case "flannel":
		fmt.Println("note: flannel is confirmed broken on the stock kindest/node image: its config delegates pod interface setup to the standard CNI \"bridge\" plugin, which the image does not ship (only kindnet's plugins are present). Pods will sit in ContainerCreating with \"failed to find plugin \\\"bridge\\\"\". Use kindnet (default) unless you know your node image ships /opt/cni/bin/bridge.")
	case "calico":
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
// metricsServerPatch adds --kubelet-insecure-tls to the metrics-server
// container args. Kept as a constant so the manual recovery command in the
// error message below can never drift from what this actually runs.
const metricsServerPatch = `[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--kubelet-insecure-tls"}]`

func (m *Manager) installMetricsServer(cp string) error {
	resp, err := httpx.Client.Get(metricsServerManifestURL)
	if err != nil {
		return fmt.Errorf("fetching metrics-server manifest: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		return fmt.Errorf("fetching metrics-server manifest: HTTP %d", resp.StatusCode)
	}
	if err := m.rt.ExecStdin(cp, resp.Body, "kubectl", "--kubeconfig", adminConf, "apply", "-f", "-"); err != nil {
		return fmt.Errorf("applying metrics-server manifest: %w", err)
	}

	// The patch is a separate API call right after apply and has been
	// observed to fail transiently (e.g. apiserver momentarily unready)
	// even though the apply immediately before it succeeded. Unlike apply,
	// there's no visible symptom of a failed patch until metrics-server's
	// readiness probe starts looping forever against kubelet certs it can't
	// validate, so this is worth a few retries before giving up.
	const attempts = 3
	for attempt := 1; ; attempt++ {
		_, err = m.rt.Exec(cp, "kubectl", "--kubeconfig", adminConf,
			"patch", "deployment", "metrics-server", "-n", "kube-system", "--type=json",
			"-p="+metricsServerPatch)
		if err == nil {
			return nil
		}
		if attempt == attempts {
			return fmt.Errorf(
				"patching metrics-server for --kubelet-insecure-tls after %d attempts (required: node kubelet serving certs here have no IP SANs, so metrics-server can't validate them without this flag): %w\n"+
					"      fix manually with: kubectl --kubeconfig %s patch deployment metrics-server -n kube-system --type=json -p='%s'",
				attempts, err, adminConf, metricsServerPatch)
		}
		time.Sleep(3 * time.Second)
	}
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

// Stop stops every node VM for a cluster without deleting them or their
// saved state, so a later Start resumes the same cluster. Workers stop
// first so their kubelets never have to notice the control plane vanish
// out from under them mid-shutdown.
func (m *Manager) Stop(cluster string) error {
	cp, workers, err := m.clusterNodes(cluster)
	if err != nil {
		return err
	}
	if len(workers) > 0 {
		if err := step(fmt.Sprintf("stopping %d worker node VM(s)", len(workers)), func() error {
			return containerrt.Stop(workers...)
		}); err != nil {
			return err
		}
	}
	if err := step("stopping control-plane node VM", func() error {
		return containerrt.Stop(cp)
	}); err != nil {
		return err
	}
	fmt.Printf("cluster %q stopped. resume it with: orchard start --name %s\n", cluster, cluster)
	return nil
}

// Start boots every node VM for a cluster back up after Stop. The control
// plane starts first and must be ready before workers start, mirroring the
// dependency workers already have on it at join time.
func (m *Manager) Start(cluster string) error {
	cp, workers, err := m.clusterNodes(cluster)
	if err != nil {
		return err
	}
	if err := step("starting control-plane node VM", func() error {
		return m.startNode(cp)
	}); err != nil {
		return err
	}

	var cpIP string
	if err := step("repairing API server certificate for current IP", func() error {
		cpIP, err = m.repairAPIServerCert(cp)
		return err
	}); err != nil {
		return err
	}

	if err := step("waiting for the API server to accept connections", func() error {
		return m.waitForAPIServer(cp, 60*time.Second)
	}); err != nil {
		return err
	}

	if len(workers) > 0 {
		if err := step(fmt.Sprintf("starting %d worker node VM(s)", len(workers)), func() error {
			return inParallel(len(workers), func(i int) error {
				if err := m.startNode(workers[i]); err != nil {
					return err
				}
				return m.repointWorkerKubelet(workers[i], cpIP)
			})
		}); err != nil {
			return err
		}
	}

	if err := step("waiting for nodes to be Ready", func() error {
		_, err := m.rt.Exec(cp, "kubectl", "--kubeconfig", adminConf,
			"wait", "--for=condition=Ready", "nodes", "--all", "--timeout=120s")
		return err
	}); err != nil {
		return err
	}

	if err := step("repairing kube-proxy and CoreDNS for current IP", func() error {
		if err := m.repairKubeProxyConfig(cp, cpIP); err != nil {
			return err
		}
		return m.restartCoreDNS(cp)
	}); err != nil {
		return err
	}

	// The control plane's DHCP-assigned IP can change across a stop/start
	// cycle (apple/container VMs re-lease on boot), so the kubeconfig
	// server address written at create time can go stale here even though
	// nothing about the cluster itself changed.
	var kubeconfigPath string
	if err := step("refreshing kubeconfig", func() error {
		raw, err := m.rt.Exec(cp, "cat", adminConf)
		if err != nil {
			return err
		}
		ip, err := m.rt.IP(cp)
		if err != nil {
			return err
		}
		kubeconfigPath, err = MergeKubeconfig(cluster, raw, ip)
		return err
	}); err != nil {
		return err
	}

	fmt.Printf("cluster %q started.\n", cluster)
	fmt.Printf("context orchard-%s refreshed in %s\n", cluster, kubeconfigPath)
	fmt.Println("  kubectl get nodes")
	return nil
}

// startNode starts a stopped node VM and waits for it to come up, retrying
// the boot itself (not just the readiness poll) a few times. The
// kindest/node image's entrypoint runs with `set -o errexit`, and its own
// IP-fixup logic (see repairAPIServerCert) reliably fails one of its steps
// on the DHCP lease change a restart usually brings -- which kills the
// entrypoint and leaves the whole VM back in the stopped state before
// systemd ever starts, rather than merely delaying it. A bare retry of
// `container start` has reliably recovered from this in testing, so retry
// here instead of surfacing the flake to the caller.
func (m *Manager) startNode(name string) error {
	const attempts = 3
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if err := containerrt.Start(name); err != nil {
			lastErr = err
			continue
		}
		if err := m.rt.WaitReady(name, 60*time.Second); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	return fmt.Errorf("node %s did not come up after %d attempts: %w", name, attempts, lastErr)
}

// waitForAPIServer polls until the API server answers on the control
// plane's admin.conf. Regenerating the serving certificate (see
// repairAPIServerCert) only replaces the file on disk; kube-apiserver keeps
// crash-looping against the old (now-replaced) cert for however long it
// takes kubelet's backoff to cycle it again, so callers that need the API
// server up -- kube-proxy/CoreDNS repair, "wait for nodes Ready" -- must
// wait for it explicitly rather than assuming the cert fix alone is enough.
func (m *Manager) waitForAPIServer(cp string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		if _, err := m.rt.Exec(cp, "kubectl", "--kubeconfig", adminConf, "get", "--raw", "/healthz"); err == nil {
			return nil
		} else {
			lastErr = err
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("API server on %s never became reachable: %w", cp, lastErr)
}

// repairAPIServerCert regenerates the API server's serving certificate for
// the control plane's current IP and repoints admin.conf/super-admin.conf
// at it. Node VMs get a fresh DHCP lease on every boot, and the
// kindest/node image's own boot-time IP-fixup script deletes the old
// apiserver cert whenever the IP changed but can't regenerate it: its
// regeneration path shells out to `kubeadm init phase certs apiserver
// --config /kind/kubeadm.conf`, a file only kind's own cluster tooling
// writes -- orchard runs kubeadm init with flags, not --config, so that
// file never exists here. Left alone, kube-apiserver crash-loops forever on
// the missing cert file. That same fixup script also rewrites the server
// address in controller-manager.conf/scheduler.conf/kubelet.conf (so the
// control plane's own components can reach it) but deliberately leaves
// admin.conf/super-admin.conf alone, so every in-guest `kubectl --kubeconfig
// admin.conf` call this package makes elsewhere would otherwise keep
// dialing a dead IP forever after the first restart. Both the cert phase
// and the sed are no-ops if nothing actually changed, so running this
// unconditionally on every Start is safe. Returns the control plane's
// current IP so the caller can also repoint workers at it (see
// repointWorkerKubelet).
func (m *Manager) repairAPIServerCert(cp string) (string, error) {
	ip, err := m.rt.IP(cp)
	if err != nil {
		return "", err
	}
	version, err := m.nodeKubernetesVersion(cp)
	if err != nil {
		return "", err
	}
	if _, err := m.rt.Exec(cp, "kubeadm", "init", "phase", "certs", "apiserver",
		"--apiserver-advertise-address="+ip,
		"--apiserver-cert-extra-sans="+ip,
		"--kubernetes-version="+version); err != nil {
		return "", err
	}
	sed := fmt.Sprintf(
		`for f in %s /etc/kubernetes/super-admin.conf; do [ -f "$f" ] && sed -i -E 's#server: https://[0-9.]+:6443#server: https://%s:6443#' "$f"; done`,
		adminConf, ip)
	if _, err := m.rt.Exec(cp, "sh", "-c", sed); err != nil {
		return "", err
	}
	return ip, nil
}

// repointWorkerKubelet rewrites a worker's kubelet.conf to dial the control
// plane's current IP and restarts kubelet to pick it up. kind's own
// boot-time IP-fixup script only rewrites a node's references to its *own*
// address; a worker's kubelet.conf points at the control plane's address,
// which from the worker's perspective never changes as far as that script
// can tell, so it's left holding whatever IP was current back when the
// worker first joined -- forever, across every future restart where the
// control plane's IP moves. Left alone, the worker's kubelet can never
// register and the node sits NotReady indefinitely.
func (m *Manager) repointWorkerKubelet(worker, cpIP string) error {
	sed := fmt.Sprintf(
		`sed -i -E 's#server: https://[0-9.]+:6443#server: https://%s:6443#' /etc/kubernetes/kubelet.conf && systemctl restart kubelet`,
		cpIP)
	_, err := m.rt.Exec(worker, "sh", "-c", sed)
	return err
}

// repairKubeProxyConfig repoints the cluster-wide kube-proxy ConfigMap at
// the control plane's current IP and bounces kube-proxy so it picks it up.
// Unlike the per-node repairs above, this isn't a file on any node's disk --
// it's baked into a Kubernetes object (the "kube-proxy" ConfigMap in
// kube-system) at kubeadm-init time and nothing about a node reboot rewrites
// API objects, so it's left holding the ORIGINAL cluster-creation IP
// forever. Left alone, kube-proxy can't reach the API server at all after a
// restart, so it never programs a single Service iptables rule -- not just
// for "kubernetes", for every ClusterIP in the cluster. That's what actually
// leaves CoreDNS stuck NotReady after a restart: its "kubernetes" plugin
// talks to the API through the in-cluster ClusterIP (10.96.0.1), which has
// silently had zero routing since boot. Piping the ConfigMap's full YAML
// through sed (rather than a JSON/strategic patch) sidesteps having to
// JSON-escape the multi-line kubeconfig.conf value while still preserving
// every other key untouched.
//
// Called only after every node VM is back up (Start runs this after
// starting workers, not before): kube-proxy's pods are a DaemonSet spread
// across every node, and deleting one whose node is still stopped leaves it
// wedged in Terminating forever waiting for a kubelet that isn't there to
// confirm it -- --wait=false additionally guards against that regardless of
// ordering, since kubectl would otherwise block the whole exec on that
// confirmation.
func (m *Manager) repairKubeProxyConfig(cp, ip string) error {
	script := fmt.Sprintf(
		`kubectl --kubeconfig %s get configmap kube-proxy -n kube-system -o yaml `+
			`| sed -E 's#server: https://[0-9.]+:6443#server: https://%s:6443#' `+
			`| kubectl --kubeconfig %s replace -f - `+
			`&& kubectl --kubeconfig %s delete pods -n kube-system -l k8s-app=kube-proxy --ignore-not-found --wait=false`,
		adminConf, ip, adminConf, adminConf)
	_, err := m.rt.Exec(cp, "sh", "-c", script)
	return err
}

// restartCoreDNS force-deletes CoreDNS's pods so they reconnect fresh.
// CoreDNS's "kubernetes" plugin latches onto its first (broken, pre-
// repairKubeProxyConfig) connection attempt and never recovers on its own
// even once Service routing starts working again -- verified live: after
// fixing kube-proxy alone, CoreDNS sat logging "Plugins not ready:
// kubernetes" indefinitely; only a pod restart cleared it. --wait=false for
// the same reason as repairKubeProxyConfig: don't let this block on a node
// that isn't fully up yet.
func (m *Manager) restartCoreDNS(cp string) error {
	_, err := m.rt.Exec(cp, "kubectl", "--kubeconfig", adminConf,
		"delete", "pods", "-n", "kube-system", "-l", "k8s-app=kube-dns", "--ignore-not-found", "--wait=false")
	return err
}

// clusterNodes returns a cluster's control-plane name and its worker names,
// erroring if the cluster has no node VMs at all.
func (m *Manager) clusterNodes(cluster string) (cp string, workers []string, err error) {
	nodes, err := containerrt.List(prefix(cluster))
	if err != nil {
		return "", nil, err
	}
	if len(nodes) == 0 {
		return "", nil, fmt.Errorf("no cluster named %q found", cluster)
	}
	cp = ControlPlane(cluster)
	for _, n := range nodes {
		if n.Name != cp {
			workers = append(workers, n.Name)
		}
	}
	return cp, workers, nil
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
