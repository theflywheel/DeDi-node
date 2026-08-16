package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// Live contract check against the real upstream: proves that /beckn/search
// still returns a usable on_discover for every domain the demo serves, which is
// the assumption this whole service rests on. Skipped unless LIVE=1, because it
// depends on a third-party host being up.
func TestLiveUpstreamAnswersEveryDomain(t *testing.T) {
	if os.Getenv("LIVE") != "1" {
		t.Skip("set LIVE=1")
	}
	for _, d := range []string{"schemes", "weather", "mandi", "news"} {
		got := make(chan []byte, 1)
		caller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			got <- b
		}))
		p := &proxy{caller: caller.URL, upstream: "https://schemes.proto.theflywheel.in",
			client: &http.Client{Timeout: 90 * time.Second}}
		q := map[string]string{"schemes": "widow pension", "weather": "Pune", "mandi": "onion", "news": "onion"}[d]
		go p.answer(map[string]any{"action": "discover", "domain": "beckn-demo:" + d},
			map[string]any{"intent": map[string]any{"q": q}}, "discover")
		select {
		case b := <-got:
			var env struct {
				Message struct {
					Catalogs []struct {
						Resources []any `json:"resources"`
					} `json:"catalogs"`
				} `json:"message"`
			}
			json.Unmarshal(b, &env)
			n := 0
			if len(env.Message.Catalogs) > 0 {
				n = len(env.Message.Catalogs[0].Resources)
			}
			t.Logf("%-8s %q -> %d catalog(s), %d resource(s)", d, q, len(env.Message.Catalogs), n)
			if n == 0 {
				t.Errorf("%s returned no resources", d)
			}
		case <-time.After(90 * time.Second):
			t.Errorf("%s: no callback", d)
		}
		caller.Close()
	}
}
