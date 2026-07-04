package patch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"goodkind.io/desktop-via-clyde/internal/targets"
)

func stepResignNestedCode(ctx context.Context, r *Runner, t targets.Target, id string, entFile string) error {
	codePaths, err := nestedCodeSignPaths(ctx, r, t)
	if err != nil {
		return err
	}
	propagateEntitlements := nestedNeedsEntitlementPropagation(t)
	for _, codePath := range codePaths {
		if !r.DryRun {
			if _, err := os.Stat(codePath); err != nil {
				if errors.Is(err, os.ErrNotExist) {
					notef(r, fmt.Sprintf("target=%s nested code object missing, skipping %s", t.ID, codePath))
					continue
				}
				return logPatchError(ctx, "patch.nested_code_stat_failed", fmt.Errorf("stat nested code object %s: %w", codePath, err))
			}
		}
		traceAction(r, actionSignNestedCode, t.ID, codePath)
		notef(r, fmt.Sprintf("target=%s re-sign nested code object %s", t.ID, codePath))
		args, err := resolveNestedCodeSignArgs(ctx, r, t, id, entFile, codePath, propagateEntitlements)
		if err != nil {
			return err
		}
		if err := r.Run(ctx, "/usr/bin/codesign", args...); err != nil {
			return logPatchError(ctx, "patch.sign_nested_code_failed", fmt.Errorf("sign nested code object %s: %w", codePath, err))
		}
	}
	return nil
}

// resolveNestedCodeSignArgs builds the codesign arguments for one nested code
// object. When the target must propagate its required boolean entitlements to
// child processes, the object is re-signed with its own entitlements augmented
// by the target policy so an inserted dylib survives hardened runtime into the
// extension host; otherwise it keeps the previous preserve-metadata behavior.
func resolveNestedCodeSignArgs(ctx context.Context, r *Runner, t targets.Target, id string, entFile string, codePath string, propagateEntitlements bool) ([]string, error) {
	if !propagateEntitlements {
		return nestedCodeSignArgs(id, entFile, codePath), nil
	}
	nestedEntFile, err := writeAugmentedEntitlementsFileAllowEmpty(
		ctx,
		r,
		"target="+t.ID+" nested "+filepath.Base(codePath),
		codePath,
		*t.Entitlements,
	)
	if err != nil {
		return nil, logPatchError(ctx, "patch.nested_entitlements_failed", fmt.Errorf("nested entitlements for %s: %w", codePath, err))
	}
	return codesignRuntimeEntitlementsArgs(id, nestedEntFile, codePath), nil
}

func nestedCodeSignArgs(id string, entFile string, codePath string) []string {
	if filepath.Ext(filepath.Clean(codePath)) == ".app" && entFile != "" {
		return codesignRuntimeEntitlementsArgs(id, entFile, codePath)
	}
	return []string{
		"--force",
		"--sign",
		id,
		"--options",
		"runtime",
		"--preserve-metadata=entitlements",
		codePath,
	}
}

// nestedNeedsEntitlementPropagation reports whether a target's nested code
// objects must carry the target's required boolean entitlements rather than
// only preserving their own.
//
// A proxy-injection target inserts a dylib through DYLD_INSERT_LIBRARIES that
// must reach child processes, notably the Electron extension-host helper that
// issues the model turn. Under hardened runtime a child process strips the
// insert unless its own code signature carries
// com.apple.security.cs.allow-dyld-environment-variables, so signing nested
// code objects with codesign --preserve-metadata=entitlements (which keeps a
// helper's original entitlements, lacking that key) leaves the insert stripped
// before it reaches the extension host. For these targets each nested object is
// instead re-signed with its own entitlements augmented by the target's
// required boolean entitlements, so the helper keeps its existing entitlements
// (allow-jit and friends) while gaining the ones that must propagate.
func nestedNeedsEntitlementPropagation(t targets.Target) bool {
	if t.Entitlements == nil || len(t.Entitlements.RequiredBooleanEntitlements) == 0 {
		return false
	}
	return t.DevelopmentSigning != nil && t.DevelopmentSigning.ProxyInjection
}
