package subscription

import (
	"net/http"
	"testing"
)

// The bodies are what sub.gachiakkuta.ru actually answers: a real subscription
// never routes to 0.0.0.0, a stubbed one has nothing else.
func TestStubReason(t *testing.T) {
	stubBody := []byte(`[{"outbounds":[{"settings":{"vnext":[{"address":"0.0.0.0","port":1}]}}],"remarks":"Limit of devices reached"}]`)
	realBody := []byte(`[{"outbounds":[{"settings":{"vnext":[{"address":"de.example.com","port":443}]}}],"remarks":"🇩🇪 Германия"}]`)

	limit := http.Header{
		"X-Hwid-Max-Devices-Reached": {"true"},
		// base64 of "лимит устройств"
		"Announce": {"base64:0LvQuNC80LjRgiDRg9GB0YLRgNC+0LnRgdGC0LI="},
	}
	if got := stubReason(limit, stubBody); got != "лимит устройств" {
		t.Fatalf("объявление панели не подставилось: %q", got)
	}

	noAnnounce := http.Header{"X-Hwid-Not-Supported": {"true"}}
	if got := stubReason(noAnnounce, stubBody); got != "X-Hwid-Not-Supported" {
		t.Fatalf("без announce ожидалось имя заголовка, получено %q", got)
	}

	// Headers without the stub body must not kill a working subscription.
	if got := stubReason(limit, realBody); got != "" {
		t.Fatalf("рабочая подписка принята за заглушку: %q", got)
	}
	if got := stubReason(http.Header{}, stubBody); got != "" {
		t.Fatalf("заглушка без заголовков не наша забота: %q", got)
	}
}
