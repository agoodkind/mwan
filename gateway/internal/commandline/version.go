package commandline

import (
	"fmt"
	"io"

	"goodkind.io/mwan/gateway/internal/yangpub"
	"goodkind.io/mwan/gateway/version"
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
