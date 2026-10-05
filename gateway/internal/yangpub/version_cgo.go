package yangpub

// No #cgo directive here: publisher_cgo.go supplies the package's sysrepo and
// libyang flags. A second sysrepo entry would place -lsysrepo after -lyang on
// the static link line and leave libsysrepo.a's libyang references unresolved.

/*
#include <sysrepo/version.h>

static const char *yangpub_sysrepo_version(void) { return SR_VERSION; }
*/
import "C"

// SysrepoVersion returns the version of the libsysrepo this binary links. The
// release links sysrepo statically, so the header the binding compiled against
// describes the library the binary runs. sysrepo has no runtime version call,
// and reading the header value opens no connection.
func SysrepoVersion() (string, error) {
	return C.GoString(C.yangpub_sysrepo_version()), nil
}
