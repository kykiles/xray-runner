package subscription

import "testing"

// A18: an entry whose transport, security and flow the core cannot run is
// marked in the list and refused, the same as an unsupported security (A07).
func TestValidate_IncompatibleStream(t *testing.T) {
	base := SubEntry{
		Protocol: "vless", Address: "example.com", Port: 443,
		UUID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
	}
	cases := []struct {
		name                    string
		network, security, flow string
		ok                      bool
	}{
		{"reality over ws", "ws", "reality", "", false},
		{"vision over ws", "ws", "", "xtls-rprx-vision", false},
		{"vision without security", "tcp", "", "xtls-rprx-vision", false},
		{"reality over tcp with vision", "tcp", "reality", "xtls-rprx-vision", true},
		{"reality over grpc", "grpc", "reality", "", true},
		{"ws over tls", "ws", "tls", "", true},
		{"plain ws", "ws", "", "", true},
	}
	for _, c := range cases {
		e := base
		e.Network, e.Security, e.Flow = c.network, c.security, c.flow
		err := e.Validate()
		if c.ok && err != nil {
			t.Errorf("%s: Validate() = %v, want nil", c.name, err)
		}
		if !c.ok && err == nil {
			t.Errorf("%s: Validate() = nil, want a refusal", c.name)
		}
	}
}
