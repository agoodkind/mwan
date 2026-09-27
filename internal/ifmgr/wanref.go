package ifmgr

import "goodkind.io/mwan/internal/connectionid"

// WANRef identifies one connection. Name is its provider display label.
// ID selects state, routing, translation, and health records.
type WANRef struct {
	ID    connectionid.ID
	Name  string
	Iface string
}

// Key selects this connection's runtime state and preserves legacy references.
func (w WANRef) Key() string {
	if w.ID != "" {
		return w.ID.String()
	}
	return w.Name
}
