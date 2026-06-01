package patch

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"goodkind.io/desktop-via-clyde/internal/catalog"
	"goodkind.io/desktop-via-clyde/internal/targets"
)

// LifecycleHook runs optional extension behavior at one lifecycle point.
type LifecycleHook func(context.Context, *Runner, targets.Target, Options) error

var (
	hooksMu         sync.RWMutex
	postPatchHooks  = map[string]LifecycleHook{}
	preUnpatchHooks = map[string]LifecycleHook{}
	preResignHooks  = map[string]LifecycleHook{}
	postBundleHooks = map[string]LifecycleHook{}
)

// RegisterPostPatchHook links one patch hook capability to post-patch behavior.
func RegisterPostPatchHook(capability string, hook LifecycleHook) error {
	return registerLifecycleHook(capability, hook, postPatchHooks)
}

// RegisterPreUnpatchHook links one patch hook capability to pre-unpatch behavior.
func RegisterPreUnpatchHook(capability string, hook LifecycleHook) error {
	return registerLifecycleHook(capability, hook, preUnpatchHooks)
}

// RegisteredPostPatchHooks returns post-patch hook capabilities in stable order.
func RegisteredPostPatchHooks() []string {
	return registeredHookNames(postPatchHooks)
}

// RegisteredPreUnpatchHooks returns pre-unpatch hook capabilities in stable order.
func RegisteredPreUnpatchHooks() []string {
	return registeredHookNames(preUnpatchHooks)
}

// RegisterPreResignHook links extension behavior before shared re-signing.
func RegisterPreResignHook(name string, hook LifecycleHook) error {
	return registerNamedHook(name, hook, preResignHooks)
}

// RegisterPostBundleHook links extension behavior after shared bundle mutation.
func RegisterPostBundleHook(name string, hook LifecycleHook) error {
	return registerNamedHook(name, hook, postBundleHooks)
}

// RegisteredPreResignHooks returns pre-resign hook names in stable order.
func RegisteredPreResignHooks() []string {
	return registeredHookNames(preResignHooks)
}

// RegisteredPostBundleHooks returns post-bundle hook names in stable order.
func RegisteredPostBundleHooks() []string {
	return registeredHookNames(postBundleHooks)
}

func runPostPatchHook(
	ctx context.Context,
	r *Runner,
	t targets.Target,
	opts Options,
	capability string,
) error {
	hook, ok := lookupLifecycleHook(postPatchHooks, capability)
	if !ok {
		return fmt.Errorf("post-patch hook %q is not registered", capability)
	}
	return hook(ctx, r, t, opts)
}

func runPreUnpatchHook(
	ctx context.Context,
	r *Runner,
	t targets.Target,
	opts Options,
	capability string,
) error {
	hook, ok := lookupLifecycleHook(preUnpatchHooks, capability)
	if !ok {
		return fmt.Errorf("pre-unpatch hook %q is not registered", capability)
	}
	return hook(ctx, r, t, opts)
}

func runPreResignHooks(
	ctx context.Context,
	r *Runner,
	t targets.Target,
	opts Options,
) error {
	return runNamedHooks(ctx, r, t, opts, preResignHooks)
}

func runPostBundleHooks(
	ctx context.Context,
	r *Runner,
	t targets.Target,
	opts Options,
) error {
	return runNamedHooks(ctx, r, t, opts, postBundleHooks)
}

func registerLifecycleHook(
	capability string,
	hook LifecycleHook,
	hooks map[string]LifecycleHook,
) error {
	if !catalog.HasPatchHookCapability(capability) {
		return fmt.Errorf("patch hook capability %q is not linked", capability)
	}
	if hook == nil {
		return fmt.Errorf("patch hook capability %q handler is required", capability)
	}
	hooksMu.Lock()
	defer hooksMu.Unlock()
	if _, ok := hooks[capability]; ok {
		return fmt.Errorf("patch hook capability %q handler is already registered", capability)
	}
	hooks[capability] = hook
	return nil
}

func registerNamedHook(
	name string,
	hook LifecycleHook,
	hooks map[string]LifecycleHook,
) error {
	if name == "" {
		return fmt.Errorf("hook name is required")
	}
	if hook == nil {
		return fmt.Errorf("hook %q handler is required", name)
	}
	hooksMu.Lock()
	defer hooksMu.Unlock()
	if _, ok := hooks[name]; ok {
		return fmt.Errorf("hook %q handler is already registered", name)
	}
	hooks[name] = hook
	return nil
}

func lookupLifecycleHook(
	hooks map[string]LifecycleHook,
	capability string,
) (LifecycleHook, bool) {
	hooksMu.RLock()
	defer hooksMu.RUnlock()
	hook, ok := hooks[capability]
	return hook, ok
}

func registeredHookNames(hooks map[string]LifecycleHook) []string {
	hooksMu.RLock()
	defer hooksMu.RUnlock()
	names := make([]string, 0, len(hooks))
	for name := range hooks {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func runNamedHooks(
	ctx context.Context,
	r *Runner,
	t targets.Target,
	opts Options,
	hooks map[string]LifecycleHook,
) error {
	hooksMu.RLock()
	defer hooksMu.RUnlock()
	names := make([]string, 0, len(hooks))
	for name := range hooks {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := hooks[name](ctx, r, t, opts); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}
