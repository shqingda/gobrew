package artifact

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// registryTransport answers like GHCR: blobs accept the credentials in
// accepted, and /token issues "scoped".
type registryTransport struct {
	accepted map[string]bool
	seen     []string
}

func (r *registryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.seen = append(r.seen, req.URL.Path+" "+req.Header.Get("Authorization"))
	status, body := http.StatusOK, "blob"
	switch {
	case req.URL.Path == "/token":
		body = `{"token":"scoped"}`
	case !r.accepted[req.Header.Get("Authorization")]:
		status, body = http.StatusUnauthorized, ""
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}, Request: req}, nil
}

func TestRegistryGetAvoidsTokenRoundTripUnlessRejected(t *testing.T) {
	u, _ := url.Parse("https://ghcr.io/v2/homebrew/core/jq/blobs/sha256:" + strings.Repeat("a", 64))
	for _, tc := range []struct {
		accepted string
		want     []string
	}{
		{"Bearer QQ==", []string{u.Path + " Bearer QQ=="}},
		{"Bearer scoped", []string{u.Path + " Bearer QQ==", "/token ", u.Path + " Bearer scoped"}},
	} {
		transport := &registryTransport{accepted: map[string]bool{tc.accepted: true}}
		client := &http.Client{Transport: transport}
		m := New(Config{HTTP: client})
		response, err := m.registryGet(context.Background(), client, u, u, nil)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusOK || strings.Join(transport.seen, "|") != strings.Join(tc.want, "|") {
			t.Fatalf("accepted %q: status %d, requests %q", tc.accepted, response.StatusCode, transport.seen)
		}
	}
}
