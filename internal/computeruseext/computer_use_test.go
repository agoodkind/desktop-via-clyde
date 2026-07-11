package computeruseext

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
	"goodkind.io/desktop-via-clyde/internal/bundleidentity"
	"goodkind.io/desktop-via-clyde/internal/config"
	"goodkind.io/desktop-via-clyde/internal/extensions"
	"goodkind.io/desktop-via-clyde/internal/patch"
	"goodkind.io/desktop-via-clyde/internal/spec"
	"goodkind.io/desktop-via-clyde/internal/targets"
)

func TestVerifyComputerUseSignTargetsChecksEveryIdentityAndEntitlementRule(t *testing.T) {
	appPath := filepath.Join(t.TempDir(), "Codex Computer Use.app")
	policy := targets.ComputerUsePolicy{
		SignTargets: []targets.ComputerUseSignTarget{
			{Path: "Contents/AuthorizationPlugin.bundle"},
			{Path: "Contents/Installer.app"},
			{
				Path: "Contents/Client.app",
				Entitlements: &extensions.EntitlementsPolicy{
					Strip:                       []string{"com.apple.security.application-groups"},
					RequiredBooleanEntitlements: []string{"com.apple.security.automation.apple-events"},
				},
			},
			{
				Path: "Contents/Guardian.app",
				Entitlements: &extensions.EntitlementsPolicy{
					Strip: []string{"com.apple.security.application-groups"},
				},
			},
			{
				Path: ".",
				Entitlements: &extensions.EntitlementsPolicy{
					Strip: []string{"com.apple.security.application-groups"},
					RequiredBooleanEntitlements: []string{
						"com.apple.security.automation.apple-events",
						"com.apple.security.device.audio-input",
					},
				},
			},
		},
	}
	wantPaths := make([]string, 0, len(policy.SignTargets))
	for _, target := range policy.SignTargets {
		wantPaths = append(wantPaths, computerUseSignTargetPath(appPath, target.Path))
	}

	identityPaths := make([]string, 0, len(wantPaths))
	requiredChecks := make([]string, 0, len(wantPaths))
	absentChecks := make([]string, 0, len(wantPaths))
	runner := patch.NewRunner(context.Background(), false, io.Discard)
	err := verifyComputerUseSignTargets(
		context.Background(),
		runner,
		appPath,
		policy,
		"H3BMXM4W7H",
		func(_ context.Context, codePath string) (bundleidentity.Signature, error) {
			identityPaths = append(identityPaths, codePath)
			return bundleidentity.Signature{TeamID: "H3BMXM4W7H"}, nil
		},
		func(_ context.Context, _ *patch.Runner, codePath string, required []string) error {
			requiredChecks = append(requiredChecks, codePath+":"+strings.Join(required, ","))
			return nil
		},
		func(_ context.Context, _ *patch.Runner, codePath string, absent []string) error {
			absentChecks = append(absentChecks, codePath+":"+strings.Join(absent, ","))
			return nil
		},
	)
	if err != nil {
		t.Fatalf("verifyComputerUseSignTargets: %v", err)
	}
	if !stringSlicesEqual(identityPaths, wantPaths) {
		t.Fatalf("identity paths = %v, want %v", identityPaths, wantPaths)
	}
	wantRequired := []string{
		wantPaths[0] + ":",
		wantPaths[1] + ":",
		wantPaths[2] + ":com.apple.security.automation.apple-events",
		wantPaths[3] + ":",
		wantPaths[4] + ":com.apple.security.automation.apple-events,com.apple.security.device.audio-input",
	}
	if !stringSlicesEqual(requiredChecks, wantRequired) {
		t.Fatalf("required entitlement checks = %v, want %v", requiredChecks, wantRequired)
	}
	wantAbsent := []string{
		wantPaths[0] + ":",
		wantPaths[1] + ":",
		wantPaths[2] + ":com.apple.security.application-groups",
		wantPaths[3] + ":com.apple.security.application-groups",
		wantPaths[4] + ":com.apple.security.application-groups",
	}
	if !stringSlicesEqual(absentChecks, wantAbsent) {
		t.Fatalf("absent entitlement checks = %v, want %v", absentChecks, wantAbsent)
	}
}

