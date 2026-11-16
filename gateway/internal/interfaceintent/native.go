package interfaceintent

// CloneNativeBindings copies connection and link records before a consumer owns bindings.
func CloneNativeBindings(connections []Connection) []Connection {
	cloned := append([]Connection(nil), connections...)
	for i := range cloned {
		if cloned[i].Link != nil {
			link := *cloned[i].Link
			link.Tunnel = link.Tunnel.Clone()
			cloned[i].Link = &link
		}
	}

	return cloned
}
