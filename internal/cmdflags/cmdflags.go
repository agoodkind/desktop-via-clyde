// Package cmdflags reads config-declared flag specs onto Cobra commands.
package cmdflags

import (
	"fmt"
	"log/slog"
	"strconv"

	"github.com/spf13/cobra"

	"goodkind.io/desktop-via-clyde/internal/operations"
	"goodkind.io/desktop-via-clyde/internal/spec"
)

// Register adds configured flags onto cmd.
func Register(cmd *cobra.Command, flags []spec.FlagSpec) {
	for _, flag := range flags {
		switch flag.Type {
		case spec.FlagTypeBool:
			value := false
			if flag.DefaultBool != nil {
				value = *flag.DefaultBool
			}
			cmd.Flags().Bool(flag.Name, value, flag.Usage)
		case spec.FlagTypeString:
			cmd.Flags().String(flag.Name, flag.DefaultString, flag.Usage)
		}
		if flag.Hidden {
			_ = cmd.Flags().MarkHidden(flag.Name)
		}
	}
}

// Read returns the configured flag values from cmd.
func Read(cmd *cobra.Command, flags []spec.FlagSpec) (operations.FlagValues, error) {
	values := operations.NewFlagValues()
	for _, flag := range flags {
		switch flag.Type {
		case spec.FlagTypeBool:
			value, err := cmd.Flags().GetBool(flag.Name)
			if err != nil {
				slog.Warn("cmdflags.read_bool_failed", "flag", flag.Name, "err", err)
				return operations.FlagValues{}, fmt.Errorf("read bool flag %s: %w", flag.Name, err)
			}
			setBool(values, flag, value)
		case spec.FlagTypeString:
			value, err := cmd.Flags().GetString(flag.Name)
			if err != nil {
				slog.Warn("cmdflags.read_string_failed", "flag", flag.Name, "err", err)
				return operations.FlagValues{}, fmt.Errorf("read string flag %s: %w", flag.Name, err)
			}
			setString(values, flag, value)
		default:
			return operations.FlagValues{}, fmt.Errorf("unsupported flag type %q", flag.Type)
		}
	}
	return values, nil
}

// Defaults returns the default configured flag values.
func Defaults(flags []spec.FlagSpec) operations.FlagValues {
	values := operations.NewFlagValues()
	for _, flag := range flags {
		switch flag.Type {
		case spec.FlagTypeBool:
			value := false
			if flag.DefaultBool != nil {
				value = *flag.DefaultBool
			}
			setBool(values, flag, value)
		case spec.FlagTypeString:
			setString(values, flag, flag.DefaultString)
		}
	}
	return values
}

// ApplyOverride applies a raw string override to the named visible flag.
func ApplyOverride(values operations.FlagValues, flags []spec.FlagSpec, name string, raw string) error {
	flag, ok := Find(flags, name)
	if !ok {
		return fmt.Errorf("unknown flag %q", name)
	}
	return ApplyFlag(values, flag, raw)
}

// ApplyFlag applies a raw string override to one flag spec.
func ApplyFlag(values operations.FlagValues, flag spec.FlagSpec, raw string) error {
	switch flag.Type {
	case spec.FlagTypeBool:
		value, err := strconv.ParseBool(raw)
		if err != nil {
			slog.Warn("cmdflags.parse_bool_failed", "flag", flag.Name, "err", err)
			return fmt.Errorf("parse bool flag %s: %w", flag.Name, err)
		}
		setBool(values, flag, value)
		return nil
	case spec.FlagTypeString:
		setString(values, flag, raw)
		return nil
	default:
		return fmt.Errorf("unsupported flag type %q", flag.Type)
	}
}

// Find returns the visible flag spec for name.
func Find(flags []spec.FlagSpec, name string) (spec.FlagSpec, bool) {
	for _, flag := range flags {
		if flag.Name == name {
			return flag, true
		}
	}
	var zero spec.FlagSpec
	return zero, false
}

func setBool(values operations.FlagValues, flag spec.FlagSpec, value bool) {
	values.SetBool(flag.Name, value)
	values.SetBool(flag.Binding, value)
}

func setString(values operations.FlagValues, flag spec.FlagSpec, value string) {
	values.SetString(flag.Name, value)
	values.SetString(flag.Binding, value)
}
