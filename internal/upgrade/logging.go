package upgrade

import (
	"context"
	"fmt"
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

func logUpgradeRegistrationError(message string, err error) error {
	upgradeLog.Error("upgrade.registration_failed", "message", message, "err", err)
	return fmt.Errorf("%s: %w", message, err)
}
