package ifmgr

import "errors"

// ErrModuleDisabled is returned from a Module.Init when the module has no
// usable runtime configuration and should be skipped for the lifetime of
// this daemon. The daemon logs the skip at INFO and drops the module from
// its dispatch list so subsequent Reconcile, OnKernelEvent, OnDHCPLease,
// and EvaluateAlerts calls bypass it entirely.
//
// Modules wrap this with extra context (e.g. via [fmt.Errorf]("%w: ...",
// ifmgr.ErrModuleDisabled, ...)) and the daemon detects it with
// [errors.Is]. The unified `oob` role uses this so a single role definition
// can list every OOB-style module, with each module turning itself off
// where its config block is absent (cloudflared_tap, wg, host_ipv6_policy
// today).
var ErrModuleDisabled = errors.New("ifmgr: module disabled")
