package service

// listenerUIDs has no meaning here either: the split is built on the tunnel.
func listenerUIDs(_ string, port int) []int {
	if uid, ok := listenerUID(port); ok {
		return []int{uid}
	}
	return nil
}
