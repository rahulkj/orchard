package cmd

import (
	"github.com/spf13/cobra"

	"github.com/rahulkj/orchard/internal/k8s"
)

func init() {
	var name string
	c := &cobra.Command{
		Use:     "stop",
		Short:   "Stop a cluster's node VMs without deleting them",
		Example: `  orchard stop --name dev`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return k8s.NewManager().Stop(name)
		},
	}
	c.Flags().StringVar(&name, "name", "dev", "cluster name")
	rootCmd.AddCommand(c)
}
