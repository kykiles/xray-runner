package subscription

import "encoding/json"

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
	GRPCMode    string // grpc mode: gun or multi
	Authority   string // grpc :authority
	SpiderX     string // reality spiderX (spx)
	XHTTPMode   string // xhttp mode (stream-one, packet-up, ...)

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

	// RawOutbound is the server's original outbound JSON when the entry came from
	// a full Xray-config subscription. Non-empty entries launch this verbatim
	// (only the tag is normalized) instead of being rebuilt from the fields above,
	// so no transport detail is lost. Empty for URL/bare links, which have no
	// outbound to preserve and go through BuildOutboundJSON.
	RawOutbound json.RawMessage

	// ProfileRaw is the panel config this server was extracted from. The ping
	// needs it to measure the server exactly as the session will run it — same
	// dns, same routing, same outbound chain — instead of guessing with
	// template.json. Empty for URL/bare links, which have no panel config.
	ProfileRaw json.RawMessage
}