func TestVerifyComputerUseSignTargetsRejectsNonlocalTeam(t *testing.T) {
	appPath := filepath.Join(t.TempDir(), "Codex Computer Use.app")
	policy := targets.ComputerUsePolicy{
		SignTargets: []targets.ComputerUseSignTarget{{Path: "Contents/Client.app"}},
	}
	runner := patch.NewRunner(context.Background(), false, io.Discard)
	err := verifyComputerUseSignTargets(
		context.Background(),
		runner,
		appPath,
		policy,
		"H3BMXM4W7H",
		func(_ context.Context, _ string) (bundleidentity.Signature, error) {
			return bundleidentity.Signature{TeamID: "2DC432GLL2"}, nil
		},
		func(_ context.Context, _ *patch.Runner, _ string, _ []string) error { return nil },
		func(_ context.Context, _ *patch.Runner, _ string, _ []string) error { return nil },
	)
	if err == nil {
		t.Fatal("verifyComputerUseSignTargets unexpectedly accepted upstream team")
	}
	want := fmt.Sprintf("%s signed by team 2DC432GLL2, want H3BMXM4W7H", filepath.Join(appPath, "Contents/Client.app"))
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %q, want fragment %q", err, want)
	}
}

func TestVerifyComputerUseSignTargetsRejectsEntitlementDrift(t *testing.T) {
	appPath := filepath.Join(t.TempDir(), "Codex Computer Use.app")
	policy := targets.ComputerUsePolicy{
		SignTargets: []targets.ComputerUseSignTarget{
			{
				Path: ".",
				Entitlements: &extensions.EntitlementsPolicy{
					Strip:                       []string{"com.apple.security.application-groups"},
					RequiredBooleanEntitlements: []string{"com.apple.security.automation.apple-events"},
				},
			},
		},
	}
	runner := patch.NewRunner(context.Background(), false, io.Discard)
	readLocalSignature := func(_ context.Context, _ string) (bundleidentity.Signature, error) {
		return bundleidentity.Signature{TeamID: "H3BMXM4W7H"}, nil
	}

	t.Run("required entitlement", func(t *testing.T) {
		err := verifyComputerUseSignTargets(
			context.Background(),
			runner,
			appPath,
			policy,
			"H3BMXM4W7H",
			readLocalSignature,
			func(_ context.Context, _ *patch.Runner, _ string, _ []string) error {
				return fmt.Errorf("missing Apple Events")
			},
			func(_ context.Context, _ *patch.Runner, _ string, _ []string) error { return nil },
		)
		if err == nil || !strings.Contains(err.Error(), "verify required entitlements") {
			t.Fatalf("verifyComputerUseSignTargets error = %v, want required entitlement rejection", err)
		}
	})

	t.Run("stripped entitlement", func(t *testing.T) {
		err := verifyComputerUseSignTargets(
			context.Background(),
			runner,
			appPath,
			policy,
			"H3BMXM4W7H",
			readLocalSignature,
			func(_ context.Context, _ *patch.Runner, _ string, _ []string) error { return nil },
			func(_ context.Context, _ *patch.Runner, _ string, _ []string) error {
				return fmt.Errorf("application group remains")
			},
		)
		if err == nil || !strings.Contains(err.Error(), "verify absent entitlements") {
			t.Fatalf("verifyComputerUseSignTargets error = %v, want stripped entitlement rejection", err)
		}
	})
}

