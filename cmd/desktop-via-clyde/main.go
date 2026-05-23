// Command desktop-via-clyde patches macOS Electron apps (Cursor, Codex, Claude)
// to route every launch through the clyde MITM proxy on [::1]:48723.
//
//	desktop-via-clyde patch <app>              install shim and re-sign
//	desktop-via-clyde unpatch <app>            restore one target's backup
//	desktop-via-clyde status                   per-target state summary
//	desktop-via-clyde keychain-migrate <app>   re-grant keychain ACLs on an already-patched app
//
// All subcommands support --dry-run. patch and keychain-migrate support
// --no-migrate-keychain.
package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

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

	root.AddCommand(newPatchCmd(out))
	root.AddCommand(newUnpatchCmd(out))
	root.AddCommand(newStatusCmd(out))
	root.AddCommand(newKeychainMigrateCmd(out))
	root.AddCommand(newMITMHookCmd())
	root.AddCommand(newUpgradeCmd(out))
	return root
}

func appArg() string {
	return "app to act on (one of: " + strings.Join(targets.IDs(), ", ") + ")"
}

func lookupTarget(arg, appPath string) (targets.Target, error) {
	t, err := targets.Lookup(arg)
	if err != nil {
		return targets.Target{}, err
	}
	if appPath != "" {
		t.AppPath = appPath
	}
	return t, nil
}

func newPatchCmd(out io.Writer) *cobra.Command {
	var dryRun bool
	var noMigrate bool
	var appPath string
	cmd := &cobra.Command{
		Use:   "patch <app>",
		Short: "Install the shim into one app bundle and re-sign it",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			t, err := lookupTarget(args[0], appPath)
			if err != nil {
				return err
			}
			return patch.Patch(t, patch.Options{
				DryRun:            dryRun,
				NoMigrateKeychain: noMigrate,
				Out:               out,
			})
		},
	}
	cmd.Long = "patch <app>: " + appArg()
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print every step without modifying the bundle")
	cmd.Flags().BoolVar(&noMigrate, "no-migrate-keychain", false, "skip steps 1b and 7a (keychain ACL re-grant)")
	cmd.Flags().StringVar(&appPath, "app-path", "", "override the target .app path for isolated testing")
	return cmd
}

func newUnpatchCmd(out io.Writer) *cobra.Command {
	var dryRun bool
	var appPath string
	cmd := &cobra.Command{
		Use:   "unpatch <app>",
		Short: "Restore one app's bundle from its backup",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			t, err := lookupTarget(args[0], appPath)
			if err != nil {
				return err
			}
			return patch.Unpatch(t, patch.Options{DryRun: dryRun, Out: out})
		},
	}
	cmd.Long = "unpatch <app>: " + appArg()
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print every step without modifying the bundle")
	cmd.Flags().StringVar(&appPath, "app-path", "", "override the target .app path for isolated testing")
	return cmd
}

func newUpgradeCmd(out io.Writer) *cobra.Command {
	var channel string
	var dryRun bool
	var noMigrate bool
	var appPath string
	cmd := &cobra.Command{
		Use:   "upgrade <app>",
		Short: "Fetch the latest build from the upstream update manifest, verify, swap into the app path, and re-patch",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			t, err := lookupTarget(args[0], appPath)
			if err != nil {
				return err
			}
			return upgrade.Run(t, upgrade.Options{
				Channel:           channel,
				DryRun:            dryRun,
				NoMigrateKeychain: noMigrate,
				Out:               out,
			})
		},
	}
	cmd.Long = "upgrade <app>: " + appArg() + ". Bypasses the in-app updater; fetches the latest upstream manifest directly, verifies the downloaded bundle against the recorded upstream DesignatedRequirement, swaps it into the target app path, and re-runs the patch flow."
	cmd.Flags().StringVar(&channel, "channel", "stable", "upstream release channel (stable, dev, etc.)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print every step without modifying the bundle")
	cmd.Flags().BoolVar(&noMigrate, "no-migrate-keychain", false, "skip keychain ACL re-grant during the post-swap patch")
	cmd.Flags().StringVar(&appPath, "app-path", "", "override the target .app path for isolated testing")
	return cmd
}

func newKeychainMigrateCmd(out io.Writer) *cobra.Command {
	var dryRun bool
	var appPath string
	cmd := &cobra.Command{
		Use:   "keychain-migrate <app>",
		Short: "Re-grant keychain ACLs on an already-patched app (steps 1b + 7a only)",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			t, err := lookupTarget(args[0], appPath)
			if err != nil {
				return err
			}
			return patch.KeychainMigrate(t, patch.Options{DryRun: dryRun, Out: out})
		},
	}
	cmd.Long = "keychain-migrate <app>: " + appArg()
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print every step without touching keychain items")
	cmd.Flags().StringVar(&appPath, "app-path", "", "override the target .app path for isolated testing")
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
