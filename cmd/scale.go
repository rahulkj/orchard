package cmd

import (
	"github.com/spf13/cobra"

	"github.com/rahulkj/orchard/internal/k8s"
)

func init() {
	cfg := k8s.ScaleConfig{}
	c := &cobra.Command{
		Use:   "scale",
		Short: "Scale a cluster's worker nodes up or down",
		Example: `  orchard scale --name dev --workers 4   # add workers
  orchard scale --name dev --workers 1   # drain and remove workers`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return k8s.NewManager().Scale(cfg)
		},
	}
	c.Flags().StringVar(&cfg.Name, "name", "dev", "cluster name")
	c.Flags().IntVar(&cfg.Workers, "workers", 0, "desired worker count")
	c.Flags().StringVar(&cfg.Image, "image", "", "image for newly added workers (default: same as control plane)")
	c.Flags().StringVar(&cfg.WorkerCPUs, "worker-cpus", "", "vCPUs for newly added workers (default 4)")
	c.Flags().StringVar(&cfg.WorkerMem, "worker-memory", "", "memory for newly added workers (default 4096M)")
	_ = c.MarkFlagRequired("workers")
	rootCmd.AddCommand(c)
}