func TestVerifyComputerUseHelperEmitsSuccessOnlyAfterCompletedChecks(t *testing.T) {
	appPath := filepath.Join(t.TempDir(), "Codex Computer Use.app")
	policy := targets.ComputerUsePolicy{
		UpstreamTrustedTeamID: "2DC432GLL2",
		SignTargets: []targets.ComputerUseSignTarget{
			{
				Path: "Contents/Client.app",
				Entitlements: &extensions.EntitlementsPolicy{
					Strip:                       []string{"com.apple.security.application-groups"},
					RequiredBooleanEntitlements: []string{"com.apple.security.automation.apple-events"},
				},
			},
		},
		TeamPatchBinaries:     []string{"Contents/MacOS/SkyComputerUseService"},
		TeamRequirementPlists: []string{"Contents/Resources/Client_Parent.coderequirement"},
	}
	writeComputerUseTrustFixtures(t, appPath, policy, "H3BMXM4W7H")

	trace := &patch.Trace{}
	runner := patch.NewRunner(context.Background(), false, io.Discard)
	runner.Trace = trace
	codePath := filepath.Join(appPath, "Contents/Client.app")
	assertNotVerified := func(action patch.Action) {
		t.Helper()
		if countTraceAction(trace, action) != 0 {
			t.Fatalf("success action %s emitted before its check completed: %#v", action, trace.Events)
		}
	}
	err := verifyComputerUseHelperWithDependencies(
		context.Background(),
		runner,
		appPath,
		policy,
		"H3BMXM4W7H",
		computerUseVerificationDependencies{
			verifyBundle: func(_ context.Context, _ *patch.Runner, gotPath string) error {
				if gotPath != appPath {
					t.Fatalf("verify bundle path = %s, want %s", gotPath, appPath)
				}
				return nil
			},
			readSignature: func(_ context.Context, gotPath string) (bundleidentity.Signature, error) {
				assertNotVerified(ActionVerifyComputerUseHelper)
				if gotPath != codePath {
					t.Fatalf("signature path = %s, want %s", gotPath, codePath)
				}
				return bundleidentity.Signature{TeamID: "H3BMXM4W7H"}, nil
			},
			verifyRequired: func(_ context.Context, _ *patch.Runner, _ string, _ []string) error {
				assertNotVerified(ActionVerifyComputerUseHelper)
				return nil
			},
			verifyAbsent: func(_ context.Context, _ *patch.Runner, _ string, _ []string) error {
				assertNotVerified(ActionVerifyComputerUseHelper)
				return nil
			},
			readFile: func(path string) ([]byte, error) {
				if strings.HasSuffix(path, "SkyComputerUseService") {
					assertNotVerified(ActionVerifyComputerUseTrustedTeam)
				} else {
					assertNotVerified(ActionVerifyComputerUseRequirement)
				}
				return os.ReadFile(path)
			},
		},
	)
	if err != nil {
		t.Fatalf("verifyComputerUseHelperWithDependencies: %v", err)
	}
	for _, action := range []patch.Action{
		ActionVerifyComputerUseHelper,
		ActionVerifyComputerUseTrustedTeam,
		ActionVerifyComputerUseRequirement,
	} {
		if got := countTraceAction(trace, action); got != 1 {
			t.Fatalf("trace action %s count = %d, want 1; events=%#v", action, got, trace.Events)
		}
	}
}

func TestVerifyComputerUseHelperDoesNotEmitSuccessForFailedCheck(t *testing.T) {
	verificationError := errors.New("verification failed")
	appPath := filepath.Join(t.TempDir(), "Codex Computer Use.app")
	codePath := filepath.Join(appPath, "Contents/Client.app")
	policy := targets.ComputerUsePolicy{
		UpstreamTrustedTeamID: "2DC432GLL2",
		SignTargets:           []targets.ComputerUseSignTarget{{Path: "Contents/Client.app"}},
	}

	for _, testCase := range []struct {
		name         string
		dependencies computerUseVerificationDependencies
	}{
		{
			name: "identity",
			dependencies: computerUseVerificationDependencies{
				readSignature: func(context.Context, string) (bundleidentity.Signature, error) {
					return bundleidentity.Signature{}, verificationError
				},
			},
		},
		{
			name: "required entitlement",
			dependencies: computerUseVerificationDependencies{
				readSignature: func(context.Context, string) (bundleidentity.Signature, error) {
					return bundleidentity.Signature{TeamID: "H3BMXM4W7H"}, nil
				},
				verifyRequired: func(context.Context, *patch.Runner, string, []string) error {
					return verificationError
				},
			},
		},
		{
			name: "absent entitlement",
			dependencies: computerUseVerificationDependencies{
				readSignature: func(context.Context, string) (bundleidentity.Signature, error) {
					return bundleidentity.Signature{TeamID: "H3BMXM4W7H"}, nil
				},
				verifyAbsent: func(context.Context, *patch.Runner, string, []string) error {
					return verificationError
				},
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			dependencies := successfulComputerUseVerificationDependencies()
			if testCase.dependencies.readSignature != nil {
				dependencies.readSignature = testCase.dependencies.readSignature
			}
			if testCase.dependencies.verifyRequired != nil {
				dependencies.verifyRequired = testCase.dependencies.verifyRequired
			}
			if testCase.dependencies.verifyAbsent != nil {
				dependencies.verifyAbsent = testCase.dependencies.verifyAbsent
			}
			trace := &patch.Trace{}
			runner := patch.NewRunner(context.Background(), false, io.Discard)
			runner.Trace = trace

			err := verifyComputerUseHelperWithDependencies(
				context.Background(), runner, appPath, policy, "H3BMXM4W7H", dependencies,
			)
			if err == nil {
				t.Fatal("verification unexpectedly succeeded")
			}
			if got := countTraceActionPath(trace, ActionVerifyComputerUseHelper, codePath); got != 0 {
				t.Fatalf("helper success trace count = %d, want 0; events=%#v", got, trace.Events)
			}
		})
	}
}

