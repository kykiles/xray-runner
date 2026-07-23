package subscription

// A panel that builds configs for Happ ships its own geo databases. The
// "routing" response header carries happ://routing/add/<base64 json>, and inside
// it the URLs of the geosite.dat/geoip.dat whose lists the rules name —
// geosite:torrent, geosite:twitch-ads, geosite:whitelist, geoip:direct, none of
// which exist in the Loyalsoldier build we install. Point xray at the panel's
// databases and its rules work as written; without them xray refuses the whole
// config over the first name it cannot resolve.

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
)

// PanelInfo is what a subscription's response headers say about itself: the
// name the panel gives it, and the geo databases its routing is written against.
type PanelInfo struct {
	Title   string // the panel's own name for the subscription
	SiteURL string // geosite.dat
	IPURL   string // geoip.dat
}

// GeoEmpty reports whether the panel named no usable database pair. One without
// the other is no good: the rules name lists in both.
func (p PanelInfo) GeoEmpty() bool { return p.SiteURL == "" || p.IPURL == "" }

const happRoutingPrefix = "happ://routing/add/"

// panelInfo reads the headers a panel decorates its subscription with: the
// title, and the database URLs inside the routing header. Anything unparseable,
// or served over plain http, is ignored — these files are fed straight to xray,
// so they are only taken from a source the transport authenticates.
func panelInfo(header http.Header) PanelInfo {
	info := PanelInfo{Title: decodeHeader(header.Get("Profile-Title"))}

	payload, ok := strings.CutPrefix(strings.TrimSpace(header.Get("Routing")), happRoutingPrefix)
	if !ok {
		return info
	}
	decoded, err := b64DecodeAnyPadding(payload)
	if err != nil {
		slog.Debug("routing header is not base64", "error", err)
		return info
	}
	var routing struct {
		GeoIPURL   string `json:"Geoipurl"`
		GeoSiteURL string `json:"Geositeurl"`
	}
	if err := json.Unmarshal(decoded, &routing); err != nil {
		slog.Debug("routing header is not json", "error", err)
		return info
	}
	info.SiteURL = httpsURL(routing.GeoSiteURL)
	info.IPURL = httpsURL(routing.GeoIPURL)
	return info
}

// httpsURL passes through https URLs and drops everything else.
func httpsURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Host == "" {
		if strings.TrimSpace(raw) != "" {
			slog.Warn("подписка предлагает гео-базу не по https, игнорирую", "url", raw)
		}
		return ""
	}
	return u.String()
}
