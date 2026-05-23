package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"

	"goodkind.io/desktop-via-clyde/internal/patch"
	"goodkind.io/desktop-via-clyde/internal/targets"
)

// hookRequestEnvelope mirrors the JSON object clyde writes on stdin
// when it forks a configured [[mitm.hook]] subprocess. The canonical
// definition lives at internal/mitm/hook.go in the clyde repository;
// the duplication here exists because the two repositories share no
// Go module, only the on-the-wire envelope shape.
type hookRequestEnvelope struct {
	Mode                     string      `json:"mode"`
	RequestID                string      `json:"request_id"`
	RuleName                 string      `json:"rule_name"`
	RequestURL               string      `json:"request_url"`
	RequestMethod            string      `json:"request_method"`
	RequestHeaders           http.Header `json:"request_headers"`
	RequestBodyPath          string      `json:"request_body_path"`
	UpstreamResponseStatus   int         `json:"upstream_response_status,omitempty"`
	UpstreamResponseHeaders  http.Header `json:"upstream_response_headers,omitempty"`
	UpstreamResponseBodyPath string      `json:"upstream_response_body_path,omitempty"`
	OutputBodyPath           string      `json:"output_body_path"`
}

// hookResponseEnvelope mirrors the JSON object clyde reads from
// stdout after the hook subprocess produces its OutputBodyPath. See
// hookRequestEnvelope for the contract-source note.
type hookResponseEnvelope struct {
	Status  int         `json:"status"`
	Headers http.Header `json:"headers,omitempty"`
}

// newMITMHookCmd builds the hidden Cobra subcommand the clyde MITM
// proxy forks when an intercepted Cursor (or other registered)
// update download matches the configured [[mitm.hook]] rule. The
// subcommand reads the JSON envelope on stdin, re-patches the
// freshly downloaded update bundle in a staging directory, and
// streams the patched zip back to clyde via the OutputBodyPath named
// in the envelope.
func newMITMHookCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "mitm-hook patch-bundle <target>",
		Short:  "Run as a clyde MITM hook subprocess to re-patch a downloaded update bundle",
		Hidden: true,
		Args:   cobra.MinimumNArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			if args[0] != "patch-bundle" {
				return fmt.Errorf("unknown mitm-hook subcommand: %s", args[0])
			}
			return runMITMHookPatchBundle(args[1])
		},
	}
}

