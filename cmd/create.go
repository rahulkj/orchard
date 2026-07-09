package cmd

import (
	"github.com/spf13/cobra"

	"github.com/rahulkj/orchard/internal/k8s"
)

func init() {
	cfg := k8s.CreateConfig{}
	c := &cobra.Command{
		Use:   "create",
		Short: "Create a Kubernetes cluster on apple/container",
		Example: `  orchard create --name dev --workers 2
  orchard create --name dev --workers 0   # single-node, control plane untainted
  orchard create --name dev --headlamp --proxy-forward`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return k8s.NewManager().Create(cfg)
		},
	}
	c.Flags().StringVar(&cfg.Name, "name", "dev", "cluster name")
	c.Flags().IntVar(&cfg.Workers, "workers", 2, "number of worker nodes")
	c.Flags().StringVar(&cfg.Image, "image", "", "node image reference (default: pinned kindest/node build)")
	c.Flags().StringVar(&cfg.CPCPUs, "cp-cpus", "", "control-plane vCPUs (default 4)")
	c.Flags().StringVar(&cfg.CPMemory, "cp-memory", "", "control-plane memory (default 4096M)")
	c.Flags().StringVar(&cfg.WorkerCPUs, "worker-cpus", "", "per-worker vCPUs (default 4)")
	c.Flags().StringVar(&cfg.WorkerMem, "worker-memory", "", "per-worker memory (default 2048M)")
	c.Flags().BoolVar(&cfg.NoMetrics, "no-metrics", false, "skip installing metrics-server")
	c.Flags().BoolVar(&cfg.NoStorage, "no-storage", false, "skip installing the default storage class")
	c.Flags().StringVar(&cfg.CNI, "cni", "kindnet", "pod network: kindnet (verified working), flannel (confirmed broken, see notes), or calico (untested)")
	c.Flags().BoolVar(&cfg.Headlamp, "headlamp", false, "install the Headlamp web UI")
	c.Flags().BoolVar(&cfg.ProxyForward, "proxy-forward", false, "forward the host's proxy env vars and trusted root CAs into every node VM")
	rootCmd.AddCommand(c)
}
