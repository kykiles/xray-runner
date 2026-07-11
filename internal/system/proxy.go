package system

type ProxyManager struct{}

func New() *ProxyManager {
	return &ProxyManager{}
}

func (pm *ProxyManager) Restore(s ProxyState) error {
	return WriteProxyState(s)
}
