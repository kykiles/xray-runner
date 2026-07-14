package subscription

import "testing"

func TestSubEntryValidate(t *testing.T) {
	const uuid = "550e8400-e29b-41d4-a716-446655440000"

	cases := []struct {
		name    string
		entry   SubEntry
		wantErr bool
	}{
		{"vless ok", SubEntry{Protocol: "vless", Address: "a.com", Port: 443, UUID: uuid}, false},
		{"vmess ok", SubEntry{Protocol: "vmess", Address: "a.com", Port: 443, UUID: uuid}, false},
		{"ss ok", SubEntry{Protocol: "ss", Address: "a.com", Port: 8443, Method: "aes-256-gcm", Password: "p"}, false},
		{"hysteria2 ok", SubEntry{Protocol: "hysteria2", Address: "a.com", Port: 443, Password: "p"}, false},
		{"empty address", SubEntry{Protocol: "vless", Address: "", Port: 443, UUID: uuid}, true},
		{"port zero", SubEntry{Protocol: "vless", Address: "a.com", Port: 0, UUID: uuid}, true},
		{"port too high", SubEntry{Protocol: "vless", Address: "a.com", Port: 70000, UUID: uuid}, true},
		{"vless bad uuid", SubEntry{Protocol: "vless", Address: "a.com", Port: 443, UUID: "not-a-uuid"}, true},
		{"vless empty uuid", SubEntry{Protocol: "vless", Address: "a.com", Port: 443, UUID: ""}, true},
		{"ss no method", SubEntry{Protocol: "ss", Address: "a.com", Port: 8443, Password: "p"}, true},
		{"ss no password", SubEntry{Protocol: "ss", Address: "a.com", Port: 8443, Method: "aes-256-gcm"}, true},
		{"hysteria2 no password", SubEntry{Protocol: "hysteria2", Address: "a.com", Port: 443}, true},
		{"unsupported protocol", SubEntry{Protocol: "trojan", Address: "a.com", Port: 443}, true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.entry.Validate()
			if (err != nil) != c.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, c.wantErr)
			}
		})
	}
}

func TestIsUUID(t *testing.T) {
	valid := []string{
		"550e8400-e29b-41d4-a716-446655440000",
		"FFFFFFFF-FFFF-FFFF-FFFF-FFFFFFFFFFFF",
	}
	invalid := []string{
		"",
		"550e8400e29b41d4a716446655440000",
		"550e8400-e29b-41d4-a716-44665544000",  // too short
		"550e8400-e29b-41d4-a716-4466554400000", // too long
		"550e8400-e29b-41d4-a716-44665544000g",  // non-hex
	}
	for _, s := range valid {
		if !isUUID(s) {
			t.Errorf("isUUID(%q) = false, want true", s)
		}
	}
	for _, s := range invalid {
		if isUUID(s) {
			t.Errorf("isUUID(%q) = true, want false", s)
		}
	}
}
