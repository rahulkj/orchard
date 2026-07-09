package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

func init() {
	c := &cobra.Command{
		Use:   "version",
		Short: "Print the orchard version",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("orchard " + Version)
			return nil
		},
	}
	rootCmd.AddCommand(c)
}
