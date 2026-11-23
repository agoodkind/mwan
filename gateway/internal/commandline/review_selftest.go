package commandline

import (
	"errors"
)

var errSelftestInvariant = errors.New("WANCONFIG self-test invariant failed")

type selftestInvariantError struct {
	message string
}

func (err selftestInvariantError) Error() string {
	return err.message
}

func (err selftestInvariantError) Unwrap() error {
	return errSelftestInvariant
}

func selftestInvariant(message string) error {
	return selftestInvariantError{message: message}
}
