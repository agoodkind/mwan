package commandui

import (
	"errors"
	"testing"

	"github.com/spf13/cobra"
)

func TestRequiredArgumentsReportsEmptyValue(t *testing.T) {
	validate := RequiredArguments(1)
	err := validate(&cobra.Command{}, []string{""})
	if err == nil {
		t.Fatal("empty argument returned no error")
	}

	if err.Error() != "argument 1 must not be empty" {
		t.Fatalf("error = %q", err)
	}

	if !errors.Is(err, ErrEmptyArgument) {
		t.Fatalf("errors.Is(%v, ErrEmptyArgument) = false", err)
	}
}

func TestOutputFormatFlagReportsInvalidValue(t *testing.T) {
	command := &cobra.Command{}
	format := ""
	OutputFormatFlag(command, &format, "text")
	format = "invalid"
	err := command.PreRunE(command, nil)
	if err == nil {
		t.Fatal("invalid output format returned no error")
	}

	if err.Error() != `invalid output format "invalid": choose text or json` {
		t.Fatalf("error = %q", err)
	}

	if !errors.Is(err, ErrInvalidOutputFormat) {
		t.Fatalf("errors.Is(%v, ErrInvalidOutputFormat) = false", err)
	}
}
