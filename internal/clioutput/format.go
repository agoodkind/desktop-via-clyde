// Package clioutput defines shared CLI output modes and JSON progress writers.
package clioutput

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// Format identifies one CLI output mode.
type Format string

const (
	// FormatText renders human-readable output.
	FormatText Format = "text"
	// FormatJSON renders JSON documents.
	FormatJSON Format = "json"
)

// FlagName is the persistent output mode flag.
const FlagName = "output-format"

// UnknownFormatError reports one unsupported format.
type UnknownFormatError struct {
	Input string
}

// Error reports the unsupported format and the supported values.
func (e *UnknownFormatError) Error() string {
	return fmt.Sprintf("output: unknown format %q (supported: %s, %s)", e.Input, FormatText, FormatJSON)
}

// ParseFormat resolves one user-supplied output mode.
func ParseFormat(raw string) (Format, error) {
	switch strings.TrimSpace(raw) {
	case "", string(FormatText):
		return FormatText, nil
	case string(FormatJSON):
		return FormatJSON, nil
	default:
		return "", &UnknownFormatError{Input: raw}
	}
}

// PersistentFlag registers the shared output mode flag.
func PersistentFlag(root *cobra.Command) {
	root.PersistentFlags().String(FlagName, string(FormatText), "Output format: text (default, human-readable) or json")
}

// DetectFormatFromArgs best-effort parses --output-format before Cobra runs.
func DetectFormatFromArgs(args []string) Format {
	for index, arg := range args {
		if raw, ok := strings.CutPrefix(arg, "--"+FlagName+"="); ok {
			format, err := ParseFormat(raw)
			if err == nil {
				return format
			}
			return FormatText
		}
		if arg == "--"+FlagName && index+1 < len(args) {
			format, err := ParseFormat(args[index+1])
			if err == nil {
				return format
			}
			return FormatText
		}
	}
	return FormatText
}
