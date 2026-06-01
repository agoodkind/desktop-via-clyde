// Package codexclishim links the bundled Codex CLI app-server wrapper into the
// desktop-via-clyde monolith.
package codexclishim

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"

	"goodkind.io/desktop-via-clyde/internal/catalog"
	"goodkind.io/desktop-via-clyde/internal/extensions"
	"goodkind.io/desktop-via-clyde/internal/helperdispatch"
	"goodkind.io/desktop-via-clyde/internal/monolith"
	"goodkind.io/desktop-via-clyde/internal/patch"
	"goodkind.io/desktop-via-clyde/internal/paths"
	"goodkind.io/desktop-via-clyde/internal/spec"
	"goodkind.io/desktop-via-clyde/internal/targets"
)

var codexCLIShimLog = slog.With("component", "desktop-via-clyde", "subcomponent", "codex-cli-shim")

const (
	// EnvRealCLI stores the selected app bundle's real bundled Codex CLI path.
	EnvRealCLI = "DVC_CODEX_REAL_CLI"
	// EnvChatGPTBaseURL stores the dedicated Clyde Codex backend URL.
	EnvChatGPTBaseURL = "DVC_CODEX_CHATGPT_BASE_URL"
	// EnvCLIPath tells Codex Desktop which CLI executable Electron should spawn.
	EnvCLIPath = "CODEX_CLI_PATH"
	// HelperName is the monolith helper entrypoint name installed for Codex.
	HelperName = "dvc-codex-cli-shim"
	// HookCapability is the config capability name for this launch-policy hook.
	HookCapability = "codex-cli-shim"
)

// RegisterHelper links the Codex CLI helper entrypoint into the monolith.
func RegisterHelper() error {
	if err := helperdispatch.Register(HelperName, func() (int, bool) {
		if filepath.Base(os.Args[0]) != HelperName {
			return 0, false
		}
		return Run(), true
	}); err != nil {
		return logCodexCLIShimRegistrationError("register Codex CLI helper", err)
	}
	return nil
}

// RegisterValidators links config validation for this extension.
func RegisterValidators() error {
	if err := extensions.RegisterAppValidator("codex_cli_shim", extensions.ValidateCodexCLIShim); err != nil {
		return logCodexCLIShimRegistrationError("register Codex CLI shim validator", err)
	}
	return nil
}

// RegisterPreLaunchPolicyHooks links launch policy mutation for this extension.
func RegisterPreLaunchPolicyHooks() error {
	if !catalog.HasPreLaunchPolicyHookCapability(HookCapability) {
		if err := catalog.RegisterPreLaunchPolicyHookCapability(HookCapability); err != nil {
			return logCodexCLIShimRegistrationError("register Codex CLI shim capability", err)
		}
	}
	if err := patch.RegisterPreLaunchPolicyHook(HookCapability, PreLaunchPolicyHook); err != nil {
		return logCodexCLIShimRegistrationError("register Codex CLI shim launch-policy hook", err)
	}
	return nil
}

// PreLaunchPolicyHook installs the monolith-backed wrapper and appends launch-policy env.
func PreLaunchPolicyHook(
	ctx context.Context,
	runner *patch.Runner,
	target *targets.Target,
	opts patch.Options,
) error {
	if target.Extensions.CodexCLIShim == nil {
		return nil
	}
	wrapperPath := InstalledPath()
	patch.Note(runner, fmt.Sprintf("target=%s install Codex CLI wrapper -> %s", target.ID, wrapperPath))
	if !opts.DryRun {
		if err := os.MkdirAll(filepath.Dir(wrapperPath), 0o755); err != nil {
			codexCLIShimLog.ErrorContext(ctx, "codexclishim.wrapper_dir_failed", "path", filepath.Dir(wrapperPath), "err", err)
			return fmt.Errorf("create wrapper dir: %w", err)
		}
		if err := monolith.CopyTo(wrapperPath); err != nil {
			codexCLIShimLog.ErrorContext(ctx, "codexclishim.wrapper_install_failed", "path", wrapperPath, "err", err)
			return fmt.Errorf("install wrapper: %w", err)
		}
	}
	realCLI := filepath.Join(target.AppPath, "Contents", "Resources", "codex")
	target.LaunchPolicy.Environment = upsertEnv(target.LaunchPolicy.Environment, spec.EnvActionSpec{
		Action: "set",
		Key:    EnvCLIPath,
		Value:  wrapperPath,
	})
	target.LaunchPolicy.Environment = upsertEnv(target.LaunchPolicy.Environment, spec.EnvActionSpec{
		Action: "set",
		Key:    EnvRealCLI,
		Value:  realCLI,
	})
	target.LaunchPolicy.Environment = upsertEnv(target.LaunchPolicy.Environment, spec.EnvActionSpec{
		Action: "set",
		Key:    EnvChatGPTBaseURL,
		Value:  target.Extensions.CodexCLIShim.ChatGPTBaseURL,
	})
	return nil
}

// InstalledPath returns the stable helper path installed outside app bundles.
func InstalledPath() string {
	return filepath.Join(paths.StateRoot(), "desktop-via-clyde-helpers", HelperName)
}

// Run executes the Codex CLI wrapper entrypoint.
func Run() int {
	if err := RunWith(os.Args[1:], os.Getenv, syscall.Exec, os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "dvc-codex-cli-shim: %v\n", err)
		return 127
	}
	return 0
}

type getenvFunc func(string) string

type execFunc func(string, []string, []string) error

// RunWith rewrites app-server invocations and execs the real bundled CLI.
func RunWith(args []string, getenv getenvFunc, execve execFunc, environ []string) error {
	realCLI := getenv(EnvRealCLI)
	if realCLI == "" {
		return fmt.Errorf("%s is required", EnvRealCLI)
	}
	execArgs, err := RewrittenArgs(args, getenv(EnvChatGPTBaseURL))
	if err != nil {
		return err
	}
	return execve(realCLI, append([]string{realCLI}, execArgs...), environ)
}

// RewrittenArgs adds the app-server ChatGPT base override when required.
func RewrittenArgs(args []string, chatGPTBaseURL string) ([]string, error) {
	if len(args) == 0 || args[0] != "app-server" {
		return append([]string(nil), args...), nil
	}
	if chatGPTBaseURL == "" {
		return nil, fmt.Errorf("%s is required for app-server", EnvChatGPTBaseURL)
	}
	rewritten := []string{
		"-c",
		"chatgpt_base_url=" + chatGPTBaseURL,
	}
	rewritten = append(rewritten, args...)
	return rewritten, nil
}

func upsertEnv(actions []spec.EnvActionSpec, action spec.EnvActionSpec) []spec.EnvActionSpec {
	for index, candidate := range actions {
		if candidate.Key == action.Key {
			actions[index] = action
			return actions
		}
	}
	return append(actions, action)
}

func logCodexCLIShimRegistrationError(message string, err error) error {
	codexCLIShimLog.Error("codexclishim.registration_failed", "message", message, "err", err)
	return fmt.Errorf("%s: %w", message, err)
}
