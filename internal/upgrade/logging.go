package upgrade

import (
	"context"
	"log/slog"
)

var upgradeLog = slog.With("component", "desktop-via-clyde", "subcomponent", "upgrade")

func logUpgradeError(ctx context.Context, event string, err error) error {
	upgradeLog.ErrorContext(ctx, event, "err", err)
	return err
}

func logUpgradeErrorNoContext(event string, err error) error {
	upgradeLog.Error(event, "err", err)
	return err
}
