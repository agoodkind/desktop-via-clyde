package computeruseext

import (
	"context"
	"os"

	"goodkind.io/desktop-via-clyde/internal/bundleidentity"
	"goodkind.io/desktop-via-clyde/internal/patch"
)

type (
	computerUseSignatureReader     func(context.Context, string) (bundleidentity.Signature, error)
	computerUseEntitlementVerifier func(context.Context, *patch.Runner, string, []string) error
	computerUseBundleVerifier      func(context.Context, *patch.Runner, string) error
	computerUseFileReader          func(string) ([]byte, error)
)

type computerUseVerificationDependencies struct {
	verifyBundle   computerUseBundleVerifier
	readSignature  computerUseSignatureReader
	verifyRequired computerUseEntitlementVerifier
	verifyAbsent   computerUseEntitlementVerifier
	readFile       computerUseFileReader
}

func defaultComputerUseVerificationDependencies() computerUseVerificationDependencies {
	return computerUseVerificationDependencies{
		verifyBundle: func(ctx context.Context, runner *patch.Runner, appPath string) error {
			return runner.Run(ctx, "/usr/bin/codesign", "--verify", "--deep", "--strict", "--verbose=2", appPath)
		},
		readSignature:  bundleidentity.ReadSignature,
		verifyRequired: patch.VerifyBooleanEntitlements,
		verifyAbsent:   patch.VerifyAbsentEntitlements,
		readFile:       os.ReadFile,
	}
}
