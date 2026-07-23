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

// GeoSources are the databases a subscription wants used with its routing.
type GeoSources struct {
	SiteURL string // geosite.dat
	IPURL   string // geoip.dat
}

// Empty reports whether the panel named no usable pair. One database without the
// other is no good: the rules name lists in both.
func (g GeoSources) Empty() bool { return g.SiteURL == "" || g.IPURL == "" }

const happRoutingPrefix = "happ://routing/add/"

// geoSources reads the panel's database URLs out of the routing header. Anything
// unparseable, or served over plain http, is ignored — these files are fed
// straight to xray, so they are only taken from a source the transport
// authenticates.
func geoSources(header http.Header) GeoSources {
	payload, ok := strings.CutPrefix(strings.TrimSpace(header.Get("Routing")), happRoutingPrefix)
	if !ok {
		return GeoSources{}
	}
	decoded, err := b64DecodeAnyPadding(payload)
	if err != nil {
		slog.Debug("routing header is not base64", "error", err)
		return GeoSources{}
	}
	var routing struct {
		GeoIPURL   string `json:"Geoipurl"`
		GeoSiteURL string `json:"Geositeurl"`
	}
	if err := json.Unmarshal(decoded, &routing); err != nil {
		slog.Debug("routing header is not json", "error", err)
		return GeoSources{}
	}
	return GeoSources{SiteURL: httpsURL(routing.GeoSiteURL), IPURL: httpsURL(routing.GeoIPURL)}
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
