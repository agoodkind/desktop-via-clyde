// Command desktop-via-clyde patches macOS Electron apps (Cursor, Codex, Claude)
// to route every launch through the clyde MITM proxy on [::1]:48723.
//
// Every program is invoked with the shape `desktop-via-clyde <program> <operation>`:
//
//	desktop-via-clyde cursor patch             install shim and re-sign Cursor
//	desktop-via-clyde codex upgrade            fetch the latest Codex build and re-patch
//	desktop-via-clyde claude unpatch           restore Claude from its backup
//	desktop-via-clyde status                   per-target state summary
//	desktop-via-clyde codex-cli upgrade        build and install locally signed Codex CLI
//	desktop-via-clyde codex-cli install        alias for `codex-cli upgrade`
//
// App operation subcommands support --dry-run. patch and keychain-migrate
// support --no-migrate-keychain.
package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/spf13/cobra"

	"goodkind.io/desktop-via-clyde/internal/codexcli"
	"goodkind.io/desktop-via-clyde/internal/patch"
	"goodkind.io/desktop-via-clyde/internal/paths"
	"goodkind.io/desktop-via-clyde/internal/state"
	"goodkind.io/desktop-via-clyde/internal/targets"
	"goodkind.io/desktop-via-clyde/internal/upgrade"
)

func main() {
	root := newRootCmd(os.Stdout)
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func newRootCmd(out io.Writer) *cobra.Command {
	root := &cobra.Command{
		Use:           "desktop-via-clyde",
		Short:         "Patch macOS Electron apps (Cursor, Codex, Claude) to route through the clyde MITM proxy",
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	root.SetOut(out)
	root.SetErr(out)

	for _, target := range targets.Registry {
		root.AddCommand(newTargetCmd(out, target))
	}
	root.AddCommand(newStatusCmd(out))
	root.AddCommand(newCodexCLICmd(out))
	return root
}

func targetWithAppPath(t targets.Target, appPath string) targets.Target {
	if appPath != "" {
		t.AppPath = appPath
	}
	return t
}

func newTargetCmd(out io.Writer, target targets.Target) *cobra.Command {
	cmd := &cobra.Command{
		Use:   target.ID,
		Short: fmt.Sprintf("Operate on %s", target.AppPath),
	}
	cmd.AddCommand(newPatchCmd(out, target))
	cmd.AddCommand(newUnpatchCmd(out, target))
	cmd.AddCommand(newUpgradeCmd(out, target))
	cmd.AddCommand(newKeychainMigrateCmd(out, target))
	cmd.AddCommand(newTargetStatusCmd(out, target))
	return cmd
}

func newPatchCmd(out io.Writer, target targets.Target) *cobra.Command {
	var dryRun bool
	var noMigrate bool
	var appPath string
	cmd := &cobra.Command{
		Use:   "patch",
		Short: "Install the shim into one app bundle and re-sign it",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			t := targetWithAppPath(target, appPath)
			return patch.Patch(t, patch.Options{
				DryRun:            dryRun,
				NoMigrateKeychain: noMigrate,
				Out:               out,
			})
		},
	}
	cmd.Long = fmt.Sprintf("Patch %s by installing the shim and re-signing the bundle.", target.AppPath)
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print every step without modifying the bundle")
	cmd.Flags().BoolVar(&noMigrate, "no-migrate-keychain", false, "skip steps 1b and 7a (keychain ACL re-grant)")
	cmd.Flags().StringVar(&appPath, "app-path", "", "override the target .app path for isolated testing")
	return cmd
}

func newUnpatchCmd(out io.Writer, target targets.Target) *cobra.Command {
	var dryRun bool
	var appPath string
	cmd := &cobra.Command{
		Use:   "unpatch",
		Short: "Restore one app's bundle from its backup",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			t := targetWithAppPath(target, appPath)
			return patch.Unpatch(t, patch.Options{DryRun: dryRun, Out: out})
		},
	}
	cmd.Long = fmt.Sprintf("Restore %s from its backup.", target.AppPath)
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print every step without modifying the bundle")
	cmd.Flags().StringVar(&appPath, "app-path", "", "override the target .app path for isolated testing")
	return cmd
}

func newUpgradeCmd(out io.Writer, target targets.Target) *cobra.Command {
	var channel string
	var dryRun bool
	var noMigrate bool
	var appPath string
	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "Fetch the latest build from the upstream update manifest, verify, swap into the app path, and re-patch",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			t := targetWithAppPath(target, appPath)
			return upgrade.Run(t, upgrade.Options{
				Channel:           channel,
				DryRun:            dryRun,
				NoMigrateKeychain: noMigrate,
				Out:               out,
			})
		},
	}
	cmd.Long = fmt.Sprintf(
		"Upgrade %s by fetching the latest upstream manifest, verifying the downloaded bundle against the recorded upstream DesignatedRequirement, swapping it into place, and re-running the patch flow.",
		target.AppPath,
	)
	cmd.Flags().StringVar(&channel, "channel", "stable", "upstream release channel (stable, dev, etc.)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print every step without modifying the bundle")
	cmd.Flags().BoolVar(&noMigrate, "no-migrate-keychain", false, "skip keychain ACL re-grant during the post-swap patch")
	cmd.Flags().StringVar(&appPath, "app-path", "", "override the target .app path for isolated testing")
	return cmd
}

func newKeychainMigrateCmd(out io.Writer, target targets.Target) *cobra.Command {
	var dryRun bool
	var appPath string
	cmd := &cobra.Command{
		Use:   "keychain-migrate",
		Short: "Re-grant keychain ACLs on an already-patched app (steps 1b + 7a only)",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			t := targetWithAppPath(target, appPath)
			return patch.KeychainMigrate(t, patch.Options{DryRun: dryRun, Out: out})
		},
	}
	cmd.Long = fmt.Sprintf("Re-grant keychain ACLs on the patched %s bundle.", target.AppPath)
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print every step without touching keychain items")
	cmd.Flags().StringVar(&appPath, "app-path", "", "override the target .app path for isolated testing")
	return cmd
}

