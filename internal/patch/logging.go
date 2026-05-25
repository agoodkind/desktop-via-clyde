package patch

import (
	"context"
	"log/slog"
)

var patchLog = slog.With("component", "desktop-via-clyde", "subcomponent", "patch")

func logPatchError(ctx context.Context, event string, err error) error {
	patchLog.ErrorContext(ctx, event, "err", err)
	return err
}

func logPatchErrorNoContext(event string, err error) error {
	patchLog.Error(event, "err", err)
	return err
}
