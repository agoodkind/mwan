package ifmgr

import "errors"

// ErrModuleDisabled tells the daemon to skip a module that lacks usable
// runtime configuration. The daemon omits it from later lifecycle calls and
// releases any resources it initialized.
var ErrModuleDisabled = errors.New("ifmgr: module disabled")
