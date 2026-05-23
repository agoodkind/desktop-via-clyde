package patch

import (
	"os/exec"
	"testing"

	"goodkind.io/desktop-via-clyde/internal/targets"
)

func TestIgnoreShimDryRunErrorAllowsClaudeSIGKILL(t *testing.T) {
	err := exec.Command("/bin/sh", "-c", "kill -9 $$").Run()
	if err == nil {
		t.Fatal("expected killed process error")
	}
	if !ignoreShimDryRunError(targets.Target{ID: "claude"}, err) {
		t.Fatal("expected Claude SIGKILL to be ignored")
	}
	if ignoreShimDryRunError(targets.Target{ID: "cursor"}, err) {
		t.Fatal("expected Cursor SIGKILL to remain fatal")
	}
}
