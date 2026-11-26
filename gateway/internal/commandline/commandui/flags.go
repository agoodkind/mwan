package commandui

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"goodkind.io/mwan/gateway/networkjson"
)

// OutputFormatFlag registers and validates the text or JSON output format.
//
// It sets the default value and rejects unsupported values before command
// execution.
//
// Example:
//
//	commandui.OutputFormatFlag(command, &format, "text")
func OutputFormatFlag(command *cobra.Command, format *string, defaultFormat string) {
	command.Flags().StringVar(format, "output-format", defaultFormat, "Choose text or json output.")
	command.PreRunE = func(_ *cobra.Command, _ []string) error {
		if *format != "text" && *format != "json" {
			return invalidOutputFormatError{format: *format}
		}

		return nil
	}
}

// NetworkFlags registers the WANCONFIG document and schema directory flags.
//
// The flags use the default document path and schema directory from
// networkjson.
//
// Example:
//
//	commandui.NetworkFlags(command, &networkPath, &schemaDir)
func NetworkFlags(command *cobra.Command, path, schema *string) {
	command.Flags().StringVar(
		path,
		"network",
		networkjson.DefaultPath,
		"Read the WANCONFIG document at this path.",
	)
	command.Flags().StringVar(
		schema,
		"schema-dir",
		networkjson.DefaultSchemaDir,
		"Validate WANCONFIG with YANG models from this directory.",
	)
}

// ErrInvalidOutputFormat marks a format other than text or json.
var ErrInvalidOutputFormat = errors.New("choose text or json")

type invalidOutputFormatError struct {
	format string
}

func (formatError invalidOutputFormatError) Error() string {
	return fmt.Sprintf("invalid output format %q: %s", formatError.format, ErrInvalidOutputFormat)
}

func (formatError invalidOutputFormatError) Unwrap() error {
	return ErrInvalidOutputFormat
}
