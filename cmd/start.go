package cmd

import (
	"github.com/spf13/cobra"

	"github.com/rahulkj/orchard/internal/k8s"
)

func init() {
	var name string
	c := &cobra.Command{
		Use:     "start",
		Short:   "Start a stopped cluster's node VMs",
		Example: `  orchard start --name dev`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return k8s.NewManager().Start(name)
		},
	}
	c.Flags().StringVar(&name, "name", "dev", "cluster name")
	rootCmd.AddCommand(c)
}
