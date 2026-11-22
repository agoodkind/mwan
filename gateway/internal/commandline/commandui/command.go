// Package commandui builds shared Cobra commands and validates their
// arguments.
package commandui

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"
)

// Leaf creates a Cobra command with help text and positional argument
// checks.
//
// It sets the command name, short and long descriptions, example, and
// validator.
//
// Example:
//
//	command := commandui.Leaf(
//		"version", "Print version.", "mwan version", cobra.NoArgs,
//	)
func Leaf(use, description, example string, arguments cobra.PositionalArgs) *cobra.Command {
	return &cobra.Command{
		Use:     use,
		Short:   description,
		Long:    description,
		Example: example,
		Args:    arguments,
	}
}

// RequiredArguments creates a validator for nonempty positional arguments.
//
// Cobra checks the argument count, and this validator rejects empty values.
//
// Example:
//
//	args := commandui.RequiredArguments(2)
func RequiredArguments(count int) cobra.PositionalArgs {
	return func(command *cobra.Command, arguments []string) error {
		if err := cobra.ExactArgs(count)(command, arguments); err != nil {
			return err
		}

		for index, argument := range arguments {
			if argument == "" {
				return emptyArgumentError{index: index + 1}
			}
		}

		return nil
	}
}

// ErrEmptyArgument marks a positional argument with an empty string.
var ErrEmptyArgument = errors.New("must not be empty")

type emptyArgumentError struct {
	index int
}

func (argumentError emptyArgumentError) Error() string {
	return fmt.Sprintf("argument %d %s", argumentError.index, ErrEmptyArgument)
}

func (argumentError emptyArgumentError) Unwrap() error {
	return ErrEmptyArgument
}

// ExitCodeError reports a command's requested process exit status.
//
// Cobra returns this error when a command needs a nonzero process status.
//
// Example:
//
//	err := commandui.ExitCodeError(2)
type ExitCodeError int

// Error returns the text form of an ExitCodeError value.
//
// The string includes the requested process status.
//
// Example:
//
//	err := commandui.ExitCode(2)
func (code ExitCodeError) Error() string {
	return fmt.Sprintf("command exited with status %d", int(code))
}

// Result converts an operation's status into a Cobra command result.
//
// It returns nil for status zero and an ExitCodeError for any other status.
//
// Example:
//
//	return commandui.Result(status)
func Result(code int) error {
	if code == 0 {
		return nil
	}

	return ExitCodeError(code)
}
