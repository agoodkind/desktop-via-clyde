package computeruseext

import (
	"context"
	"fmt"

	"goodkind.io/desktop-via-clyde/internal/extensions"
	"goodkind.io/desktop-via-clyde/internal/patch"
	"goodkind.io/desktop-via-clyde/internal/targets"
)

// RegisterLifecycleHooks links Computer Use patch lifecycle hooks.
func RegisterLifecycleHooks() error {
	if err := patch.RegisterBundleExtension("computer-use-bundled", patch.BundleExtension{
		MutateBeforeSeal: MutateBundledComputerUse,
		VerifyAfterSeal:  VerifyBundledComputerUse,
	}); err != nil {
		return logComputerUseRegistrationError("register bundled Computer Use extension", err)
	}
	if err := patch.RegisterPostBundleHook("computer-use", LifecycleHook); err != nil {
		return logComputerUseRegistrationError("register Computer Use lifecycle hook", err)
	}
	return nil
}

// RegisterValidators links Computer Use config validation.
func RegisterValidators() error {
	if err := extensions.RegisterAppValidator("computer_use", extensions.ValidateComputerUse); err != nil {
		return logComputerUseRegistrationError("register Computer Use validator", err)
	}
	return nil
}

// MutateBundledComputerUse repairs a bundled Computer Use helper before app signing.
func MutateBundledComputerUse(
	ctx context.Context,
	r *patch.Runner,
	t targets.Target,
	_ patch.Options,
) error {
	if t.Extensions.ComputerUse == nil {
		return nil
	}
	policy := *t.Extensions.ComputerUse
	localTeamID, err := validateComputerUsePolicy(policy)
	if err != nil {
		return err
	}
	appPath := bundledComputerUseAppPath(t.AppPath, policy)
	patch.RecordTrace(r, ActionRepairBundledComputerUse, t.ID, appPath)
	patch.Note(r, fmt.Sprintf("target=%s repair bundled Computer Use helper at %s", t.ID, appPath))
	if !r.DryRun {
		if err := ensureComputerUseAppPath(appPath); err != nil {
			return err
		}
	}
	return mutateComputerUseBundle(ctx, r, appPath, policy, localTeamID)
}

// VerifyBundledComputerUse verifies the bundled Computer Use helper after app signing.
func VerifyBundledComputerUse(
	ctx context.Context,
	r *patch.Runner,
	t targets.Target,
	_ patch.Options,
) error {
	if t.Extensions.ComputerUse == nil {
		return nil
	}
	policy := *t.Extensions.ComputerUse
	localTeamID, err := validateComputerUsePolicy(policy)
	if err != nil {
		return err
	}
	appPath := bundledComputerUseAppPath(t.AppPath, policy)
	if !r.DryRun {
		if err := ensureComputerUseAppPath(appPath); err != nil {
			return err
		}
	}
	return verifyComputerUseHelper(ctx, r, appPath, policy, localTeamID)
}
