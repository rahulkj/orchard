package k8s

import (
	"fmt"
	"sort"
	"time"

	"github.com/rahulkj/orchard/internal/containerrt"
	"github.com/rahulkj/orchard/internal/state"
)

// ScaleConfig describes a target worker count for an existing cluster.
type ScaleConfig struct {
	Name        string
	Workers     int    // desired worker count
	Image       string // new workers only; defaults to the running control-plane's image
	WorkerCPUs  string // new workers only
	WorkerMem   string // new workers only
	WaitTimeout time.Duration
}

func (c *ScaleConfig) applyDefaults() {
	if c.WorkerCPUs == "" {
		c.WorkerCPUs = "4"
	}
	if c.WorkerMem == "" {
		c.WorkerMem = "4096M"
	}
	if c.WaitTimeout == 0 {
		c.WaitTimeout = 180 * time.Second
	}
}

// Scale adds or removes worker nodes to reach cfg.Workers.
func (m *Manager) Scale(cfg ScaleConfig) error {
	cfg.applyDefaults()
	if cfg.Workers < 0 {
		return fmt.Errorf("worker count cannot be negative")
	}

	nodes, err := containerrt.List(prefix(cfg.Name))
	if err != nil {
		return err
	}
	if len(nodes) == 0 {
		return fmt.Errorf("no cluster named %q found", cfg.Name)
	}

	cp := ControlPlane(cfg.Name)
	haveCP := false
	var existingIdx []int
	for _, n := range nodes {
		if n.Name == cp {
			haveCP = true
			if cfg.Image == "" {
				cfg.Image = n.Image
			}
		}
		if idx, ok := WorkerIndex(cfg.Name, n.Name); ok {
			existingIdx = append(existingIdx, idx)
		}
	}
	if !haveCP {
		return fmt.Errorf("control-plane node not found for cluster %q", cfg.Name)
	}
	sort.Ints(existingIdx)
	current := len(existingIdx)

	// Best-effort: reuse the cluster's original proxy-forward setting for
	// any new workers. Clusters created before state existed just get none.
	proxyForward := false
	if st, err := state.Load(cfg.Name); err == nil {
		proxyForward = st.ProxyForward
	}

	switch {
	case cfg.Workers > current:
		return m.scaleUp(cfg, cp, existingIdx, proxyForward)
	case cfg.Workers < current:
		return m.scaleDown(cfg, cp, existingIdx)
	default:
		fmt.Printf("cluster %q already has %d worker(s)\n", cfg.Name, current)
		return nil
	}
}

// scaleUp boots enough new worker VMs to go from len(existingIdx) to
// cfg.Workers, reusing the lowest free indices above the current maximum.
func (m *Manager) scaleUp(cfg ScaleConfig, cp string, existingIdx []int, proxyForward bool) error {
	next := 1
	if len(existingIdx) > 0 {
		next = existingIdx[len(existingIdx)-1] + 1
	}
	add := cfg.Workers - len(existingIdx)
	newIdx := make([]int, add)
	for i := range newIdx {
		newIdx[i] = next + i
	}

	if err := step(fmt.Sprintf("booting %d new worker VM(s)", add), func() error {
		for _, idx := range newIdx {
			w := Worker(cfg.Name, idx)
			if err := m.rt.RunDetached(containerrt.NodeSpec{
				Name: w, Image: cfg.Image, CPUs: cfg.WorkerCPUs, Memory: cfg.WorkerMem,
			}); err != nil {
				return err
			}
		}
		return inParallel(len(newIdx), func(i int) error {
			return m.bootstrapNode(Worker(cfg.Name, newIdx[i]), cfg.WaitTimeout, proxyForward)
		})
	}); err != nil {
		return err
	}

	if err := step(fmt.Sprintf("joining %d worker(s)", add), func() error {
		return m.joinWorkers(cfg.Name, cp, newIdx)
	}); err != nil {
		return err
	}

	names := make([]string, len(newIdx))
	for i, idx := range newIdx {
		names[i] = Worker(cfg.Name, idx)
	}
	if err := step("waiting for new node(s) to be Ready", func() error {
		args := append([]string{"--kubeconfig", adminConf, "wait", "--for=condition=Ready", "node"},
			append(names, fmt.Sprintf("--timeout=%ds", int(cfg.WaitTimeout.Seconds())))...)
		_, err := m.rt.Exec(cp, append([]string{"kubectl"}, args...)...)
		return err
	}); err != nil {
		return err
	}

	fmt.Printf("cluster %q scaled to %d worker(s)\n", cfg.Name, cfg.Workers)
	return nil
}

// scaleDown drains and removes the highest-indexed workers until only
// cfg.Workers remain, so lower-numbered workers stay stable across resizes.
func (m *Manager) scaleDown(cfg ScaleConfig, cp string, existingIdx []int) error {
	remove := len(existingIdx) - cfg.Workers
	toRemove := existingIdx[len(existingIdx)-remove:]

	for _, idx := range toRemove {
		w := Worker(cfg.Name, idx)
		if err := step(fmt.Sprintf("draining %s", w), func() error {
			_, err := m.rt.Exec(cp, "kubectl", "--kubeconfig", adminConf,
				"drain", w, "--ignore-daemonsets", "--delete-emptydir-data", "--force", "--timeout=60s")
			return err
		}); err != nil {
			return err
		}
		if err := step(fmt.Sprintf("removing %s from the cluster", w), func() error {
			_, err := m.rt.Exec(cp, "kubectl", "--kubeconfig", adminConf, "delete", "node", w)
			return err
		}); err != nil {
			return err
		}
		if err := step(fmt.Sprintf("deleting %s VM", w), func() error {
			return containerrt.Remove(w)
		}); err != nil {
			return err
		}
	}

	fmt.Printf("cluster %q scaled to %d worker(s)\n", cfg.Name, cfg.Workers)
	return nil
}