func TestVerifyComputerUseHelperDoesNotEmitTrustedSurfaceSuccessForFailedCheck(t *testing.T) {
	for _, testCase := range []struct {
		name         string
		policy       targets.ComputerUsePolicy
		writeFixture func(*testing.T, string, targets.ComputerUsePolicy)
		action       patch.Action
	}{
		{
			name: "trusted team bytes",
			policy: targets.ComputerUsePolicy{
				UpstreamTrustedTeamID: "2DC432GLL2",
				TeamPatchBinaries:     []string{"Contents/MacOS/SkyComputerUseService"},
			},
			writeFixture: func(t *testing.T, appPath string, policy targets.ComputerUsePolicy) {
				writeComputerUseTrustFixtures(t, appPath, policy, "2DC432GLL2")
			},
			action: ActionVerifyComputerUseTrustedTeam,
		},
		{
			name: "parent requirement",
			policy: targets.ComputerUsePolicy{
				UpstreamTrustedTeamID: "2DC432GLL2",
				TeamRequirementPlists: []string{"Contents/Resources/Client_Parent.coderequirement"},
			},
			writeFixture: func(t *testing.T, appPath string, policy targets.ComputerUsePolicy) {
				writeComputerUseTrustFixtures(t, appPath, policy, "2DC432GLL2")
			},
			action: ActionVerifyComputerUseRequirement,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			appPath := filepath.Join(t.TempDir(), "Codex Computer Use.app")
			testCase.writeFixture(t, appPath, testCase.policy)
			trace := &patch.Trace{}
			runner := patch.NewRunner(context.Background(), false, io.Discard)
			runner.Trace = trace

			err := verifyComputerUseHelperWithDependencies(
				context.Background(),
				runner,
				appPath,
				testCase.policy,
				"H3BMXM4W7H",
				successfulComputerUseVerificationDependencies(),
			)
			if err == nil {
				t.Fatal("verification unexpectedly succeeded")
			}
			if got := countTraceAction(trace, testCase.action); got != 0 {
				t.Fatalf("success action %s count = %d, want 0; events=%#v", testCase.action, got, trace.Events)
			}
		})
	}
}