func runMITMHookPatchBundle(targetID string) error {
	t, err := targets.Lookup(targetID)
	if err != nil {
		return err
	}
	env, err := readHookRequest()
	if err != nil {
		return err
	}
	if env.Mode != "transform_response" {
		return fmt.Errorf("mitm-hook patch-bundle only supports transform_response, got %q", env.Mode)
	}
	if env.UpstreamResponseBodyPath == "" {
		return fmt.Errorf("mitm-hook envelope missing upstream_response_body_path")
	}
	if env.OutputBodyPath == "" {
		return fmt.Errorf("mitm-hook envelope missing output_body_path")
	}
	originalDR, err := readOriginalDR(t)
	if err != nil {
		return err
	}
	stagingDir, err := os.MkdirTemp("", "dvc-hook-"+t.ID+"-")
	if err != nil {
		return fmt.Errorf("create staging dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(stagingDir) }()

	extractedApp, err := extractUpdateBundle(env.UpstreamResponseBodyPath, stagingDir, t)
	if err != nil {
		return err
	}
	if err := verifyBundleSatisfiesDR(extractedApp, originalDR); err != nil {
		return fmt.Errorf("verify upstream DR: %w", err)
	}
	if err := repatchExtractedBundle(t, extractedApp); err != nil {
		return err
	}
	if err := rezipBundle(extractedApp, env.OutputBodyPath); err != nil {
		return err
	}
	return writeHookResponse()
}

// readHookRequest decodes the JSON envelope clyde writes on stdin.
// The hook subprocess only reads one envelope per fork, so the full
// stdin can be JSON-decoded directly.
func readHookRequest() (hookRequestEnvelope, error) {
	env := hookRequestEnvelope{}
	if err := json.NewDecoder(os.Stdin).Decode(&env); err != nil {
		return env, fmt.Errorf("decode hook request envelope: %w", err)
	}
	return env, nil
}

// readOriginalDR loads the stored DesignatedRequirement string from
// state.json. Returns a typed error when the entry or the field is
// missing because the hook must refuse to operate on an
// unverified-DR target rather than silently Goodkind-sign whatever
// upstream zip happens to arrive.
func readOriginalDR(t targets.Target) (string, error) {
	return patch.EnsureOriginalDesignatedRequirement(t)
}

// extractUpdateBundle unzips the upstream-supplied update zip into
// the staging directory using /usr/bin/ditto, which preserves Apple
// bundle metadata (extended attributes, symlinks, finder info) that
// archive/zip mangles. Returns the absolute path of the extracted
// .app inside the staging directory.
func extractUpdateBundle(zipPath, stagingDir string, t targets.Target) (string, error) {
	extractDir := filepath.Join(stagingDir, "extracted")
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		return "", fmt.Errorf("create extract dir: %w", err)
	}
	cmd := exec.Command("/usr/bin/ditto", "-x", "-k", zipPath, extractDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("ditto -x -k failed: %w (output=%q)", err, string(out))
	}
	appName := filepath.Base(t.AppPath)
	extractedApp := filepath.Join(extractDir, appName)
	if _, err := os.Stat(extractedApp); err != nil {
		return "", fmt.Errorf("expected %s inside upstream zip, not found: %w", appName, err)
	}
	return extractedApp, nil
}

// verifyBundleSatisfiesDR runs `codesign --verify --deep --strict
// --requirement <dr>` against the extracted bundle. The DR string is
// the one captured at initial patch time from the upstream-signed
// /Applications copy; if the freshly downloaded zip is not signed by
// the same upstream identity, codesign fails and the hook aborts
// before any Goodkind re-signing happens.
func verifyBundleSatisfiesDR(appPath, requirement string) error {
	cmd := exec.Command("/usr/bin/codesign", "--verify", "--deep", "--strict", "-R="+requirement, appPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("codesign --verify --deep --strict -R=%q %s: %w (output=%q)", requirement, appPath, err, string(out))
	}
	return nil
}

// repatchExtractedBundle runs the shared bundle-mutation steps
// against the extracted upstream bundle, leaving the bundle
// Goodkind-signed so Squirrel.Mac's downstream DR check (which
// compares against the running Goodkind-signed /Applications copy)
// succeeds.
func repatchExtractedBundle(t targets.Target, appPath string) error {
	hookTarget := t
	hookTarget.AppPath = appPath
	return patch.PatchExtractedBundle(hookTarget, patch.BundleOptions{
		DryRun: false,
		Out:    os.Stderr,
	})
}

// rezipBundle re-creates the wire-format zip Squirrel.Mac expects.
// `ditto -c -k --sequesterRsrc --keepParent` matches the upstream
// archive shape: the zip contains the .app at its root rather than
// inside an extra directory level. The resulting bytes go to the
// path the hook envelope named in OutputBodyPath; clyde streams that
// file back on the original HTTP response to Cursor.
func rezipBundle(appPath, outputPath string) error {
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}
	cmd := exec.Command("/usr/bin/ditto", "-c", "-k", "--sequesterRsrc", "--keepParent", appPath, outputPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ditto -c -k --sequesterRsrc --keepParent failed: %w (output=%q)", err, string(out))
	}
	return nil
}

// writeHookResponse prints the success envelope clyde expects on
// stdout. The status is 200 and the content-type is application/zip
// so clyde forwards the right header to Squirrel.Mac when it streams
// the re-zipped patched bundle back on the original response
// connection.
func writeHookResponse() error {
	resp := hookResponseEnvelope{
		Status: 200,
		Headers: http.Header{
			"Content-Type": []string{"application/zip"},
		},
	}
	if err := json.NewEncoder(os.Stdout).Encode(resp); err != nil {
		return fmt.Errorf("write hook response envelope: %w", err)
	}
	return nil
}
