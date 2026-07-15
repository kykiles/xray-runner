package subscription

// Bare links (vless://, vmess://, ss://, hysteria2://) are a subscription of
// exactly one server that the user pastes by hand. They live in
// subscriptions.txt next to the http(s) subscription URLs; everything that
// fetches a subscription resolves them locally instead of over the network.

import "fmt"

// IsBareLink reports whether raw is a server link rather than a subscription
// URL. parseURL already rejects every scheme it cannot build an outbound from,
// so a successful parse is the definition of "bare link" and the two can never
// disagree.
func IsBareLink(raw string) bool {
	_, err := parseURL(raw)
	return err == nil
}

// ParseBareLink turns a single server link into an entry. It fails for
// subscription URLs and unsupported protocols.
func ParseBareLink(raw string) (*SubEntry, error) {
	e, err := parseURL(raw)
	if err != nil {
		return nil, fmt.Errorf("не удалось разобрать ссылку: %w", err)
	}
	return &e, nil
}

// bareLinkName labels a bare link in the menu. The #fragment is what the panel
// meant the server to be called; without one the endpoint is used, because the
// raw line would put the UUID on screen and vmess links are opaque base64.
func bareLinkName(raw string) string {
	e, err := ParseBareLink(raw)
	if err != nil {
		return ""
	}
	if e.Remarks != "" {
		return e.Remarks
	}
	if e.Address == "" {
		return ""
	}
	return fmt.Sprintf("%s:%d", e.Address, e.Port)
}