func TestLifecycleHookRepairsExistingInstalledAndMatchedCacheHelpers(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	config.SetCurrent(&spec.Config{Signing: spec.SigningSpec{
		Identity: "Developer ID Application: Test (H3BMXM4W7H)",
		TeamID:   "H3BMXM4W7H",
	}})
	t.Cleanup(func() { config.SetCurrent(nil) })
	installedPath := filepath.Join(home, ".codex", "computer-use", "Codex Computer Use.app")
	cachePath := filepath.Join(home, ".codex", "plugins", "cache", "openai-bundled", "computer-use", "1.0.1", "Codex Computer Use.app")
	for _, appPath := range []string{installedPath, cachePath} {
		if err := os.MkdirAll(appPath, 0o755); err != nil {
			t.Fatalf("MkdirAll(%s): %v", appPath, err)
		}
	}
	hostPath := filepath.Join(t.TempDir(), "Codex.app")
	policy := &targets.ComputerUsePolicy{
		HostAppPath:           hostPath,
		BundledAppPath:        "Contents/Resources/Codex Computer Use.app",
		AppPathFromHome:       ".codex/computer-use/Codex Computer Use.app",
		CacheAppGlobsFromHome: []string{".codex/plugins/cache/*/computer-use/*/Codex Computer Use.app"},
		AuthPluginPath:        filepath.Join(t.TempDir(), "AuthorizationPlugin.bundle"),
		AuthPluginExecutable:  "Contents/MacOS/AuthorizationPlugin",
		UpstreamTrustedTeamID: "2DC432GLL2",
	}
	target := targets.Target{
		ID:      "codex",
		AppPath: hostPath,
		Extensions: extensions.Target{
			ComputerUse: policy,
		},
	}
	var repairedPaths []string
	dependencies := computerUseLifecycleDependencies{
		patchBundle: func(
			_ context.Context,
			_ *patch.Runner,
			_ targets.Target,
			appPath string,
			_ targets.ComputerUsePolicy,
			_ string,
		) error {
			repairedPaths = append(repairedPaths, appPath)
			return nil
		},
		patchAuthPlugin: func(
			context.Context,
			*patch.Runner,
			targets.Target,
			targets.ComputerUsePolicy,
			string,
		) error {
			return nil
		},
	}
	runner := patch.NewRunner(context.Background(), false, io.Discard)

	if err := lifecycleHookWithDependencies(
		context.Background(), runner, target, patch.Options{}, dependencies,
	); err != nil {
		t.Fatalf("lifecycleHookWithDependencies: %v", err)
	}
	want := []string{installedPath, cachePath}
	if !stringSlicesEqual(repairedPaths, want) {
		t.Fatalf("repaired paths = %v, want existing installed helper then matched cache helper %v", repairedPaths, want)
	}
}

func TestVerifyComputerUseTrustSurfacesAcceptsLocalTeam(t *testing.T) {
	appPath := t.TempDir()
	policy := targets.ComputerUsePolicy{
		UpstreamTrustedTeamID: "2DC432GLL2",
		TeamPatchBinaries: []string{
			"Contents/MacOS/SkyComputerUseService",
			"Contents/SharedSupport/CUALockScreenGuardian.app/Contents/MacOS/CUALockScreenGuardian",
		},
		TeamRequirementPlists: []string{
			"Contents/SharedSupport/SkyComputerUseClient.app/Contents/Resources/SkyComputerUseClient_Parent.coderequirement",
			"Contents/SharedSupport/CUALockScreenGuardian.app/Contents/Resources/CUALockScreenGuardian_Parent.coderequirement",
		},
	}
	writeComputerUseTrustFixtures(t, appPath, policy, "H3BMXM4W7H")

	if err := verifyComputerUseTrustSurfaces(context.Background(), appPath, policy, "H3BMXM4W7H"); err != nil {
		t.Fatalf("verifyComputerUseTrustSurfaces: %v", err)
	}
}

func TestVerifyComputerUseTrustSurfacesRejectsUpstreamBytes(t *testing.T) {
	appPath := t.TempDir()
	policy := targets.ComputerUsePolicy{
		UpstreamTrustedTeamID: "2DC432GLL2",
		TeamPatchBinaries:     []string{"Contents/MacOS/SkyComputerUseService"},
	}
	binaryPath := filepath.Join(appPath, filepath.FromSlash(policy.TeamPatchBinaries[0]))
	writeComputerUseFixtureFile(t, binaryPath, []byte("trusted-team\x002DC432GLL2\x00"))

	err := verifyComputerUseTrustSurfaces(context.Background(), appPath, policy, "H3BMXM4W7H")
	if err == nil || !strings.Contains(err.Error(), "still contains upstream trusted team 2DC432GLL2") {
		t.Fatalf("verifyComputerUseTrustSurfaces error = %v, want upstream team rejection", err)
	}
}

func TestVerifyComputerUseTrustSurfacesRejectsLocalTeamFoundOnlyInCodeSignature(t *testing.T) {
	appPath := t.TempDir()
	policy := targets.ComputerUsePolicy{
		UpstreamTrustedTeamID: "2DC432GLL2",
		TeamPatchBinaries:     []string{"Contents/MacOS/SkyComputerUseService"},
	}
	binaryPath := filepath.Join(appPath, filepath.FromSlash(policy.TeamPatchBinaries[0]))
	writeComputerUseFixtureFile(t, binaryPath, signedLikeMachOFixture(nil, []byte("signature-team\x00H3BMXM4W7H\x00")))

	err := verifyComputerUseTrustSurfaces(context.Background(), appPath, policy, "H3BMXM4W7H")
	if err == nil || !strings.Contains(err.Error(), "does not contain local trusted team H3BMXM4W7H outside its code signature") {
		t.Fatalf("verifyComputerUseTrustSurfaces error = %v, want trusted-location rejection", err)
	}
}

