package computeruseext

import (
	"context"

	"goodkind.io/desktop-via-clyde/internal/patch"
	"goodkind.io/desktop-via-clyde/internal/targets"
)

type computerUseBundlePatcher func(
	context.Context,
	*patch.Runner,
	targets.Target,
	string,
	targets.ComputerUsePolicy,
	string,
) error

type computerUseAuthPluginPatcher func(
	context.Context,
	*patch.Runner,
	targets.Target,
	targets.ComputerUsePolicy,
	string,
) error

type computerUseLifecycleDependencies struct {
	patchBundle     computerUseBundlePatcher
	patchAuthPlugin computerUseAuthPluginPatcher
}

func defaultComputerUseLifecycleDependencies() computerUseLifecycleDependencies {
	return computerUseLifecycleDependencies{
		patchBundle:     patchComputerUseBundle,
		patchAuthPlugin: patchComputerUseAuthPluginStep,
	}
}
