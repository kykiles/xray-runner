package xraycfg

import "testing"

// Sniffing on the redirect listener must stay routeOnly: the destination comes
// from the kernel (followRedirect), and letting the sniffed domain replace it
// sends the connection to whatever the exit node resolves instead.
func TestRedirectInboundSniffsForRoutingOnly(t *testing.T) {
	in := BuildRedirectInbounds()
	if len(in) != 2 {
		t.Fatalf("inbounds = %d, want 2", len(in))
	}
	if in[0].Sniffing == nil || !in[0].Sniffing.RouteOnly {
		t.Errorf("sniffing = %+v, want routeOnly", in[0].Sniffing)
	}
}
