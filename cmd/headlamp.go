package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/rahulkj/orchard/internal/k8s"
)

func init() {
	headlampCmd := &cobra.Command{
		Use:   "headlamp",
		Short: "Install and access the Headlamp web UI",
	}

	var installName string
	install := &cobra.Command{
		Use:     "install",
		Short:   "Install Headlamp into a running cluster",
		Example: `  orchard headlamp install --name dev`,
		RunE: func(cmd *cobra.Command, args []string) error {
			token, err := k8s.NewManager().HeadlampToken(installName)
			if err != nil {
				return err
			}
			printHeadlampAccess(installName, token)
			return nil
		},
	}
	install.Flags().StringVar(&installName, "name", "dev", "cluster name")

	var tokenName string
	token := &cobra.Command{
		Use:     "token",
		Short:   "Print a Headlamp access token (installs Headlamp if needed)",
		Example: `  orchard headlamp token --name dev`,
		RunE: func(cmd *cobra.Command, args []string) error {
			token, err := k8s.NewManager().HeadlampToken(tokenName)
			if err != nil {
				return err
			}
			printHeadlampAccess(tokenName, token)
			return nil
		},
	}
	token.Flags().StringVar(&tokenName, "name", "dev", "cluster name")

	headlampCmd.AddCommand(install, token)
	rootCmd.AddCommand(headlampCmd)
}

func printHeadlampAccess(cluster, token string) {
	fmt.Println("Headlamp is installed in kube-system. To access it:")
	fmt.Println()
	fmt.Println("  kubectl port-forward -n kube-system service/headlamp 8080:80")
	fmt.Println("  open http://localhost:8080")
	fmt.Println()
	fmt.Println("Login with this token (valid 1 year):")
	fmt.Println()
	fmt.Println("  " + token)
}
