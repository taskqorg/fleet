package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"fleet/contract"
)

// Telegram's Bot API takes the key in the URL path (/bot<key>/getMe), not in a header.
func TestKeyPathCarriesKeyInURLAndNoHeader(t *testing.T) {
	var path, auth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, auth = r.URL.Path, r.Header.Get("Authorization")
		w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()
	prefix := "/bot"
	p := provider(contract.GatewayProvider{BaseUrl: upstream.URL, KeyPath: &prefix})
	expect(t, providerURL(p, "123:abc", "/getMe"), upstream.URL+"/bot123:abc/getMe", "url")
	expect(t, keyInHeader(p, "123:abc"), "", "header value")
	g := &gateway{client: http.DefaultClient, config: &config{requestTimeout: time.Second}}
	if _, _, err := g.send(context.Background(), "POST", providerURL(p, "123:abc", "/getMe"), "", keyInHeader(p, "123:abc"), body{}, nil); err != nil {
		t.Fatal(err)
	}
	expect(t, path, "/bot123:abc/getMe", "sent path")
	expect(t, auth, "", "no Authorization header")
	plain := provider(contract.GatewayProvider{BaseUrl: upstream.URL})
	expect(t, providerURL(plain, "k", "/x"), upstream.URL+"/x", "header provider url")
	expect(t, keyInHeader(plain, "k"), "k", "header provider value")
}