func TestVerifyComputerUseTrustSurfacesRejectsUpstreamParentRequirement(t *testing.T) {
	appPath := t.TempDir()
	policy := targets.ComputerUsePolicy{
		UpstreamTrustedTeamID: "2DC432GLL2",
		TeamRequirementPlists: []string{
			"Contents/SharedSupport/SkyComputerUseClient.app/Contents/Resources/SkyComputerUseClient_Parent.coderequirement",
		},
	}
	plistPath := filepath.Join(appPath, filepath.FromSlash(policy.TeamRequirementPlists[0]))
	writeComputerUseFixtureFile(t, plistPath, []byte(`<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict><key>team-identifier</key><string>2DC432GLL2</string></dict></plist>`))

	err := verifyComputerUseTrustSurfaces(context.Background(), appPath, policy, "H3BMXM4W7H")
	if err == nil || !strings.Contains(err.Error(), "trusts parent team 2DC432GLL2, want H3BMXM4W7H") {
		t.Fatalf("verifyComputerUseTrustSurfaces error = %v, want upstream parent requirement rejection", err)
	}
}

func writeComputerUseTrustFixtures(
	t *testing.T,
	appPath string,
	policy targets.ComputerUsePolicy,
	localTeamID string,
) {
	t.Helper()
	for _, relativePath := range policy.TeamPatchBinaries {
		binaryPath := filepath.Join(appPath, filepath.FromSlash(relativePath))
		writeComputerUseFixtureFile(t, binaryPath, []byte("trusted-team\x00"+localTeamID+"\x00"))
	}
	for _, relativePath := range policy.TeamRequirementPlists {
		plistPath := filepath.Join(appPath, filepath.FromSlash(relativePath))
		contents := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict><key>team-identifier</key><string>` + localTeamID + `</string></dict></plist>`)
		writeComputerUseFixtureFile(t, plistPath, contents)
	}
}

func verifyComputerUseTrustSurfaces(
	ctx context.Context,
	appPath string,
	policy targets.ComputerUsePolicy,
	localTeamID string,
) error {
	return verifyComputerUseTrustSurfacesWithDependencies(ctx, nil, appPath, policy, localTeamID, os.ReadFile)
}

func writeComputerUseFixtureFile(t *testing.T, path string, contents []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, contents, 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
}

func signedLikeMachOFixture(contents []byte, codeSignature []byte) []byte {
	const (
		headerSize        = 32
		loadCommandSize   = 16
		codeSignatureData = 64
	)
	data := make([]byte, codeSignatureData+len(codeSignature))
	binary.LittleEndian.PutUint32(data[0:4], 0xfeedfacf)
	binary.LittleEndian.PutUint32(data[16:20], 1)
	binary.LittleEndian.PutUint32(data[20:24], loadCommandSize)
	binary.LittleEndian.PutUint32(data[headerSize:headerSize+4], 0x1d)
	binary.LittleEndian.PutUint32(data[headerSize+4:headerSize+8], loadCommandSize)
	binary.LittleEndian.PutUint32(data[headerSize+8:headerSize+12], codeSignatureData)
	binary.LittleEndian.PutUint32(data[headerSize+12:headerSize+16], uint32(len(codeSignature)))
	copy(data[headerSize+loadCommandSize:codeSignatureData], contents)
	copy(data[codeSignatureData:], codeSignature)
	return data
}

func successfulComputerUseVerificationDependencies() computerUseVerificationDependencies {
	return computerUseVerificationDependencies{
		verifyBundle: func(context.Context, *patch.Runner, string) error { return nil },
		readSignature: func(context.Context, string) (bundleidentity.Signature, error) {
			return bundleidentity.Signature{TeamID: "H3BMXM4W7H"}, nil
		},
		verifyRequired: func(context.Context, *patch.Runner, string, []string) error { return nil },
		verifyAbsent:   func(context.Context, *patch.Runner, string, []string) error { return nil },
		readFile:       os.ReadFile,
	}
}

func countTraceAction(trace *patch.Trace, action patch.Action) int {
	count := 0
	for _, event := range trace.Events {
		if event.Action == action {
			count++
		}
	}
	return count
}

func countTraceActionPath(trace *patch.Trace, action patch.Action, path string) int {
	count := 0
	for _, event := range trace.Events {
		if event.Action == action && event.Path == path {
			count++
		}
	}
	return count
}

func stringSlicesEqual(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func TestTeamIDFromSignIdentity(t *testing.T) {
	got, err := teamIDFromSignIdentity("Developer ID Application: Alex Goodkind (H3BMXM4W7H)")
	if err != nil {
		t.Fatalf("teamIDFromSignIdentity: %v", err)
	}
	if got != "H3BMXM4W7H" {
		t.Fatalf("teamIDFromSignIdentity = %q, want H3BMXM4W7H", got)
	}
}

func TestReplaceStandaloneTeamIDPreservesAppGroupPrefix(t *testing.T) {
	input := []byte("2DC432GLL2\x00prefix 2DC432GLL2.com.openai.sky.CUAService\n2DC432GLL2 ")
	out, replacements, alreadyPatched, err := replaceStandaloneTeamID(input, "2DC432GLL2", "H3BMXM4W7H")
	if err != nil {
		t.Fatalf("replaceStandaloneTeamID: %v", err)
	}
	if replacements != 2 {
		t.Fatalf("replacements = %d, want 2", replacements)
	}
	if alreadyPatched {
		t.Fatal("alreadyPatched = true, want false")
	}
	if !bytes.Contains(out, []byte("2DC432GLL2.com.openai.sky.CUAService")) {
		t.Fatalf("expected app group prefix to remain unchanged; got %q", string(out))
	}
	if got := countStandaloneToken(out, "2DC432GLL2"); got != 0 {
		t.Fatalf("standalone upstream team count = %d, want 0", got)
	}
	if got := countStandaloneToken(out, "H3BMXM4W7H"); got != 2 {
		t.Fatalf("standalone local team count = %d, want 2", got)
	}
}

func TestReplaceStandaloneTeamIDIdempotent(t *testing.T) {
	input := []byte("H3BMXM4W7H\x00prefix 2DC432GLL2.com.openai.sky.CUAService")
	out, replacements, alreadyPatched, err := replaceStandaloneTeamID(input, "2DC432GLL2", "H3BMXM4W7H")
	if err != nil {
		t.Fatalf("replaceStandaloneTeamID: %v", err)
	}
	if replacements != 0 {
		t.Fatalf("replacements = %d, want 0", replacements)
	}
	if !alreadyPatched {
		t.Fatal("alreadyPatched = false, want true")
	}
	if !bytes.Equal(out, input) {
		t.Fatalf("idempotent replacement changed input: got %q want %q", string(out), string(input))
	}
}

func TestReplaceTeamRequirementPlist(t *testing.T) {
	input := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
<key>team-identifier</key>
<string>2DC432GLL2</string>
</dict>
</plist>`)
	out, changed, alreadyPatched, err := replaceTeamRequirementPlist(input, "2DC432GLL2", "H3BMXM4W7H")
	if err != nil {
		t.Fatalf("replaceTeamRequirementPlist: %v", err)
	}
	if !changed {
		t.Fatal("changed = false, want true")
	}
	if alreadyPatched {
		t.Fatal("alreadyPatched = true, want false")
	}
	got, err := teamRequirementPlistTeamID(out)
	if err != nil {
		t.Fatalf("teamRequirementPlistTeamID: %v", err)
	}
	if got != "H3BMXM4W7H" {
		t.Fatalf("team-identifier = %q, want H3BMXM4W7H", got)
	}
}

