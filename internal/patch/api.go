package patch

import (
	"context"

	"goodkind.io/desktop-via-clyde/internal/targets"
)

const (
	// ActionRestorePreservedNestedCode records a preserved nested code restore step.
	ActionRestorePreservedNestedCode = actionRestorePreservedNestedCode
	// ActionSignBundle records a bundle signing step.
	ActionSignBundle = actionSignBundle
	// ActionSignNestedCode records a nested code signing step.
	ActionSignNestedCode = actionSignNestedCode
)

// Note writes one patch progress note.
func Note(r *Runner, message string) {
	notef(r, message)
}

// RecordTrace appends one patch trace event.
func RecordTrace(r *Runner, action Action, target string, path string) {
	traceAction(r, action, target, path)
}

// LogError records and returns one patch error.
func LogError(ctx context.Context, event string, err error) error {
	return logPatchError(ctx, event, err)
}

// LogErrorNoContext records and returns one patch error without a context.
func LogErrorNoContext(event string, err error) error {
	return logPatchErrorNoContext(event, err)
}

// ResolveSignIdentity returns the signing identity used by patch flows.
func ResolveSignIdentity(ctx context.Context, dryRun bool) (string, error) {
	return resolveSignIdentity(ctx, dryRun)
}

// WriteAugmentedEntitlementsFileAllowEmpty writes a patched entitlements file.
func WriteAugmentedEntitlementsFileAllowEmpty(
	ctx context.Context,
	r *Runner,
	label string,
	source string,
	policy targets.EntitlementsPolicy,
) (string, error) {
	return writeAugmentedEntitlementsFileAllowEmpty(ctx, r, label, source, policy)
}

// VerifyBooleanEntitlements verifies boolean entitlement values on code.
func VerifyBooleanEntitlements(
	ctx context.Context,
	r *Runner,
	codePath string,
	required []string,
) error {
	return verifyBooleanEntitlements(ctx, r, codePath, required)
}

// VerifyAbsentEntitlements verifies stripped entitlements are absent.
func VerifyAbsentEntitlements(
	ctx context.Context,
	r *Runner,
	codePath string,
	absent []string,
) error {
	return verifyAbsentEntitlements(ctx, r, codePath, absent)
}

// CodesignRuntimeArgs returns codesign arguments for runtime signing.
func CodesignRuntimeArgs(id string, codePath string) []string {
	return codesignRuntimeArgs(id, codePath)
}

// CodesignRuntimeEntitlementsArgs returns codesign arguments with entitlements.
func CodesignRuntimeEntitlementsArgs(id string, entFile string, codePath string) []string {
	return codesignRuntimeEntitlementsArgs(id, entFile, codePath)
}
