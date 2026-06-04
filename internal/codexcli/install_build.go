package codexcli

import (
	"context"
	"fmt"
	"strings"

	"goodkind.io/desktop-via-clyde/internal/clock"
	"goodkind.io/desktop-via-clyde/internal/patch"
)

func resolveCodexBuildIdentity(
	ctx context.Context,
	r *patch.Runner,
	opts InstallOptions,
	target string,
	buildMode BuildMode,
) (codexBuildIdentity, error) {
	log := codexcliLog.With("function", "resolveCodexBuildIdentity")
	head := "dryrun"
	tree := "dryrun"
	baseVersion := "0.0.0"
	buildTime := clock.Now()
	if opts.DryRun {
		buildTime = dryRunBuildTime()
	} else {
		resolvedHead, resolvedTree, resolvedBaseVersion, err := readSourceBuildInputs(ctx, r, opts.SourceDir)
		if err != nil {
			log.ErrorContext(ctx, "codexcli.resolve_build_identity.source_inputs_failed", "err", err)
			return emptyCodexBuildIdentity(), err
		}
		head = resolvedHead
		tree = resolvedTree
		baseVersion = resolvedBaseVersion
	}
	return newBuildIdentity(
		baseVersion,
		head,
		tree,
		target,
		buildMode,
		opts.PackageVariant,
		opts.CommandName,
		buildTime,
	), nil
}

func emptyCodexBuildIdentity() codexBuildIdentity {
	return codexBuildIdentity{BaseVersion: "", PackageVersion: "", BuildStamp: "", Head: "", Tree: "", BuildHash: ""}
}

func readSourceBuildInputs(ctx context.Context, r *patch.Runner, sourceDir string) (string, string, string, error) {
	log := codexcliLog.With("function", "readSourceBuildInputs")
	headBytes, err := r.RunCaptureStdout(ctx, "git", "-C", sourceDir, "rev-parse", "--short=12", "HEAD")
	if err != nil {
		log.ErrorContext(ctx, "codexcli.read_source_build_inputs.head_failed", "err", err)
		return "", "", "", fmt.Errorf("read Codex source HEAD: %w", err)
	}
	head := strings.TrimSpace(string(headBytes))
	notef(r, "codex-cli: source checkout is at HEAD "+head)
	treeBytes, err := r.RunCaptureStdout(ctx, "git", "-C", sourceDir, "rev-parse", "HEAD^{tree}")
	if err != nil {
		log.ErrorContext(ctx, "codexcli.read_source_build_inputs.tree_failed", "err", err)
		return "", "", "", fmt.Errorf("read Codex source tree: %w", err)
	}
	tree := strings.TrimSpace(string(treeBytes))
	baseVersion, err := latestStableRustVersion(ctx, r, sourceDir)
	if err != nil {
		return "", "", "", err
	}
	notef(r, "codex-cli: latest stable Codex release is "+baseVersion)
	return head, tree, baseVersion, nil
}

func buildStampedPackage(
	ctx context.Context,
	r *patch.Runner,
	opts InstallOptions,
	target string,
	buildMode BuildMode,
	identity codexBuildIdentity,
) (packageMetadata, error) {
	buildSourceDir, err := prepareStampedBuildSource(ctx, r, opts.SourceDir, target, buildMode, identity)
	if err != nil {
		return packageMetadata{}, err
	}
	notef(r, "codex-cli: build upstream entrypoint")
	entrypointPath, err := buildEntrypoint(ctx, r, buildSourceDir, opts.CommandName, target, buildMode, opts.NoSccache)
	if err != nil {
		return packageMetadata{}, err
	}
	notef(r, "codex-cli: sign upstream entrypoint")
	if err := signBinary(ctx, r, buildSourceDir, entrypointPath); err != nil {
		return packageMetadata{}, err
	}
	notef(r, "codex-cli: build upstream package")
	if err := buildPackage(ctx, r, buildSourceDir, opts.PackageDir, opts.PackageVariant, target, entrypointPath); err != nil {
		return packageMetadata{}, err
	}
	return readStampedPackageMetadata(r, opts, target, identity)
}

func readStampedPackageMetadata(
	r *patch.Runner,
	opts InstallOptions,
	target string,
	identity codexBuildIdentity,
) (packageMetadata, error) {
	notef(r, "codex-cli: read package metadata")
	metadata := packageMetadata{
		Version: identity.PackageVersion,
		Target:  target,
		Variant: opts.PackageVariant,
	}
	if opts.DryRun {
		return metadata, nil
	}
	metadata, err := readPackageMetadata(opts.PackageDir, opts.PackageVariant)
	if err != nil {
		return packageMetadata{}, err
	}
	if metadata.Version != identity.PackageVersion {
		return packageMetadata{}, fmt.Errorf("package version mismatch: got %s want %s", metadata.Version, identity.PackageVersion)
	}
	return metadata, nil
}
