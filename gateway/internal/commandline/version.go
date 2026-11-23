package commandline

import (
	"fmt"
	"io"

	"goodkind.io/mwan/internal/version"
	"goodkind.io/mwan/internal/yangpub"
)

const sysrepoVersionUnavailable = "unavailable"

// runVersion prints build and library versions without loading configuration.
func runVersion(output io.Writer) int {
	fmt.Fprintf(
		output,
		"version=%s %s libsysrepo=%s\n",
		version.BuildVersion(),
		version.BuildVersionString(),
		linkedSysrepoVersion(),
	)

	return 0
}

func linkedSysrepoVersion() string {
	sysrepoVersion, err := yangpub.SysrepoVersion()
	if err != nil {
		return sysrepoVersionUnavailable
	}

	return sysrepoVersion
}
