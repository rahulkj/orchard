package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/rahulkj/orchard/internal/selfupdate"
)

func init() {
	var repo string
	c := &cobra.Command{
		Use:     "self-update",
		Short:   "Update orchard to the latest released version",
		Example: `  orchard self-update`,
		RunE: func(cmd *cobra.Command, args []string) error {
			rel, err := selfupdate.LatestRelease(repo)
			if err != nil {
				return err
			}
			if rel.Tag == Version {
				fmt.Printf("already up to date (%s)\n", Version)
				return nil
			}
			fmt.Printf("updating %s -> %s\n", Version, rel.Tag)
			if err := selfupdate.Apply(rel.AssetURL); err != nil {
				return err
			}
			fmt.Println("done. run `orchard version` to confirm.")
			return nil
		},
	}
	c.Flags().StringVar(&repo, "repo", "rahulkj/orchard", "GitHub repo (owner/name) to check for releases")
	rootCmd.AddCommand(c)
}
