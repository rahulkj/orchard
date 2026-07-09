package k8s

import (
	"fmt"
	"strconv"
	"strings"
)

// maxNodeNameLen is the Linux hostname limit. A node's container name is
// also its hostname and kubeadm node name, so all three only stay in sync
// while the name fits here.
const maxNodeNameLen = 63

func prefix(cluster string) string { return "orchard-" + cluster + "-" }

// ControlPlane returns the control-plane container/node name for a cluster.
func ControlPlane(cluster string) string { return prefix(cluster) + "control-plane" }

// Worker returns the container/node name for worker index i (1-based).
func Worker(cluster string, i int) string { return fmt.Sprintf("%sworker-%d", prefix(cluster), i) }

// ValidName reports whether a cluster name is safe for container names,
// kubeconfig entries, and Linux hostnames: lowercase letters, digits, dashes.
func ValidName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r != '-' && (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

// CheckNameLength rejects a cluster name whose control-plane node name would
// exceed the Linux hostname limit.
func CheckNameLength(cluster string) error {
	cp := ControlPlane(cluster)
	if len(cp) > maxNodeNameLen {
		return fmt.Errorf("cluster name %q is too long: node name %q is %d chars, over the %d-char limit",
			cluster, cp, len(cp), maxNodeNameLen)
	}
	return nil
}

// WorkerIndex extracts the numeric suffix from a worker container name, e.g.
// "orchard-dev-worker-3" -> 3. Returns 0, false if name isn't a worker of
// this cluster.
func WorkerIndex(cluster, name string) (int, bool) {
	p := prefix(cluster) + "worker-"
	if !strings.HasPrefix(name, p) {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimPrefix(name, p))
	if err != nil {
		return 0, false
	}
	return n, true
}
