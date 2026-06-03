// Command desktop-via-clyde patches configured macOS desktop applications so
// they launch through the Clyde MITM harness.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"goodkind.io/desktop-via-clyde/internal/batchops"
	"goodkind.io/desktop-via-clyde/internal/clioutput"
	"goodkind.io/desktop-via-clyde/internal/clispec"
	"goodkind.io/desktop-via-clyde/internal/composition"
	"goodkind.io/desktop-via-clyde/internal/config"
	"goodkind.io/desktop-via-clyde/internal/helperdispatch"
	"goodkind.io/desktop-via-clyde/internal/logging"
	"goodkind.io/desktop-via-clyde/internal/operations"
	"goodkind.io/desktop-via-clyde/internal/paths"
	"goodkind.io/desktop-via-clyde/internal/response"
	"goodkind.io/desktop-via-clyde/internal/version"
	"goodkind.io/gklog"
	"goodkind.io/gklog/correlation"
)

var bootstrapLog = slog.New(slog.DiscardHandler)

type operationRunner func(context.Context, operations.Request) error

type batchOperationRunner func(context.Context, batchops.Request) error

func main() {
	bootstrapLog.Info("desktop-via-clyde.main")
	exitCode := run()
	if exitCode != 0 {
		os.Exit(exitCode)
	}
}

func run() int {
	outputFormat := clioutput.DetectFormatFromArgs(os.Args[1:])

	logger, closer, loggingErr := logging.Setup()
	slog.SetDefault(logger)

	baseCtx := correlation.WithContext(context.Background(), correlation.New(""))
	ctx := gklog.WithLogger(baseCtx, logger)
	if loggingErr != nil {
		logger.WarnContext(ctx, "cli.logging_setup_degraded", slog.Any("err", loggingErr))
	}

	logger.InfoContext(ctx, "cli.start",
		"version", version.String(),
		"gklog_build", logging.GklogBuild(),
		"log_path", paths.ProcessLogPath(),
		"args", os.Args[1:])

	if err := composition.Register(); err != nil {
		logger.ErrorContext(ctx, "cli.composition_register_failed", slog.Any("err", err))
		writeRuntimeMessage(ctx, os.Stderr, outputFormat, "error: "+err.Error())
		_ = closer.Close()
		return 1
	}
	if code, ok := helperdispatch.RunIfMatched(); ok {
		_ = closer.Close()
		return code
	}

	loadedConfig, err := config.LoadRequired()
	if err != nil {
		logger.ErrorContext(ctx, "cli.config_load_failed", slog.Any("err", err))
		writeRuntimeMessage(ctx, os.Stderr, outputFormat, "error: "+err.Error())
		_ = closer.Close()
		return 1
	}
	config.SetCurrent(loadedConfig)

	root := newRootCmd(ctx, os.Stdout, os.Stderr)
	if err := root.ExecuteContext(ctx); err != nil {
		logger.ErrorContext(ctx, "cli.execute.failed", slog.Any("err", err))
		writeRuntimeMessage(ctx, os.Stderr, outputFormat, "error: "+err.Error())
		_ = closer.Close()
		return 1
	}
	logger.InfoContext(ctx, "cli.exit")
	_ = closer.Close()
	return 0
}

func newRootCmd(ctx context.Context, out io.Writer, errOut io.Writer) *cobra.Command {
	return newRootCmdWithRunners(ctx, out, errOut, operations.Run, batchops.Run)
}

func newRootCmdWithRunners(
	ctx context.Context,
	out io.Writer,
	errOut io.Writer,
	runner operationRunner,
	batchRunner batchOperationRunner,
) *cobra.Command {
	normalizedCtx, _ := correlation.Ensure(ctx, "")
	root := clispec.BuildRoot(
		normalizedCtx,
		out,
		errOut,
		clispec.OperationRunner(runner),
		clispec.BatchRunner(batchRunner),
	)
	traceHeaderWritten := false
	root.PersistentPreRun = func(cmd *cobra.Command, _ []string) {
		if traceHeaderWritten {
			return
		}
		format, err := readOutputFormat(normalizedCtx, cmd)
		if err != nil || format == clioutput.FormatJSON {
			return
		}
		header := response.FromContext(normalizedCtx).HeaderLine()
		if header == "" {
			return
		}
		traceHeaderWritten = true
		_, _ = io.WriteString(cmd.OutOrStdout(), header+"\n")
	}
	return root
}

func writeRuntimeMessage(ctx context.Context, out io.Writer, format clioutput.Format, message string) {
	if format == clioutput.FormatJSON {
		body, err := json.Marshal(map[string]string{
			"type":    "runtime",
			"message": message,
		})
		if err == nil {
			_ = response.WriteJSON(ctx, out, body, response.JSONCompact)
			return
		}
	}
	_, _ = io.WriteString(out, message+"\n")
}

func readOutputFormat(ctx context.Context, cmd *cobra.Command) (clioutput.Format, error) {
	rawFormat, err := cmd.Root().PersistentFlags().GetString(clioutput.FlagName)
	if err != nil {
		slog.WarnContext(ctx, "cli.output_format_read_failed", "err", err)
		return "", fmt.Errorf("read output format flag: %w", err)
	}
	format, err := clioutput.ParseFormat(rawFormat)
	if err != nil {
		slog.WarnContext(ctx, "cli.output_format_parse_failed", "err", err)
		return "", fmt.Errorf("parse output format flag: %w", err)
	}
	return format, nil
}
