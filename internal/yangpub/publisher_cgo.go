package yangpub

/*
#cgo pkg-config: sysrepo libyang
#include <stdlib.h>
#include <sysrepo.h>
#include <sysrepo/values.h>
#include <libyang/libyang.h>

extern int yangpubOperCB(sr_session_ctx_t *session, uint32_t sub_id, const char *module_name,
        const char *path, const char *request_xpath, uint32_t operation_id,
        struct lyd_node **parent, void *private_data);
extern int yangpubChangeCB(sr_session_ctx_t *session, uint32_t sub_id, const char *module_name,
        const char *xpath, sr_event_t event, uint32_t request_id, void *private_data);
extern void yangpubNotifCB(sr_session_ctx_t *session, uint32_t sub_id, sr_ev_notif_type_t notif_type,
        struct lyd_node *notif, struct timespec *timestamp, void *private_data);
*/
import "C"

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/cgo"
	"sync"
	"unsafe"
)

// applyTimeoutMS bounds sr_apply_changes so a stuck datastore cannot
// stall the caller indefinitely.
const applyTimeoutMS = 5000

// getTimeoutMS bounds sr_get_item so a stuck datastore cannot stall a
// read indefinitely.
const getTimeoutMS = 5000

// ErrClosed means the publisher was already closed. Every entry point
// rejects a closed publisher rather than passing a freed connection back
// into sysrepo.
var ErrClosed = errors.New("yangpub: publisher is closed")

// publisher is the cgo-backed Publisher over one sysrepo connection.
// Every field below mu is guarded by it, including the connection itself,
// because Close frees the connection and any later call must see that.
type publisher struct {
	log *slog.Logger

	mu            sync.Mutex
	conn          *C.sr_conn_ctx_t
	closed        bool
	subscriptions []*operSubscription
}

// connection returns the live connection, or an error once Close ran.
// The caller holds no lock; this takes and releases mu itself.
func (p *publisher) connection() (*C.sr_conn_ctx_t, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.conn == nil {
		return nil, ErrClosed
	}
	return p.conn, nil
}

// operSubscription holds everything one registration owns: the
// long-lived session the subscription is tied to, the subscription
// itself, and, for a provider registration, the cgo handle the C
// callback resolves with the C-allocated cell that carries it across the
// language boundary. A module ownership registration carries no handle;
// its cell is nil.
type operSubscription struct {
	session      *C.sr_session_ctx_t
	subscription *C.sr_subscription_ctx_t
	handle       cgo.Handle
	handleCell   unsafe.Pointer
}

// providerReg is the value the C callback recovers through its handle.
type providerReg struct {
	log *slog.Logger
	fn  ProviderFunc
}

// notifReg is the value the notification callback recovers through its
// handle.
type notifReg struct {
	log *slog.Logger
	fn  NotificationFunc
}

// New connects to sysrepo and returns the cgo-backed publisher. A failed
// connect leaves nothing behind; the caller keeps running without a
// management surface.
func New(log *slog.Logger) (Publisher, error) {
	var conn *C.sr_conn_ctx_t
	if rc := C.sr_connect(C.sr_conn_options_t(0), &conn); srFailed(rc) {
		connectErr := srError("sr_connect", rc)
		log.Error("sysrepo connect failed", "err", connectErr)
		return nil, connectErr
	}
	return &publisher{
		log:           log,
		mu:            sync.Mutex{},
		conn:          conn,
		closed:        false,
		subscriptions: nil,
	}, nil
}

// srErrorString renders a sysrepo return code as its message.
func srErrorString(returnCode C.int) string {
	return C.GoString(C.sr_strerror(returnCode))
}

// srFailed reports whether a sysrepo call returned anything other than
// success. Every call site reads the same way through it, so no caller
// open-codes the comparison against the success constant.
func srFailed(returnCode C.int) bool {
	return returnCode != C.SR_ERR_OK
}

// srError names the failed sysrepo call and carries its message, so a
// caller returns and logs one value instead of formatting the same text
// twice.
func srError(operation string, returnCode C.int) error {
	return fmt.Errorf("yangpub: %s: %s", operation, srErrorString(returnCode))
}

// startSession opens a sysrepo session on ds over the live connection.
// Every entry point needs one, and each previously repeated the same
// call, check, and error wording.
func (p *publisher) startSession(ctx context.Context, ds C.sr_datastore_t) (*C.sr_session_ctx_t, error) {
	conn, err := p.connection()
	if err != nil {
		return nil, err
	}
	var session *C.sr_session_ctx_t
	returnCode := C.sr_session_start(conn, ds, &session)
	if srFailed(returnCode) {
		startErr := srError("sr_session_start", returnCode)
		p.log.ErrorContext(ctx, "sysrepo session start failed", "err", startErr)
		return nil, startErr
	}
	return session, nil
}

