package xraycfg

import "encoding/json"

// Ports the split-tunnel nft rules redirect into. They sit next to the SOCKS/HTTP
// pair (10808/10809) so one glance at the port table covers every local listener.
const (
	RedirectPort = 10810
	RedirectDNS  = 10853
)

// DokodemoSettings is the dokodemo-door inbound settings block. With
// FollowRedirect set, xray reads the pre-NAT destination out of the socket, so
// a connection the kernel redirected here still reaches its real target.
type DokodemoSettings struct {
	Address        string `json:"address,omitempty"`
	Port           int    `json:"port,omitempty"`
	Network        string `json:"network"`
	FollowRedirect bool   `json:"followRedirect,omitempty"`
}

// BuildRedirectInbounds returns the two listeners the split tunnel needs: TCP
// traffic from the selected processes, and their DNS.
//
// The DNS one is not optional. Without it the process resolves through the
// host's resolver — outside the tunnel — and a blocked or poisoned answer
// defeats the redirect that was about to work.
func BuildRedirectInbounds() []Inbound {
	tcp, _ := json.Marshal(DokodemoSettings{Network: "tcp", FollowRedirect: true})
	// A fixed address is what makes UDP work through a NAT redirect: the reply
	// leaves the same socket the request arrived on, so conntrack can map it back.
	dns, _ := json.Marshal(DokodemoSettings{Address: "1.1.1.1", Port: 53, Network: "udp"})

	return []Inbound{
		{
			Tag:      "redirect",
			Port:     RedirectPort,
			Listen:   "127.0.0.1",
			Protocol: "dokodemo-door",
			Settings: tcp,
			// routeOnly, like every other inbound: the sniffed domain is for the
			// routing rules only. Without it xray replaces the destination the
			// kernel handed us with the domain, and the exit node resolves it
			// again — the app ends up on a different host than the one it dialled.
			Sniffing: &SniffingConfig{
				Enabled:      true,
				RouteOnly:    true,
				DestOverride: []string{"http", "tls"},
			},
		},
		{
			Tag:      "redirect-dns",
			Port:     RedirectDNS,
			Listen:   "127.0.0.1",
			Protocol: "dokodemo-door",
			Settings: dns,
		},
	}
}
