package ifmgr

// WANRef is the per-WAN identity every ifmgr module keys on: the WAN's stable
// name and the interface it lives on. Modules attach their own per-WAN data by
// embedding a WANRef or by keying a map on WANRef.Name; the identity itself is
// declared once here so wan.routes and npt agree on the same WAN set.
type WANRef struct {
	Name  string
	Iface string
}