func newCodexCLICmd(out io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "codex-cli",
		Short: "Build, sign, install, and inspect the local Codex CLI",
	}
	cmd.AddCommand(newCodexCLIUpgradeCmd(out))
	cmd.AddCommand(newCodexCLIStatusCmd(out))
	return cmd
}

func newCodexCLIUpgradeCmd(out io.Writer) *cobra.Command {
	var dryRun bool
	var sourceDir string
	var ref string
	var installDir string
	var codexHome string
	var buildMode string
	var noSccache bool
	var forceRebuild bool
	cmd := &cobra.Command{
		Use:     "upgrade",
		Aliases: []string{"install"},
		Short:   "Clone or update Codex source, build the CLI, sign it locally, and install it",
		RunE: func(_ *cobra.Command, _ []string) error {
			return codexcli.Install(codexcli.InstallOptions{
				DryRun:       dryRun,
				SourceDir:    sourceDir,
				Ref:          ref,
				InstallDir:   installDir,
				CodexHome:    codexHome,
				BuildMode:    buildMode,
				NoSccache:    noSccache,
				ForceRebuild: forceRebuild,
				Out:          out,
			})
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print every step without modifying the filesystem")
	cmd.Flags().StringVar(&sourceDir, "source-dir", "", "Codex source checkout path (default XDG cache)")
	cmd.Flags().StringVar(&ref, "ref", codexcli.DefaultRef(), "upstream Codex ref to fetch and build")
	cmd.Flags().StringVar(&installDir, "install-dir", "", "directory for the visible codex command")
	cmd.Flags().StringVar(&codexHome, "codex-home", "", "Codex home for standalone package releases")
	cmd.Flags().StringVar(&buildMode, "build-mode", codexcli.DefaultBuildMode(), "entrypoint build mode (local-fast or release)")
	cmd.Flags().BoolVar(&noSccache, "no-sccache", false, "disable automatic sccache wrapper detection for the Cargo build")
	cmd.Flags().BoolVar(&forceRebuild, "force-rebuild", false, "skip same-head installed release reuse and rebuild the entrypoint")
	return cmd
}

func newCodexCLIStatusCmd(out io.Writer) *cobra.Command {
	var sourceDir string
	var installDir string
	var codexHome string
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Print local Codex CLI source, install, and signing state",
		RunE: func(_ *cobra.Command, _ []string) error {
			return codexcli.Status(codexcli.StatusOptions{
				SourceDir:  sourceDir,
				InstallDir: installDir,
				CodexHome:  codexHome,
				Out:        out,
			})
		},
	}
	cmd.Flags().StringVar(&sourceDir, "source-dir", "", "Codex source checkout path (default XDG cache)")
	cmd.Flags().StringVar(&installDir, "install-dir", "", "directory for the visible codex command")
	cmd.Flags().StringVar(&codexHome, "codex-home", "", "Codex home for standalone package releases")
	return cmd
}

func newStatusCmd(out io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Print per-target state (clean/patched/drifted) and bundle metadata",
		RunE: func(_ *cobra.Command, _ []string) error {
			ms, err := state.Load(paths.StateFile())
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "state file: %s\n", paths.StateFile())
			fmt.Fprintf(out, "%-8s  %-9s  %-20s  %s\n", "TARGET", "STATE", "VERSION", "NOTES")
			for _, t := range targets.Registry {
				printTargetStatus(out, t, ms)
			}
			return nil
		},
	}
}

func newTargetStatusCmd(out io.Writer, target targets.Target) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Print state for this target",
		RunE: func(_ *cobra.Command, _ []string) error {
			ms, err := state.Load(paths.StateFile())
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "state file: %s\n", paths.StateFile())
			fmt.Fprintf(out, "%-8s  %-9s  %-20s  %s\n", "TARGET", "STATE", "VERSION", "NOTES")
			printTargetStatus(out, target, ms)
			return nil
		},
	}
}

func printTargetStatus(out io.Writer, t targets.Target, ms state.MultiState) {
	entry, patched := ms.Targets[t.ID]
	if _, err := os.Stat(t.AppPath); err != nil {
		fmt.Fprintf(out, "%-8s  %-9s  %-20s  bundle missing at %s\n", t.ID, "absent", "-", t.AppPath)
		return
	}
	if !patched {
		fmt.Fprintf(out, "%-8s  %-9s  %-20s  bundle present, no state entry\n", t.ID, "clean", "-")
		return
	}
	curVer := readBundleVersion(t)
	stateLabel := "patched"
	notes := fmt.Sprintf("signed-as=%q", entry.SignIdentity)
	realPath := paths.RealBinaryPath(t)
	if _, err := os.Stat(realPath); err != nil {
		stateLabel = "drifted"
		notes = notes + "; " + t.ExecName + ".real missing"
	} else if curVer != "" && curVer != entry.PatchedVersion {
		stateLabel = "drifted"
		notes = notes + fmt.Sprintf("; current version %s != patched %s", curVer, entry.PatchedVersion)
	}
	fmt.Fprintf(out, "%-8s  %-9s  %-20s  %s\n", t.ID, stateLabel, entry.PatchedVersion, notes)
}

func readBundleVersion(t targets.Target) string {
	cmd := exec.Command("/usr/bin/defaults", "read", t.AppPath+"/Contents/Info", "CFBundleVersion")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	s := string(out)
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r' || s[len(s)-1] == ' ') {
		s = s[:len(s)-1]
	}
	return s
}
