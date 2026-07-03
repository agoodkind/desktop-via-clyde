package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"goodkind.io/desktop-via-clyde/internal/updateopts"
	"goodkind.io/desktop-via-clyde/internal/version"
	"goodkind.io/go-makefile/selfupdate"
)

var (
	selfUpdateCheck     = selfupdate.Check
	selfUpdateApply     = selfupdate.Apply
	selfUpdateLoadState = selfupdate.LoadState
)

func newUpdateCmd(out io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Manage desktop-via-clyde self-updates",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(newUpdateCheckCmd(out))
	cmd.AddCommand(newUpdateApplyCmd(out))
	cmd.AddCommand(newUpdateStatusCmd(out))
	return cmd
}

func newUpdateCheckCmd(out io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "check",
		Short: "Check for a desktop-via-clyde release update",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := selfUpdateCheck(cmd.Context(), updateopts.Options(updateopts.Overrides{}))
			if err != nil {
				return fmt.Errorf("update check: %w", err)
			}
			printUpdateCheckResult(out, result)
			return nil
		},
	}
}

func newUpdateApplyCmd(out io.Writer) *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Download, verify, and install a desktop-via-clyde release update",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runUpdateApply(cmd.Context(), out, dryRun)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "download and verify without installing")
	return cmd
}

func newUpdateStatusCmd(out io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show desktop-via-clyde self-update state",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			statePath := selfupdate.DefaultStatePath("desktop-via-clyde")
			state, err := selfUpdateLoadState(statePath)
			if err != nil {
				return fmt.Errorf("update status: %w", err)
			}
			printUpdateStatus(out, state)
			return nil
		},
	}
}

func runUpdateApply(ctx context.Context, out io.Writer, dryRun bool) error {
	result, err := selfUpdateApply(ctx, updateopts.Options(updateopts.Overrides{DryRun: dryRun}))
	if err != nil {
		return fmt.Errorf("update apply: %w", err)
	}
	if !result.UpdateAvailable {
		_, _ = io.WriteString(out, "desktop-via-clyde: already current\n")
		return nil
	}
	if dryRun {
		_, _ = io.WriteString(out, "desktop-via-clyde: update apply dry run ok\n")
		return nil
	}
	if result.Applied {
		_, _ = io.WriteString(out, "desktop-via-clyde: update applied\n")
		return nil
	}
	_, _ = io.WriteString(out, "desktop-via-clyde: update verified but not applied\n")
	return nil
}

func printUpdateCheckResult(out io.Writer, result selfupdate.CheckResult) {
	_, _ = fmt.Fprintf(out, "current version: %s\n", result.CurrentVersion)
	_, _ = fmt.Fprintf(out, "current commit:  %s\n", result.CurrentCommit)
	_, _ = fmt.Fprintf(out, "latest tag:      %s\n", result.LatestTag)
	_, _ = fmt.Fprintf(out, "asset:           %s\n", result.AssetName)
	if result.UpdateAvailable {
		_, _ = io.WriteString(out, "update available: yes\n")
		return
	}
	_, _ = io.WriteString(out, "update available: no\n")
}

func printUpdateStatus(out io.Writer, state selfupdate.State) {
	_, _ = fmt.Fprintf(out, "current version:   %s\n", version.Version)
	_, _ = fmt.Fprintf(out, "current commit:    %s\n", version.Commit)
	_, _ = fmt.Fprintf(out, "current buildHash: %s\n", version.BuildHash())
	if !state.LastCheckAt.IsZero() {
		_, _ = fmt.Fprintf(out, "last check:        %s\n", state.LastCheckAt.Format(time.RFC3339))
	}
	if !state.NextCheckAt.IsZero() {
		_, _ = fmt.Fprintf(out, "next check:        %s\n", state.NextCheckAt.Format(time.RFC3339))
	}
	if state.LatestTag != "" {
		_, _ = fmt.Fprintf(out, "latest tag:        %s\n", state.LatestTag)
	}
	if state.AppliedTag != "" {
		_, _ = fmt.Fprintf(out, "applied tag:       %s\n", state.AppliedTag)
	}
	if state.LastResult != "" {
		_, _ = fmt.Fprintf(out, "last result:       %s\n", state.LastResult)
	}
	if state.LastError != "" {
		_, _ = fmt.Fprintf(out, "last error:        %s\n", state.LastError)
	}
}
