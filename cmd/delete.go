package cmd

import (
	"github.com/spf13/cobra"

	"github.com/rahulkj/orchard/internal/k8s"
)

func init() {
	var name string
	c := &cobra.Command{
		Use:     "delete",
		Short:   "Delete a cluster and its node VMs",
		Example: `  orchard delete --name dev`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return k8s.NewManager().Delete(name)
		},
	}
	c.Flags().StringVar(&name, "name", "dev", "cluster name")
	rootCmd.AddCommand(c)
}
