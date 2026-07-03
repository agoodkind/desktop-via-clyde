package main

import (
	"context"
	"io"

	"github.com/spf13/cobra"
)

type configFreeCommandName string

const (
	configFreeCommandUpdate       configFreeCommandName = "update"
	configFreeCommandVersion      configFreeCommandName = "version"
	configFreeCommandVersionLong  configFreeCommandName = "--version"
	configFreeCommandVersionShort configFreeCommandName = "-v"
)

func runConfigFreeCommand(
	ctx context.Context,
	args []string,
	out io.Writer,
	errOut io.Writer,
) (int, bool) {
	if len(args) == 0 {
		return 0, false
	}

	var cmd *cobra.Command
	switch configFreeCommandName(args[0]) {
	case configFreeCommandVersion, configFreeCommandVersionLong, configFreeCommandVersionShort:
		cmd = newVersionCmd(out)
	case configFreeCommandUpdate:
		cmd = newUpdateCmd(ctx, out)
	default:
		return 0, false
	}
	cmd.SetOut(out)
	cmd.SetErr(errOut)
	cmd.SetArgs(args[1:])
	if err := cmd.ExecuteContext(ctx); err != nil {
		_, _ = io.WriteString(errOut, "error: "+err.Error()+"\n")
		return 1, true
	}
	return 0, true
}
