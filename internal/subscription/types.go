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

	// AllowInsecure is the local opt-in (ALLOW_INSECURE) gating whether the
	// subscription-provided Insecure flag is honored. Set by the app, never by
	// the subscription itself (S-1).
	AllowInsecure bool
}
