package patch

import (
	"context"

	"goodkind.io/desktop-via-clyde/internal/targets"
)

const (
	ActionRestorePreservedNestedCode = actionRestorePreservedNestedCode
	ActionSignBundle                 = actionSignBundle
	ActionSignNestedCode             = actionSignNestedCode
)

func Note(r *Runner, message string) {
	notef(r, message)
}

func RecordTrace(r *Runner, action Action, target string, path string) {
	traceAction(r, action, target, path)
}

func LogError(ctx context.Context, event string, err error) error {
	return logPatchError(ctx, event, err)
}

func LogErrorNoContext(event string, err error) error {
	return logPatchErrorNoContext(event, err)
}

func ResolveSignIdentity(ctx context.Context, dryRun bool) (string, error) {
	return resolveSignIdentity(ctx, dryRun)
}

func WriteAugmentedEntitlementsFileAllowEmpty(
	ctx context.Context,
	r *Runner,
	label string,
	source string,
	policy targets.EntitlementsPolicy,
) (string, error) {
	return writeAugmentedEntitlementsFileAllowEmpty(ctx, r, label, source, policy)
}

func VerifyBooleanEntitlements(
	ctx context.Context,
	r *Runner,
	codePath string,
	required []string,
) error {
	return verifyBooleanEntitlements(ctx, r, codePath, required)
}

func VerifyAbsentEntitlements(
	ctx context.Context,
	r *Runner,
	codePath string,
	absent []string,
) error {
	return verifyAbsentEntitlements(ctx, r, codePath, absent)
}

func CodesignRuntimeArgs(id string, codePath string) []string {
	return codesignRuntimeArgs(id, codePath)
}

func CodesignRuntimeEntitlementsArgs(id string, entFile string, codePath string) []string {
	return codesignRuntimeEntitlementsArgs(id, entFile, codePath)
}
