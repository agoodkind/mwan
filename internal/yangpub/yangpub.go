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
	// DatastoreStartup is the configuration datastore sysrepo loads into
	// running when it starts with no running data.
	DatastoreStartup Datastore = "startup"
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
	// Update lets InstallModules replace a module installed at another
	// revision with this file. It is off for the base type modules, whose
	// older revisions libyang and sysrepo load themselves.
	Update bool
}

// ModuleAction is what InstallModules did to one module.
type ModuleAction string

const (
	// ModuleInstalled means the module was absent and is now installed with
	// its features.
	ModuleInstalled ModuleAction = "installed"
	// ModuleUpdated means the module was installed at another revision and
	// now carries the file's revision.
	ModuleUpdated ModuleAction = "updated"
)

// ModuleChange is one module InstallModules changed. A module already
// installed at the file's revision produces none.
type ModuleChange struct {
	// Module is the module name.
	Module string
	// Revision is the revision the module carries now.
	Revision string
	// PriorRevision is the revision it carried before an update, and empty
	// for an install.
	PriorRevision string
	// Action says whether the module was installed or updated.
	Action ModuleAction
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

// Installer is the setup half of the datastore handle: the schema and the
// configuration the install verb puts in place before the daemon publishes.
type Installer interface {
	// InstallModules brings each model into the datastore's repository in
	// the given order, resolving imports from the colon-separated
	// searchDirs, and returns what it changed. A module that is absent is
	// installed with its features. A module whose Model sets Update and
	// that is installed at a revision other than the file's is updated to
	// the file, which keeps its stored data and its enabled features. Any
	// other installed module is left alone. The revision is read from the
	// file name, name@revision.yang.
	InstallModules(ctx context.Context, models []Model, searchDirs string) ([]ModuleChange, error)
	// ImportConfig replaces module's whole configuration in ds with the XML
	// document xml, the way `sysrepocfg --import` does: the document is
	// parsed strictly as configuration and any node it leaves out is removed.
	ImportConfig(ctx context.Context, ds Datastore, module string, xml []byte) error
}

// Publisher is the daemon's handle on the management datastore.
type Publisher interface {
	Notifier
	Installer
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
