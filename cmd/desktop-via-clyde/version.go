package main

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"goodkind.io/desktop-via-clyde/internal/version"
)

func newVersionCmd(out io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print build version metadata",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			printVersion(out)
			return nil
		},
	}
}

func printVersion(out io.Writer) {
	_, _ = fmt.Fprintf(out, "version:   %s\n", version.Version)
	_, _ = fmt.Fprintf(out, "commit:    %s\n", version.Commit)
	_, _ = fmt.Fprintf(out, "dirty:     %s\n", version.Dirty)
	_, _ = fmt.Fprintf(out, "buildTime: %s\n", version.BuildTime)
	_, _ = fmt.Fprintf(out, "buildHash: %s\n", version.BuildHash())
}