func TestReplaceTeamRequirementPlistIdempotent(t *testing.T) {
	input := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<dict>
<key>team-identifier</key>
<string>H3BMXM4W7H</string>
</dict>
</plist>`)
	out, changed, alreadyPatched, err := replaceTeamRequirementPlist(input, "2DC432GLL2", "H3BMXM4W7H")
	if err != nil {
		t.Fatalf("replaceTeamRequirementPlist: %v", err)
	}
	if changed {
		t.Fatal("changed = true, want false")
	}
	if !alreadyPatched {
		t.Fatal("alreadyPatched = false, want true")
	}
	if !bytes.Equal(out, input) {
		t.Fatalf("idempotent replacement changed input: got %q want %q", string(out), string(input))
	}
}

func TestReplaceStandaloneTeamIDRejectsInvalidTeam(t *testing.T) {
	_, _, _, err := replaceStandaloneTeamID([]byte("2DC432GLL2"), "2DC432GLL2", "TOO-SHORT")
	if err == nil {
		t.Fatal("expected invalid team error")
	}
}

func TestBundledAuthPluginSourcePathUsesDeclaredSignTarget(t *testing.T) {
	policy := targets.ComputerUsePolicy{
		BundledAppPath: "Contents/Resources/plugins/openai-bundled/plugins/computer-use/Codex Computer Use.app",
		AuthPluginPath: "/Library/Security/SecurityAgentPlugins/CodexComputerUseAuthorizationPlugin.bundle",
		SignTargets: []targets.ComputerUseSignTarget{
			{Path: "Contents/SharedSupport/Codex Computer Use Installer.app/Contents/Resources/CodexComputerUseAuthorizationPlugin.bundle"},
		},
	}
	got, ok := bundledAuthPluginSourcePath("/Applications/Codex.app", policy)
	if !ok {
		t.Fatal("bundledAuthPluginSourcePath ok = false, want true")
	}
	want := filepath.Join(
		"/Applications/Codex.app",
		"Contents/Resources/plugins/openai-bundled/plugins/computer-use/Codex Computer Use.app",
		"Contents/SharedSupport/Codex Computer Use Installer.app/Contents/Resources/CodexComputerUseAuthorizationPlugin.bundle",
	)
	if got != want {
		t.Fatalf("bundledAuthPluginSourcePath = %q, want %q", got, want)
	}
}

func TestWriteExistingFileOpenErrorIncludesRewriteEvidence(t *testing.T) {
	dirPath := t.TempDir()
	err := writeExistingFile(dirPath, 0o755, []byte("data"))
	if err == nil {
		t.Fatal("writeExistingFile(dir) unexpectedly succeeded")
	}

	for _, fragment := range []string{
		"attempted operation=atomic replace existing file",
		"replace " + dirPath,
		"path=" + dirPath,
		"owner=",
		"mode=",
		"flags=",
		"xattrs=",
	} {
		if !strings.Contains(err.Error(), fragment) {
			t.Fatalf("error %q missing %q", err, fragment)
		}
	}
}

func TestWriteExistingFileReplacesContentsAndMode(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "helper")
	if err := os.WriteFile(filePath, []byte("old"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := writeExistingFile(filePath, 0o755, []byte("new-data")); err != nil {
		t.Fatalf("writeExistingFile: %v", err)
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "new-data" {
		t.Fatalf("contents = %q, want new-data", string(data))
	}
	info, err := os.Stat(filePath)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("mode = %#o, want 0755", info.Mode().Perm())
	}
}

func TestListPathXattrsReturnsSortedNames(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "helper")
	if err := os.WriteFile(filePath, []byte("data"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := unix.Setxattr(filePath, "com.goodkind.test.second", []byte("2"), 0); err != nil {
		t.Skipf("Setxattr second: %v", err)
	}
	if err := unix.Setxattr(filePath, "com.goodkind.test.first", []byte("1"), 0); err != nil {
		t.Skipf("Setxattr first: %v", err)
	}

	xattrs, err := ReadPathXattrs(filePath)
	if err != nil {
		t.Fatalf("ReadPathXattrs: %v", err)
	}
	firstIndex := -1
	secondIndex := -1
	for index, xattr := range xattrs {
		if xattr == "com.goodkind.test.first" {
			firstIndex = index
		}
		if xattr == "com.goodkind.test.second" {
			secondIndex = index
		}
	}
	if firstIndex < 0 || secondIndex < 0 {
		t.Fatalf("xattrs = %v, want both custom xattrs present", xattrs)
	}
	if firstIndex > secondIndex {
		t.Fatalf("xattrs = %v, want custom xattrs sorted", xattrs)
	}
}
