package patch

import (
	"os/exec"
	"testing"

	"goodkind.io/desktop-via-clyde/internal/spec"
	"goodkind.io/desktop-via-clyde/internal/targets"
)

func TestIgnoreShimDryRunErrorAllowsConfiguredSIGKILL(t *testing.T) {
	err := exec.Command("/bin/sh", "-c", "kill -9 $$").Run()
	if err == nil {
		t.Fatal("expected killed process error")
	}
	if !ignoreShimDryRunError(targets.Target{LaunchPolicy: spec.LaunchPolicySpec{IgnoreDryRunSignal: "SIGKILL"}}, err) {
		t.Fatal("expected configured SIGKILL to be ignored")
	}
	if ignoreShimDryRunError(targets.Target{}, err) {
		t.Fatal("expected missing dry-run signal rule to remain fatal")
	}
}
