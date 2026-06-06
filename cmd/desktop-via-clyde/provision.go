package main

import (
	"fmt"
	"io"
	"log/slog"

	"github.com/spf13/cobra"

	"goodkind.io/desktop-via-clyde/internal/appleportal"
	"goodkind.io/desktop-via-clyde/internal/paths"
)

var errProvisionFlagsMissing = fmt.Errorf("both --bundle-id and --out are required")

type provisionProfileHandler struct {
	out         io.Writer
	bundleID    string
	outputPath  string
	profileName string
}

// newProvisionCmd builds the verb-first `provision` parent that groups the
// App Store Connect provisioning subcommands.
func newProvisionCmd(out io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "provision",
		Short: "Generate provisioning assets via App Store Connect",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(newProvisionProfileCmd(out))
	return cmd
}

// newProvisionProfileCmd builds the one-time `provision profile` command that
// generates a Developer ID provisioning profile through App Store Connect and
// writes it to a local path. The patch flow then embeds that local profile, so
// patching itself never contacts Apple.
func newProvisionProfileCmd(out io.Writer) *cobra.Command {
	handler := &provisionProfileHandler{out: out}
	cmd := &cobra.Command{
		Use:   "profile",
		Short: "Generate a Developer ID provisioning profile via App Store Connect",
	}
	cmd.RunE = handler.run
	cmd.Flags().StringVar(&handler.bundleID, "bundle-id", "", "bundle identifier to provision, for example com.openai.codex.beta")
	cmd.Flags().StringVar(&handler.outputPath, "out", "", "destination path for the generated .provisionprofile")
	cmd.Flags().StringVar(&handler.profileName, "name", "", "profile name, defaults to one derived from the bundle id")
	return cmd
}

func (h *provisionProfileHandler) run(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	slog.InfoContext(ctx, "cli.provision_profile.start", "bundle_id", h.bundleID, "out", h.outputPath)
	if h.bundleID == "" || h.outputPath == "" {
		return errProvisionFlagsMissing
	}
	name := h.profileName
	if name == "" {
		name = "desktop-via-clyde " + h.bundleID
	}
	if err := appleportal.ProvisionDeveloperIDProfile(ctx, h.bundleID, name, paths.SignIdentity(), h.outputPath); err != nil {
		slog.ErrorContext(ctx, "cli.provision_profile.failed", "bundle_id", h.bundleID, "err", err)
		return fmt.Errorf("provision profile %s: %w", h.bundleID, err)
	}
	_, _ = io.WriteString(h.out, "wrote provisioning profile to "+h.outputPath+"\n")
	return nil
}
