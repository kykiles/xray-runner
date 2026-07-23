package subscription

import (
	"net/http"
	"testing"
)

// The payload is the routing header a Remnawave panel actually served, trimmed
// to the fields we read.
const happRouting = happRoutingPrefix + "eyJOYW1lIjoiYWxvaGF2cG5ib3QiLCJHZW9pcHVybCI6Imh0dHBzOi8vczMucnUxLnN0b3JhZ2UuYmVnZXQuY2xvdWQvOGM0MTI4MzJjMTY2LW9wZW5oZWFydGVkLWx5cy9nZW9pcC5kYXQiLCJHZW9zaXRldXJsIjoiaHR0cHM6Ly9zMy5ydTEuc3RvcmFnZS5iZWdldC5jbG91ZC84YzQxMjgzMmMxNjYtb3BlbmhlYXJ0ZWQtbHlzL2dlb3NpdGUuZGF0In0="

func TestGeoSources(t *testing.T) {
	h := http.Header{}
	h.Set("Routing", happRouting)

	got := geoSources(h)
	if got.Empty() {
		t.Fatalf("no sources parsed: %+v", got)
	}
	if want := "https://s3.ru1.storage.beget.cloud/8c412832c166-openhearted-lys/geosite.dat"; got.SiteURL != want {
		t.Errorf("SiteURL = %q, want %q", got.SiteURL, want)
	}
	if want := "https://s3.ru1.storage.beget.cloud/8c412832c166-openhearted-lys/geoip.dat"; got.IPURL != want {
		t.Errorf("IPURL = %q, want %q", got.IPURL, want)
	}
}

// Anything that is not a usable https pair leaves the core's own databases in
// place: the files go straight to xray, so http and junk are refused.
func TestGeoSourcesRejected(t *testing.T) {
	for name, value := range map[string]string{
		"missing":    "",
		"not happ":   "https://example.com/geosite.dat",
		"not b64":    happRoutingPrefix + "!!!!",
		"not json":   happRoutingPrefix + "aGVsbG8=",
		"plain http": happRoutingPrefix + `eyJHZW9pcHVybCI6Imh0dHA6Ly9leGFtcGxlLmNvbS9nZW9pcC5kYXQiLCJHZW9zaXRldXJsIjoiaHR0cDovL2V4YW1wbGUuY29tL2dlb3NpdGUuZGF0In0=`,
	} {
		h := http.Header{}
		h.Set("Routing", value)
		if got := geoSources(h); !got.Empty() {
			t.Errorf("%s: got %+v, want empty", name, got)
		}
	}
}
