package cmd

import (
	"fmt"
	"os/exec"

	"github.com/spf13/cobra"

	"github.com/rahulkj/orchard/internal/containerrt"
)

func init() {
	c := &cobra.Command{
		Use:     "doctor",
		Short:   "Check that this Mac can run orchard clusters",
		Example: `  orchard doctor`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ok := true
			check := func(label string, pass bool, hint string) {
				mark := "OK"
				if !pass {
					mark = "FAIL"
					ok = false
				}
				fmt.Printf("[%s] %s\n", mark, label)
				if !pass && hint != "" {
					fmt.Printf("       %s\n", hint)
				}
			}
			check("apple/container CLI on PATH", containerrt.Available(),
				"install from https://github.com/apple/container")
			check("container system service running", containerrt.SystemRunning(),
				"run: container system start")
			_, kubectlErr := exec.LookPath("kubectl")
			check("kubectl on PATH", kubectlErr == nil,
				"install kubectl: brew install kubectl")
			if !ok {
				return fmt.Errorf("one or more checks failed")
			}
			fmt.Println("\nall good - run: orchard create --name dev --workers 2")
			return nil
		},
	}
	rootCmd.AddCommand(c)
}
