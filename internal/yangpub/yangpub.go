// Package yangpub publishes daemon state into the wanconfig management
// datastore (sysrepo) and registers operational providers. It carries
// exactly what publishing needs: connect, open a session, set values by
// path, apply, and provider registration. The surface accepts no edits,
// so the package holds no write acceptance, no validation, and no
// transaction machinery beyond apply.
//
// The package binds libsysrepo through cgo, and that binding is the only
// implementation it has. The module ships one platform and builds with cgo
// on, and the go-makefile cgo hook provisions the pinned libraries where the
// gates run, so the gates check these files fully.
package yangpub

import "context"

// Datastore names a sysrepo datastore a publish targets.
type Datastore string

const (
	// DatastoreRunning is the running configuration datastore.
	DatastoreRunning Datastore = "running"
	// DatastoreOperational is the read-only operational datastore.
	DatastoreOperational Datastore = "operational"
)

// Item is one path-value pair to publish.
type Item struct {
	Path  string
	Value string
}

// ProviderFunc computes the items under xpath at the moment a read
// arrives, so the served tree cannot drift from the daemon.
type ProviderFunc func(ctx context.Context, xpath string) ([]Item, error)

// NotificationFunc receives one notification: its schema path and the
// instance printed as JSON.
type NotificationFunc func(xpath string, payloadJSON string)

// Model is one YANG module file to install, with the features to enable.
type Model struct {
	Path     string
	Features []string
}

// Notifier is the notification half of the datastore handle: sending
// the daemon's own notifications and, for the selftest, receiving them
// the way the stack's servers do.
type Notifier interface {
	// SendNotification sends one notification instance: path names the
	// notification ("/goodkind-mwan-steering:tier-change") and each item's
	// path is a leaf relative to it. Delivery is asynchronous: the call
	// returns once the notification is published, without waiting for any
	// subscriber, so a slow or absent subscriber costs the caller nothing.
	SendNotification(ctx context.Context, path string, items []Item) error
	// SubscribeNotifications delivers every notification sent under module
	// to fn until Close. The selftest uses it to prove a sent notification
	// reaches a second connection the way the stack's servers receive one.
	SubscribeNotifications(ctx context.Context, module string, fn NotificationFunc) error
}

// Publisher is the daemon's handle on the management datastore.
type Publisher interface {
	Notifier
	// InstallModules installs each model into the datastore's repository
	// in the given order, resolving imports from the colon-separated
	// searchDirs. The selftest uses it to stand up a private repository;
	// the gateway's repository is installed by the deploy.
	InstallModules(ctx context.Context, models []Model, searchDirs string) error
	// ExportJSON reads the subtree at xpath in ds and returns it printed
	// as JSON. It reports found=false when nothing is served there.
	ExportJSON(ctx context.Context, ds Datastore, xpath string) (tree string, found bool, err error)
	// GetItem reads one value by path in ds. It reports found=false when
	// the path holds no value, which lets a publisher leave state it did
	// not own unchanged.
	GetItem(ctx context.Context, ds Datastore, path string) (value string, found bool, err error)
	// SetItems sets every item by path in ds and applies them as one
	// change.
	SetItems(ctx context.Context, ds Datastore, items []Item) error
	// ReplaceItems deletes every path in deletePaths, then sets every item,
	// and applies the whole edit as one change. A publisher that owns a
	// subtree uses it to make the datastore hold exactly what it publishes:
	// stale entries from an earlier run disappear in the same transaction
	// that writes the current ones, so a reader never sees the subtree
	// empty or half-replaced. A path that holds nothing deletes as a no-op.
	ReplaceItems(ctx context.Context, ds Datastore, deletePaths []string, items []Item) error
	// DeleteItem removes the value at path in ds and applies the change.
	DeleteItem(ctx context.Context, ds Datastore, path string) error
	// RegisterProvider registers fn as the operational provider of
	// xpath inside module, so a read reaches the daemon at request
	// time. Registrations live until Close.
	RegisterProvider(ctx context.Context, module string, xpath string, fn ProviderFunc) error
	// OwnModule marks module's running data as in use by holding a change
	// subscription that applies nothing, so the configuration appears in
	// the operational datastore beside the live state. The subscription
	// lives until Close.
	OwnModule(ctx context.Context, module string) error
	// Close releases every registration and the connection.
	Close() error
}