// datastoreOf maps the package datastore names onto sysrepo's enum.
func datastoreOf(ds Datastore) (C.sr_datastore_t, error) {
	switch ds {
	case DatastoreRunning:
		return C.SR_DS_RUNNING, nil
	case DatastoreOperational:
		return C.SR_DS_OPERATIONAL, nil
	}
	return C.SR_DS_RUNNING, fmt.Errorf("yangpub: unknown datastore %q", string(ds))
}

// SetItems sets every item by path in ds and applies them as one change.
func (p *publisher) SetItems(ctx context.Context, ds Datastore, items []Item) error {
	return p.edit(ctx, ds, nil, items)
}

// ReplaceItems deletes deletePaths, sets items, and applies the edit as one
// change. Deletes are non-strict, so a path that holds nothing is a no-op
// rather than an error; that is what lets a publisher clear a subtree it
// may or may not have written on an earlier run.
func (p *publisher) ReplaceItems(ctx context.Context, ds Datastore, deletePaths []string, items []Item) error {
	return p.edit(ctx, ds, deletePaths, items)
}

// edit is the one write path: open a session on ds, stage every delete and
// then every set, and apply once. SetItems and ReplaceItems are the two
// public shapes of it.
func (p *publisher) edit(ctx context.Context, ds Datastore, deletePaths []string, items []Item) error {
	if err := ctx.Err(); err != nil {
		p.log.ErrorContext(ctx, "publish aborted before start", "err", err)
		return fmt.Errorf("yangpub: publish aborted: %w", err)
	}
	srDS, err := datastoreOf(ds)
	if err != nil {
		p.log.ErrorContext(ctx, "publish rejected", "err", err)
		return err
	}

	session, err := p.startSession(ctx, srDS)
	if err != nil {
		return err
	}
	defer C.sr_session_stop(session)

	for _, path := range deletePaths {
		cPath := C.CString(path)
		rc := C.sr_delete_item(session, cPath, C.sr_edit_options_t(0))
		C.free(unsafe.Pointer(cPath))
		if srFailed(rc) {
			deleteErr := srError("sr_delete_item "+path, rc)
			p.log.ErrorContext(ctx, "sysrepo delete failed", "path", path, "err", deleteErr)
			return deleteErr
		}
	}

	for _, item := range items {
		cPath := C.CString(item.Path)
		cValue := C.CString(item.Value)
		rc := C.sr_set_item_str(session, cPath, cValue, nil, C.sr_edit_options_t(0))
		C.free(unsafe.Pointer(cPath))
		C.free(unsafe.Pointer(cValue))
		if srFailed(rc) {
			setErr := srError("sr_set_item_str "+item.Path, rc)
			p.log.ErrorContext(ctx, "sysrepo set failed", "path", item.Path, "err", setErr)
			return setErr
		}
	}

	if rc := C.sr_apply_changes(session, C.uint32_t(applyTimeoutMS)); srFailed(rc) {
		applyErr := srError("sr_apply_changes", rc)
		p.log.ErrorContext(ctx, "sysrepo apply failed", "err", applyErr)
		return applyErr
	}
	return nil
}

// RegisterProvider registers fn as the operational provider of xpath
// inside module. The subscription and its session live until Close.
func (p *publisher) RegisterProvider(ctx context.Context, module string, xpath string, fn ProviderFunc) error {
	if err := ctx.Err(); err != nil {
		p.log.ErrorContext(ctx, "provider registration aborted before start", "err", err)
		return fmt.Errorf("yangpub: registration aborted: %w", err)
	}

	session, err := p.startSession(ctx, C.SR_DS_OPERATIONAL)
	if err != nil {
		return err
	}

	handle := cgo.NewHandle(&providerReg{log: p.log, fn: fn})
	handleCell := C.malloc(C.size_t(unsafe.Sizeof(C.uintptr_t(0))))
	*(*C.uintptr_t)(handleCell) = C.uintptr_t(handle)

	cModule := C.CString(module)
	cPath := C.CString(xpath)
	var subscription *C.sr_subscription_ctx_t
	// Merge mode keeps whatever the datastore already holds under xpath
	// and grafts the provider's items onto it. Without it sysrepo removes
	// every existing node under the path before the callback runs, which
	// discards the owned configuration an operational read is meant to
	// carry beside the state.
	rc := C.sr_oper_get_subscribe(session, cModule, cPath,
		C.sr_oper_get_items_cb(C.yangpubOperCB), handleCell,
		C.sr_subscr_options_t(C.SR_SUBSCR_OPER_MERGE), &subscription)
	C.free(unsafe.Pointer(cModule))
	C.free(unsafe.Pointer(cPath))
	if srFailed(rc) {
		C.free(handleCell)
		handle.Delete()
		C.sr_session_stop(session)
		subscribeErr := srError("sr_oper_get_subscribe "+xpath, rc)
		p.log.ErrorContext(ctx, "sysrepo oper subscribe failed",
			"module", module, "path", xpath, "err", subscribeErr)
		return subscribeErr
	}

	p.mu.Lock()
	p.subscriptions = append(p.subscriptions, &operSubscription{
		session:      session,
		subscription: subscription,
		handle:       handle,
		handleCell:   handleCell,
	})
	p.mu.Unlock()
	return nil
}

