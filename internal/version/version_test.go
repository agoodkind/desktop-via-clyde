package version

import (
	"testing"

	gklogversion "goodkind.io/gklog/version"
)

func TestBuildHashFallsBackWhenStampedHashIsEmpty(t *testing.T) {
	originalBinHash := gklogversion.BinHash
	t.Cleanup(func() {
		gklogversion.BinHash = originalBinHash
	})

	gklogversion.BinHash = ""

	if got := BuildHash(); got == "" || got == "unknown" {
		t.Fatalf("BuildHash() = %q, want runtime hash fallback", got)
	}
}
