package computeruseext

import (
	"context"
	"fmt"
	"path/filepath"

	"goodkind.io/desktop-via-clyde/internal/patch"
	"goodkind.io/desktop-via-clyde/internal/targets"
)

func verifyComputerUseHelper(
	ctx context.Context,
	r *patch.Runner,
	appPath string,
	policy targets.ComputerUsePolicy,
	localTeamID string,
) error {
	if r.DryRun {
		recordComputerUseVerificationPreview(r, appPath, policy)
		return nil
	}
	return verifyComputerUseHelperWithDependencies(
		ctx,
		r,
		appPath,
		policy,
		localTeamID,
		defaultComputerUseVerificationDependencies(),
	)
}

func recordComputerUseVerificationPreview(
	r *patch.Runner,
	appPath string,
	policy targets.ComputerUsePolicy,
) {
	for _, target := range policy.SignTargets {
		codePath := computerUseSignTargetPath(appPath, target.Path)
		patch.RecordTrace(r, ActionPreviewVerifyComputerUseHelper, "", codePath)
	}
	for _, relPath := range policy.TeamPatchBinaries {
		binaryPath := filepath.Join(appPath, filepath.FromSlash(relPath))
		patch.RecordTrace(r, ActionPreviewVerifyComputerUseTrustedTeam, "", binaryPath)
	}
	for _, relPath := range policy.TeamRequirementPlists {
		plistPath := filepath.Join(appPath, filepath.FromSlash(relPath))
		patch.RecordTrace(r, ActionPreviewVerifyComputerUseRequirement, "", plistPath)
	}
}

func verifyComputerUseHelperWithDependencies(
	ctx context.Context,
	r *patch.Runner,
	appPath string,
	policy targets.ComputerUsePolicy,
	localTeamID string,
	dependencies computerUseVerificationDependencies,
) error {
	patch.Note(r, "computer-use: verify helper signature")
	if err := dependencies.verifyBundle(ctx, r, appPath); err != nil {
		return logComputerUsePatchError(ctx, "patch.computer_use_verify_bundle_failed", fmt.Errorf("verify helper bundle: %w", err))
	}
	if err := verifyComputerUseSignTargets(
		ctx,
		r,
		appPath,
		policy,
		localTeamID,
		dependencies.readSignature,
		dependencies.verifyRequired,
		dependencies.verifyAbsent,
	); err != nil {
		return err
	}
	return verifyComputerUseTrustSurfacesWithDependencies(ctx, r, appPath, policy, localTeamID, dependencies.readFile)
}

func verifyComputerUseTrustSurfacesWithDependencies(
	ctx context.Context,
	r *patch.Runner,
	appPath string,
	policy targets.ComputerUsePolicy,
	localTeamID string,
	readFile computerUseFileReader,
) error {
	for _, relPath := range policy.TeamPatchBinaries {
		binaryPath := filepath.Join(appPath, filepath.FromSlash(relPath))
		data, err := readFile(binaryPath)
		if err != nil {
			return logComputerUsePatchError(ctx, "patch.computer_use_verify_binary_read_failed",
				fmt.Errorf("read helper binary %s: %w", binaryPath, err))
		}
		upstreamCount, err := countTrustedTeamTokens(data, policy.UpstreamTrustedTeamID)
		if err != nil {
			return logComputerUsePatchError(ctx, "patch.computer_use_verify_binary_signature_range_failed",
				fmt.Errorf("read helper binary code signature ranges %s: %w", binaryPath, err))
		}
		if upstreamCount > 0 {
			return fmt.Errorf("%s still contains upstream trusted team %s", binaryPath, policy.UpstreamTrustedTeamID)
		}
		localCount, err := countTrustedTeamTokens(data, localTeamID)
		if err != nil {
			return logComputerUsePatchError(ctx, "patch.computer_use_verify_binary_signature_range_failed",
				fmt.Errorf("read helper binary code signature ranges %s: %w", binaryPath, err))
		}
		if localCount == 0 {
			return fmt.Errorf("%s does not contain local trusted team %s outside its code signature", binaryPath, localTeamID)
		}
		patch.RecordTrace(r, ActionVerifyComputerUseTrustedTeam, "", binaryPath)
	}
	for _, relPath := range policy.TeamRequirementPlists {
		plistPath := filepath.Join(appPath, filepath.FromSlash(relPath))
		data, err := readFile(plistPath)
		if err != nil {
			return logComputerUsePatchError(ctx, "patch.computer_use_verify_requirement_plist_read_failed", fmt.Errorf("read helper requirement plist %s: %w", plistPath, err))
		}
		teamID, err := teamRequirementPlistTeamID(data)
		if err != nil {
			return logComputerUsePatchError(ctx, "patch.computer_use_verify_requirement_team_read_failed", fmt.Errorf("read helper requirement plist team %s: %w", plistPath, err))
		}
		if teamID != localTeamID {
			return fmt.Errorf("%s trusts parent team %s, want %s", plistPath, teamID, localTeamID)
		}
		patch.RecordTrace(r, ActionVerifyComputerUseRequirement, "", plistPath)
	}
	return nil
}

func verifyComputerUseSignTargets(
	ctx context.Context,
	r *patch.Runner,
	appPath string,
	policy targets.ComputerUsePolicy,
	localTeamID string,
	readSignature computerUseSignatureReader,
	verifyRequired computerUseEntitlementVerifier,
	verifyAbsent computerUseEntitlementVerifier,
) error {
	for _, target := range policy.SignTargets {
		codePath := computerUseSignTargetPath(appPath, target.Path)
		signature, err := readSignature(ctx, codePath)
		if err != nil {
			return logComputerUsePatchError(ctx, "patch.computer_use_verify_identity_read_failed",
				fmt.Errorf("read helper identity %s: %w", codePath, err))
		}
		if signature.TeamID != localTeamID {
			return logComputerUsePatchError(ctx, "patch.computer_use_verify_identity_team_failed",
				fmt.Errorf("%s signed by team %s, want %s", codePath, signature.TeamID, localTeamID))
		}

		var required []string
		var absent []string
		if target.Entitlements != nil {
			required = target.Entitlements.RequiredBooleanEntitlements
			absent = target.Entitlements.Strip
		}
		if err := verifyRequired(ctx, r, codePath, required); err != nil {
			return logComputerUsePatchError(ctx, "patch.computer_use_verify_required_entitlements_failed",
				fmt.Errorf("verify required entitlements %s: %w", codePath, err))
		}
		if err := verifyAbsent(ctx, r, codePath, absent); err != nil {
			return logComputerUsePatchError(ctx, "patch.computer_use_verify_absent_entitlements_failed",
				fmt.Errorf("verify absent entitlements %s: %w", codePath, err))
		}
		patch.RecordTrace(r, ActionVerifyComputerUseHelper, "", codePath)
	}
	return nil
}