// OwnModule holds a change subscription on module until Close. The
// subscription is registered done-only, so sysrepo never asks it to
// verify or deny a change and the surface stays read-only through its
// access control. Its one effect is that sysrepo treats the module's
// running data as in use, which is what makes the configuration appear in
// the operational datastore beside the live state; without it an
// operational read returns only the state nodes.
func (p *publisher) OwnModule(ctx context.Context, module string) error {
	if err := ctx.Err(); err != nil {
		p.log.ErrorContext(ctx, "module ownership aborted before start", "err", err)
		return fmt.Errorf("yangpub: ownership aborted: %w", err)
	}

	session, err := p.startSession(ctx, C.SR_DS_RUNNING)
	if err != nil {
		return err
	}

	cModule := C.CString(module)
	var subscription *C.sr_subscription_ctx_t
	rc := C.sr_module_change_subscribe(session, cModule, nil,
		C.sr_module_change_cb(C.yangpubChangeCB), nil, 0,
		C.sr_subscr_options_t(C.SR_SUBSCR_DONE_ONLY), &subscription)
	C.free(unsafe.Pointer(cModule))
	if srFailed(rc) {
		C.sr_session_stop(session)
		subscribeErr := srError("sr_module_change_subscribe "+module, rc)
		p.log.ErrorContext(ctx, "sysrepo change subscribe failed",
			"module", module, "err", subscribeErr)
		return subscribeErr
	}

	p.mu.Lock()
	p.subscriptions = append(p.subscriptions, &operSubscription{
		session:      session,
		subscription: subscription,
		handle:       0,
		handleCell:   nil,
	})
	p.mu.Unlock()
	return nil
}

// SendNotification builds the notification instance with libyang and
// publishes it without waiting for any subscriber, so delivery cost never
// reaches the caller beyond the send itself.
func (p *publisher) SendNotification(ctx context.Context, path string, items []Item) error {
	if err := ctx.Err(); err != nil {
		p.log.ErrorContext(ctx, "notification aborted before start", "err", err)
		return fmt.Errorf("yangpub: notification aborted: %w", err)
	}
	session, err := p.startSession(ctx, C.SR_DS_RUNNING)
	if err != nil {
		return err
	}
	defer C.sr_session_stop(session)

	connection := C.sr_session_get_connection(session)
	lyContext := C.sr_acquire_context(connection)
	defer C.sr_release_context(connection)

	cPath := C.CString(path)
	var tree *C.struct_lyd_node
	lyErr := C.lyd_new_path(nil, lyContext, cPath, nil, 0, &tree)
	C.free(unsafe.Pointer(cPath))
	if lyFailed(lyErr) {
		buildErr := fmt.Errorf("yangpub: lyd_new_path %s: libyang code %d", path, int(lyErr))
		p.log.ErrorContext(ctx, "notification build failed", "path", path, "err", buildErr)
		return buildErr
	}
	defer C.lyd_free_all(tree)

	for _, item := range items {
		cLeaf := C.CString(item.Path)
		cValue := C.CString(item.Value)
		lyErr := C.lyd_new_path(tree, nil, cLeaf, cValue, 0, nil)
		C.free(unsafe.Pointer(cLeaf))
		C.free(unsafe.Pointer(cValue))
		if lyFailed(lyErr) {
			buildErr := fmt.Errorf("yangpub: lyd_new_path %s/%s: libyang code %d",
				path, item.Path, int(lyErr))
			p.log.ErrorContext(ctx, "notification build failed",
				"path", path, "leaf", item.Path, "err", buildErr)
			return buildErr
		}
	}

	// wait=0 publishes without waiting for subscriber callbacks, so the
	// timeout is irrelevant and a slow subscriber cannot slow the sender.
	if rc := C.sr_notif_send_tree(session, tree, 0, 0); srFailed(rc) {
		sendErr := srError("sr_notif_send_tree "+path, rc)
		p.log.ErrorContext(ctx, "notification send failed", "path", path, "err", sendErr)
		return sendErr
	}
	return nil
}

