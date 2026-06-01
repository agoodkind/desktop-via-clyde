package patch

import (
	"path/filepath"
	"testing"

	"goodkind.io/desktop-via-clyde/internal/config"
)

func installFixture(t *testing.T) {
	t.Helper()
	if err := registerFixtureCapabilities(); err != nil {
		t.Fatalf("RegisterFixtureCapabilities(): %v", err)
	}
	cfg, err := config.LoadPath(filepath.Join("..", "testconfig", "testdata", "current-config.toml"))
	if err != nil {
		t.Fatalf("LoadPath(current-config.toml): %v", err)
	}
	config.SetCurrent(cfg)
	t.Cleanup(func() {
		config.SetCurrent(nil)
	})
}
