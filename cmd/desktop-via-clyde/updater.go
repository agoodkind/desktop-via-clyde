package main

import (
	"github.com/spf13/cobra"

	"goodkind.io/desktop-via-clyde/internal/daemon"
)

// newUpdaterCmd builds the `updater` command group that manages the background
// daemon which owns the operation control plane and the upgrade tick loop.
func newUpdaterCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "updater",
		Short: "Manage the background updater daemon",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(newUpdaterRunCmd())
	return cmd
}

// newUpdaterRunCmd runs the daemon in the foreground. launchd owns the daemon's
// lifecycle in production, so this is normally invoked by the launch agent.
func newUpdaterRunCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "run",
		Short: "Run the updater daemon in the foreground",
		Long:  "Run the updater daemon control plane. launchd owns the daemon's lifecycle, so this entry point is normally invoked by the launch agent rather than directly.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return daemon.Run(cmd.Context())
		},
	}
}
