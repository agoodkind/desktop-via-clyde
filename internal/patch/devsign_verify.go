package patch

import (
	"context"
	"errors"
	"fmt"
	"os"

	"goodkind.io/desktop-via-clyde/internal/devsign"
	"goodkind.io/desktop-via-clyde/internal/targets"
)

func verifyDevelopmentSignedInjector(ctx context.Context, r *Runner, t targets.Target) error {
	injectorPath := devsign.InjectorPath(t)
	policyPath := devsign.InjectorPolicyPath(t)
	appLocalPath := devsign.AppLocalInjectorPath(t)
	if _, err := os.Stat(appLocalPath); err == nil {
		return logPatchError(ctx, "patch.verify_app_local_injector_present", fmt.Errorf("stale app-local injector remains at %s", appLocalPath))
	} else if !errors.Is(err, os.ErrNotExist) {
		return logPatchError(ctx, "patch.verify_app_local_injector_stat_failed", fmt.Errorf("stat stale app-local injector %s: %w", appLocalPath, err))
	}
	if _, err := os.Stat(injectorPath); err != nil {
		return logPatchError(ctx, "patch.verify_external_injector_stat_failed", fmt.Errorf("stat external injector %s: %w", injectorPath, err))
	}
	if _, err := os.Stat(policyPath); err != nil {
		return logPatchError(ctx, "patch.verify_injector_policy_stat_failed", fmt.Errorf("stat injector policy %s: %w", policyPath, err))
	}
	if err := r.Run(ctx, "/usr/bin/codesign", "--verify", "--strict", "--verbose=2", injectorPath); err != nil {
		return logPatchError(ctx, "patch.verify_external_injector_signature_failed", fmt.Errorf("verify external injector %s: %w", injectorPath, err))
	}
	if err := devsign.SmokeTestInjector(ctx, injectorPath, policyPath); err != nil {
		return logPatchError(ctx, "patch.verify_external_injector_smoke_failed", fmt.Errorf("smoke-test external injector %s: %w", injectorPath, err))
	}
	return nil
}
