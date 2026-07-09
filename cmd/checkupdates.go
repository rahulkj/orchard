package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/rahulkj/orchard/internal/k8s"
)

func init() {
	c := &cobra.Command{
		Use:     "check-updates",
		Short:   "Check whether a newer Kubernetes node image is available",
		Example: `  orchard check-updates`,
		RunE: func(cmd *cobra.Command, args []string) error {
			latest, err := k8s.LatestAvailable()
			if err != nil {
				return err
			}
			fmt.Printf("latest available: %s\n\n", latest.Version)

			clusters, err := k8s.Clusters()
			if err != nil {
				return err
			}
			if len(clusters) == 0 {
				fmt.Println("no clusters running")
				return nil
			}
			m := k8s.NewManager()
			for _, name := range clusters {
				current, err := m.CurrentVersion(name)
				if err != nil {
					fmt.Printf("%-20s error: %v\n", name, err)
					continue
				}
				status := "up to date"
				if current != latest.Version {
					status = fmt.Sprintf("upgrade available -> orchard upgrade --name %s", name)
				}
				fmt.Printf("%-20s %-10s %s\n", name, current, status)
			}
			return nil
		},
	}
	rootCmd.AddCommand(c)
}
