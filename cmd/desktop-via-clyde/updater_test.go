package main

import (
	"context"
	"io"
	"testing"

	"goodkind.io/desktop-via-clyde/internal/clioutput"
)

func TestUpdaterStatusPassesRequestedFormat(t *testing.T) {
	originalUpdaterStatusRunner := updaterStatusRunner
	t.Cleanup(func() {
		updaterStatusRunner = originalUpdaterStatusRunner
	})

	var gotFormat clioutput.Format
	updaterStatusRunner = func(_ context.Context, out io.Writer, format clioutput.Format) error {
		gotFormat = format
		_, _ = io.WriteString(out, "{}\n")
		return nil
	}

	output, err := executeRoot(t, "updater", "status", "--output-format", "json")
	if err != nil {
		t.Fatalf("executeRoot(updater status json): %v\noutput:\n%s", err, output)
	}
	if gotFormat != clioutput.FormatJSON {
		t.Fatalf("format = %q, want %q", gotFormat, clioutput.FormatJSON)
	}
	if output != "{}\n" {
		t.Fatalf("output = %q, want compact stub json", output)
	}
}
