package subscription

type SubEntry struct {
	Remarks  string
	Protocol string // vless | vmess | ss | hysteria2 | trojan
	Address  string
	Port     int

	// VLESS / VMess
	UUID        string
	Encryption  string
	Flow        string
	Network     string
	Security    string
	Path        string
	Host        string
	SNI         string
	Fingerprint string
	PublicKey   string
	ShortID     string
	ALPN        string
	ServiceName string

	// SS
	Method   string
	Password string

	// Hysteria2
	Insecure     bool
	Up           string
	Down         string
	Obfs         string
	ObfsPassword string
	Congestion   string
}