// SubscribeNotifications registers fn for every notification sent under
// module. The subscription and its session live until Close.
func (p *publisher) SubscribeNotifications(ctx context.Context, module string, fn NotificationFunc) error {
	if err := ctx.Err(); err != nil {
		p.log.ErrorContext(ctx, "notification subscription aborted before start", "err", err)
		return fmt.Errorf("yangpub: subscription aborted: %w", err)
	}

	session, err := p.startSession(ctx, C.SR_DS_RUNNING)
	if err != nil {
		return err
	}

	handle := cgo.NewHandle(&notifReg{log: p.log, fn: fn})
	handleCell := C.malloc(C.size_t(unsafe.Sizeof(C.uintptr_t(0))))
	*(*C.uintptr_t)(handleCell) = C.uintptr_t(handle)

	cModule := C.CString(module)
	var subscription *C.sr_subscription_ctx_t
	rc := C.sr_notif_subscribe_tree(session, cModule, nil, nil, nil,
		C.sr_event_notif_tree_cb(C.yangpubNotifCB), handleCell,
		C.sr_subscr_options_t(0), &subscription)
	C.free(unsafe.Pointer(cModule))
	if srFailed(rc) {
		C.free(handleCell)
		handle.Delete()
		C.sr_session_stop(session)
		subscribeErr := srError("sr_notif_subscribe_tree "+module, rc)
		p.log.ErrorContext(ctx, "sysrepo notification subscribe failed",
			"module", module, "err", subscribeErr)
		return subscribeErr
	}

	p.mu.Lock()
	p.subscriptions = append(p.subscriptions, &operSubscription{
		session:      session,
		subscription: subscription,
		handle:       handle,
		handleCell:   handleCell,
	})
	p.mu.Unlock()
	return nil
}

// InstallModules installs each model in order over the live connection.
func (p *publisher) InstallModules(ctx context.Context, models []Model, searchDirs string) error {
	if err := ctx.Err(); err != nil {
		p.log.ErrorContext(ctx, "module install aborted before start", "err", err)
		return fmt.Errorf("yangpub: install aborted: %w", err)
	}
	conn, err := p.connection()
	if err != nil {
		return err
	}
	cSearch := C.CString(searchDirs)
	defer C.free(unsafe.Pointer(cSearch))
	for _, model := range models {
		if err := installModule(conn, model, cSearch); err != nil {
			p.log.ErrorContext(ctx, "sysrepo module install failed", "path", model.Path, "err", err)
			return err
		}
	}
	return nil
}

// installModule installs one model with its features, handing sysrepo a
// NULL-terminated array of feature names or NULL when there are none.
func installModule(conn *C.sr_conn_ctx_t, model Model, cSearch *C.char) error {
	cPath := C.CString(model.Path)
	defer C.free(unsafe.Pointer(cPath))

	var cFeatures **C.char
	if len(model.Features) > 0 {
		slotSize := C.size_t(unsafe.Sizeof((*C.char)(nil)))
		block := C.malloc(slotSize * C.size_t(len(model.Features)+1))
		defer C.free(block)
		slots := unsafe.Slice((**C.char)(block), len(model.Features)+1)
		for i, feature := range model.Features {
			slots[i] = C.CString(feature)
			defer C.free(unsafe.Pointer(slots[i]))
		}
		slots[len(model.Features)] = nil
		cFeatures = (**C.char)(block)
	}
	if rc := C.sr_install_module(conn, cPath, cSearch, cFeatures); srFailed(rc) {
		return srError("sr_install_module "+model.Path, rc)
	}
	return nil
}

