// Package connectionid defines stable identities for configured connections.
package connectionid

// ID identifies one configured connection independently of its provider label.
type ID string

// String returns the identity used in persisted state and runtime maps.
func (id ID) String() string { return string(id) }

// Resolve preserves provider-name identities in existing configurations.
func Resolve(explicit string, provider string, iface string) ID {
	if explicit != "" {
		return ID(explicit)
	}
	if provider != "" {
		return ID(provider)
	}
	return ID(iface)
}
