package yangpub

/*
#include <stdlib.h>
#include <sysrepo.h>
#include <libyang/libyang.h>
*/
import "C"

import (
	"context"
	"fmt"
	"runtime/cgo"
	"time"
	"unsafe"
)

// providerTimeout bounds one provider call. A provider that blocks past
// it loses its sysrepo worker thread to a cancelled context rather than
// holding it for the life of the read.
const providerTimeout = 5 * time.Second

// lyFailed reports whether a libyang call returned anything other than
// success, so no caller open-codes the comparison against the success
// constant.
func lyFailed(result C.LY_ERR) bool {
	return result != C.LY_SUCCESS
}

// operGetCallback holds the trampoline sysrepo invokes. The only real
// caller is C, through the exported symbol, and no Go analysis can see
// that edge; this reference states it so reachability analysis agrees
// with what the program does.
var operGetCallback = yangpubOperCB

// moduleChangeCallback holds the change trampoline for the same reason.
var moduleChangeCallback = yangpubChangeCB

// notifCallback holds the notification trampoline for the same reason.
var notifCallback = yangpubNotifCB

// yangpubChangeCB is the C-visible trampoline for the change
// subscriptions OwnModule holds. They exist only to mark a module's
// running data in use; the daemon applies nothing on a change because
// the surface accepts no writes, so every event is acknowledged as is.
//
// The parameters are named because cgo emits the C prototype from them
// and rejects blank identifiers; the callback reads none of them.
//
//export yangpubChangeCB
func yangpubChangeCB(session *C.sr_session_ctx_t, subID C.uint32_t, moduleName *C.char,
	xpath *C.char, event C.sr_event_t, requestID C.uint32_t, privateData unsafe.Pointer,
) C.int {
	return C.SR_ERR_OK
}

// callNotification hands one received notification to the registered
// function and swallows a panic, for the same reason callProvider does:
// this runs on a thread that entered Go from C, and an unwinding panic
// would abort the whole process.
func callNotification(registration *notifReg, xpath string, payload string) {
	defer func() {
		if recovered := recover(); recovered != nil {
			registration.log.Error("notification callback panicked",
				"xpath", xpath, "err", fmt.Sprint(recovered))
		}
	}()
	registration.fn(xpath, payload)
}

// yangpubNotifCB is the C-visible trampoline sysrepo invokes for every
// notification a subscription receives. It recovers the Go registration
// through the handle cell in private_data, renders the notification's
// path and JSON body, and hands both to the registered function. Signal
// events (replay markers, termination, suspension) carry no notification
// data and are dropped.
//
//export yangpubNotifCB
func yangpubNotifCB(session *C.sr_session_ctx_t, subID C.uint32_t, notifType C.sr_ev_notif_type_t,
	notif *C.struct_lyd_node, timestamp *C.struct_timespec, privateData unsafe.Pointer,
) {
	if notifType != C.SR_EV_NOTIF_REALTIME && notifType != C.SR_EV_NOTIF_REPLAY {
		return
	}
	handle := cgo.Handle(*(*C.uintptr_t)(privateData))
	registration, ok := handle.Value().(*notifReg)
	if !ok || notif == nil {
		return
	}

	cPath := C.lyd_path(notif, C.LYD_PATH_STD, nil, 0)
	if cPath == nil {
		registration.log.Error("notification path render failed",
			"err", "lyd_path returned no path")
		return
	}
	defer C.free(unsafe.Pointer(cPath))

	var printed *C.char
	if lyErr := C.lyd_print_mem(&printed, notif, C.LYD_JSON, 0); lyFailed(lyErr) {
		registration.log.Error("notification print failed",
			"xpath", C.GoString(cPath), "err", fmt.Sprintf("libyang code %d", int(lyErr)))
		return
	}
	defer C.free(unsafe.Pointer(printed))

	callNotification(registration, C.GoString(cPath), C.GoString(printed))
}

// callProvider runs one provider with a deadline and turns a panic into
// an error. The panic case is the load-bearing one: this runs on a
// thread that entered Go from C, and a panic that unwinds past this
// frame aborts the whole process, so one bad provider would take the
// daemon down with it.
func callProvider(registration *providerReg, xpath string) (items []Item, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("yangpub: provider panicked: %v", recovered)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), providerTimeout)
	defer cancel()
	return registration.fn(ctx, xpath)
}

// yangpubOperCB is the C-visible trampoline sysrepo invokes for every
// operational read under a registered provider. It recovers the Go
// registration through the handle cell in private_data, asks the
// provider for its items, and grafts them onto the reply tree with
// libyang. Any provider or tree error fails only this read; the daemon
// and every other registration keep running.
//
//export yangpubOperCB
func yangpubOperCB(session *C.sr_session_ctx_t, subID C.uint32_t, moduleName *C.char,
	path *C.char, requestXPath *C.char, operationID C.uint32_t,
	parent **C.struct_lyd_node, privateData unsafe.Pointer,
) C.int {
	handle := cgo.Handle(*(*C.uintptr_t)(privateData))
	registration, ok := handle.Value().(*providerReg)
	if !ok {
		return C.SR_ERR_INTERNAL
	}

	requested := C.GoString(path)
	if requestXPath != nil && *requestXPath != 0 {
		requested = C.GoString(requestXPath)
	}
	items, err := callProvider(registration, requested)
	if err != nil {
		registration.log.Error("provider callback failed",
			"xpath", requested, "err", err)
		return C.SR_ERR_CALLBACK_FAILED
	}

	connection := C.sr_session_get_connection(session)
	lyContext := C.sr_acquire_context(connection)
	defer C.sr_release_context(connection)

	for _, item := range items {
		cPath := C.CString(item.Path)
		cValue := C.CString(item.Value)
		var lyErr C.LY_ERR
		if *parent == nil {
			lyErr = C.lyd_new_path(nil, lyContext, cPath, cValue, 0, parent)
		} else {
			lyErr = C.lyd_new_path(*parent, nil, cPath, cValue, 0, nil)
		}
		C.free(unsafe.Pointer(cPath))
		C.free(unsafe.Pointer(cValue))
		if lyFailed(lyErr) {
			buildErr := fmt.Errorf("yangpub: lyd_new_path %s: libyang code %d", item.Path, int(lyErr))
			registration.log.Error("provider tree build failed",
				"path", item.Path, "err", buildErr)
			return C.SR_ERR_CALLBACK_FAILED
		}
	}
	return C.SR_ERR_OK
}