// ExportJSON reads the subtree at xpath in ds and prints it as JSON.
func (p *publisher) ExportJSON(ctx context.Context, ds Datastore, xpath string) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		p.log.ErrorContext(ctx, "export aborted before start", "err", err)
		return "", false, fmt.Errorf("yangpub: export aborted: %w", err)
	}
	srDS, err := datastoreOf(ds)
	if err != nil {
		p.log.ErrorContext(ctx, "export rejected", "err", err)
		return "", false, err
	}
	session, err := p.startSession(ctx, srDS)
	if err != nil {
		return "", false, err
	}
	defer C.sr_session_stop(session)

	cPath := C.CString(xpath)
	defer C.free(unsafe.Pointer(cPath))
	var data *C.sr_data_t
	rc := C.sr_get_data(session, cPath, 0, C.uint32_t(getTimeoutMS), C.sr_get_options_t(0), &data)
	if srFailed(rc) {
		getErr := srError("sr_get_data "+xpath, rc)
		p.log.ErrorContext(ctx, "sysrepo get data failed", "path", xpath, "err", getErr)
		return "", false, getErr
	}
	if data == nil || data.tree == nil {
		if data != nil {
			C.sr_release_data(data)
		}
		return "", false, nil
	}
	defer C.sr_release_data(data)

	var printed *C.char
	if lyErr := C.lyd_print_mem(&printed, data.tree, C.LYD_JSON, C.LYD_PRINT_WITHSIBLINGS); lyFailed(lyErr) {
		return "", false, fmt.Errorf("yangpub: lyd_print_mem %s: libyang code %d", xpath, int(lyErr))
	}
	defer C.free(unsafe.Pointer(printed))
	return C.GoString(printed), true, nil
}

// GetItem reads one value by path in ds.
func (p *publisher) GetItem(ctx context.Context, ds Datastore, path string) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		p.log.ErrorContext(ctx, "read aborted before start", "err", err)
		return "", false, fmt.Errorf("yangpub: read aborted: %w", err)
	}
	srDS, err := datastoreOf(ds)
	if err != nil {
		p.log.ErrorContext(ctx, "read rejected", "err", err)
		return "", false, err
	}
	session, err := p.startSession(ctx, srDS)
	if err != nil {
		return "", false, err
	}
	defer C.sr_session_stop(session)

	cPath := C.CString(path)
	defer C.free(unsafe.Pointer(cPath))
	var value *C.sr_val_t
	rc := C.sr_get_item(session, cPath, C.uint32_t(getTimeoutMS), &value)
	if rc == C.SR_ERR_NOT_FOUND {
		return "", false, nil
	}
	if srFailed(rc) {
		getErr := srError("sr_get_item "+path, rc)
		p.log.ErrorContext(ctx, "sysrepo get failed", "path", path, "err", getErr)
		return "", false, getErr
	}
	defer C.sr_free_val(value)

	printed := C.sr_val_to_str(value)
	if printed == nil {
		return "", false, fmt.Errorf("yangpub: sr_val_to_str %s returned no value", path)
	}
	defer C.free(unsafe.Pointer(printed))
	return C.GoString(printed), true, nil
}

// DeleteItem removes the value at path in ds and applies the change.
func (p *publisher) DeleteItem(ctx context.Context, ds Datastore, path string) error {
	if err := ctx.Err(); err != nil {
		p.log.ErrorContext(ctx, "delete aborted before start", "err", err)
		return fmt.Errorf("yangpub: delete aborted: %w", err)
	}
	srDS, err := datastoreOf(ds)
	if err != nil {
		p.log.ErrorContext(ctx, "delete rejected", "err", err)
		return err
	}
	session, err := p.startSession(ctx, srDS)
	if err != nil {
		return err
	}
	defer C.sr_session_stop(session)

	cPath := C.CString(path)
	rc := C.sr_delete_item(session, cPath, C.sr_edit_options_t(0))
	C.free(unsafe.Pointer(cPath))
	if srFailed(rc) {
		deleteErr := srError("sr_delete_item "+path, rc)
		p.log.ErrorContext(ctx, "sysrepo delete failed", "path", path, "err", deleteErr)
		return deleteErr
	}
	if rc := C.sr_apply_changes(session, C.uint32_t(applyTimeoutMS)); srFailed(rc) {
		applyErr := srError("sr_apply_changes", rc)
		p.log.ErrorContext(ctx, "sysrepo apply failed", "err", applyErr)
		return applyErr
	}
	return nil
}

// Close releases every registration and the connection. It is idempotent:
// a second call finds the connection already surrendered and returns nil
// rather than handing a freed pointer back to sysrepo.
func (p *publisher) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	conn := p.conn
	p.conn = nil
	subs := p.subscriptions
	p.subscriptions = nil
	p.mu.Unlock()

	for _, sub := range subs {
		if rc := C.sr_unsubscribe(sub.subscription); srFailed(rc) {
			p.log.Error("sysrepo unsubscribe failed", "err", srError("sr_unsubscribe", rc))
		}
		C.sr_session_stop(sub.session)
		if sub.handleCell != nil {
			C.free(sub.handleCell)
			sub.handle.Delete()
		}
	}
	if rc := C.sr_disconnect(conn); srFailed(rc) {
		disconnectErr := srError("sr_disconnect", rc)
		p.log.Error("sysrepo disconnect failed", "err", disconnectErr)
		return disconnectErr
	}
	return nil
}
