// Package state persists the configuration a cluster was created with, so
// commands that act on an existing cluster later (scale, upgrade) can reuse
// its settings instead of re-deriving them from flags or live VM inspection.
package state

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Cluster is the durable record of one orchard-managed cluster.
type Cluster struct {
	Name         string `json:"name"`
	Image        string `json:"image"`
	CPCPUs       string `json:"cpCpus"`
	CPMemory     string `json:"cpMemory"`
	WorkerCPUs   string `json:"workerCpus"`
	WorkerMem    string `json:"workerMemory"`
	CNI          string `json:"cni"`
	NoMetrics    bool   `json:"noMetrics"`
	NoStorage    bool   `json:"noStorage"`
	Headlamp     bool   `json:"headlamp"`
	ProxyForward bool   `json:"proxyForward"`
}

func dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	d := filepath.Join(home, ".orchard", "clusters")
	if err := os.MkdirAll(d, 0o755); err != nil {
		return "", err
	}
	return d, nil
}

func path(name string) (string, error) {
	d, err := dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, name+".json"), nil
}

// Save writes (or overwrites) a cluster's state record.
func Save(c Cluster) error {
	p, err := path(c.Name)
	if err != nil {
		return err
	}
	out, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, out, 0o644)
}

// Load reads a cluster's state record. Returns os.ErrNotExist if absent --
// callers on older clusters created before state existed should fall back
// to defaults rather than failing.
func Load(name string) (Cluster, error) {
	p, err := path(name)
	if err != nil {
		return Cluster{}, err
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return Cluster{}, err
	}
	var c Cluster
	if err := json.Unmarshal(raw, &c); err != nil {
		return Cluster{}, err
	}
	return c, nil
}

// Delete removes a cluster's state record, ignoring a missing file.
func Delete(name string) error {
	p, err := path(name)
	if err != nil {
		return err
	}
	err = os.Remove(p)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
