package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/rahulkj/orchard/internal/k8s"
)

func init() {
	c := &cobra.Command{
		Use:     "list",
		Short:   "List clusters and their nodes",
		Example: `  orchard list`,
		RunE: func(cmd *cobra.Command, args []string) error {
			clusters, err := k8s.Clusters()
			if err != nil {
				return err
			}
			if len(clusters) == 0 {
				fmt.Println("no clusters found")
				return nil
			}
			for _, name := range clusters {
				nodes, err := k8s.Nodes(name)
				if err != nil {
					return err
				}
				fmt.Printf("%s\n", name)
				for _, n := range nodes {
					fmt.Printf("  %-40s %-10s %s\n", n.Name, n.State, n.Image)
				}
			}
			return nil
		},
	}
	rootCmd.AddCommand(c)
}
