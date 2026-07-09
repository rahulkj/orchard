// Package cmd implements the orchard CLI: create, delete, and scale
// Kubernetes clusters where every node is an apple/container VM.
package cmd

import (
	"github.com/spf13/cobra"
)

// Version is the orchard build version. Set at build time with:
//
//	go build -ldflags "-X github.com/rahulkj/orchard/cmd.Version=v1.2.3"
//
// goreleaser (or any CI that tags releases) does this automatically.
// Unset, it stays "dev".
var Version = "dev"

var rootCmd = &cobra.Command{
	Use:   "orchard",
	Short: "Kubernetes clusters where every node is an apple/container VM",
	Long: `orchard boots kubeadm Kubernetes clusters on top of Apple's container
runtime (https://github.com/apple/container). Each node -- control plane and
every worker -- is its own apple/container VM.`,
}

func Execute() error {
	return rootCmd.Execute()
}
