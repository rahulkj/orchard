package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/rahulkj/orchard/internal/k8s"
)

func init() {
	var name, image string
	var yes bool
	c := &cobra.Command{
		Use:   "upgrade",
		Short: "Move a cluster to a newer Kubernetes node image",
		Long: `Upgrade replaces a cluster's node VMs with a newer image. This is a
destroy-and-recreate, not an in-place kubeadm upgrade -- kubeadm only
supports upgrading one minor version at a time with matching binaries
already on the node, and node images here are immutable single-version
builds with no general internet egress to fetch different ones. Upstream
kind has the same limitation and takes the same approach: there's no
in-place "kind upgrade". Workloads are NOT preserved.`,
		Example: `  orchard upgrade --name dev
  orchard upgrade --name dev --image docker.io/kindest/node@sha256:...`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes {
				fmt.Printf("This deletes and recreates cluster %q with a new node image. Workloads are not preserved. Continue? [y/N] ", name)
				reader := bufio.NewReader(os.Stdin)
				line, _ := reader.ReadString('\n')
				if strings.ToLower(strings.TrimSpace(line)) != "y" {
					fmt.Println("aborted")
					return nil
				}
			}
			result, err := k8s.NewManager().Upgrade(name, image)
			if err != nil {
				return err
			}
			if !result.Changed {
				fmt.Printf("cluster %q is already on %s\n", name, result.ToVersion)
				return nil
			}
			fmt.Printf("\ncluster %q upgraded: %s -> %s\n", name, result.FromVersion, result.ToVersion)
			return nil
		},
	}
	c.Flags().StringVar(&name, "name", "dev", "cluster name")
	c.Flags().StringVar(&image, "image", "", "target node image (default: latest available)")
	c.Flags().BoolVarP(&yes, "yes", "y", false, "skip the confirmation prompt")
	rootCmd.AddCommand(c)
}
