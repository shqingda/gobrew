package catalog

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shqingda/gobrew/internal/model"
	"github.com/shqingda/gobrew/internal/platform"
)

func signed(t *testing.T, key *rsa.PrivateKey, payload string) []byte {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"PS512","b64":false,"crit":["b64"]}`))
	digest := sha512.Sum512([]byte(header + "." + payload))
	sig, err := rsa.SignPSS(rand.Reader, key, crypto.SHA512, digest[:], &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash})
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(map[string]any{"payload": payload, "signatures": []any{map[string]any{"protected": header, "header": map[string]string{"kid": "homebrew-1"}, "signature": base64.URLEncoding.EncodeToString(sig)}}})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestUpdateKeepsLastVerifiedSnapshot(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	payload := `[{"name":"demo","tap":"homebrew/core","aliases":["d"],"versions":{"stable":"1.0"},"bottle":{"stable":{"files":{}}},"dependencies":["base"],"uses_from_macos":["expat"],"uses_from_macos_bounds":[{"since":"sequoia"}],"variations":{"arm64_sequoia":{"dependencies":["actual"]}}}]`
	body := signed(t, key, payload)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) }))
	defer server.Close()
	client := Client{Dir: t.TempDir(), HTTP: server.Client(), URL: server.URL, Key: &key.PublicKey, Platform: model.Platform{OS: "darwin", Arch: "arm64", Major: 15}}
	first, err := client.Update(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	f, err := first.Lookup("d")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.Dependencies, []string{"actual"}) {
		t.Fatalf("dependencies = %v", f.Dependencies)
	}
	body = signed(t, key, payload)
	var envelope map[string]any
	_ = json.Unmarshal(body, &envelope)
	envelope["payload"] = `[]`
	body, _ = json.Marshal(envelope)
	if _, err := client.Update(context.Background()); err == nil {
		t.Fatal("accepted a tampered payload")
	}
	loaded, err := client.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Generation != first.Generation {
		t.Fatal("unverified update replaced current snapshot")
	}
}

func TestNormalizationKeepsDependencyDeclarationOrder(t *testing.T) {
	raw := json.RawMessage(`{"name":"demo","versions":{"stable":"1"},"dependencies":["zlib","abc","zlib"],"recommended_dependencies":["mid"]}`)
	f, err := normalize(raw, model.Platform{OS: "darwin", Arch: "arm64", Major: 15})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"zlib", "abc", "mid"}; !reflect.DeepEqual(f.Dependencies, want) {
		t.Fatalf("dependencies = %v, want %v", f.Dependencies, want)
	}
}

func statusServer(t *testing.T, statuses ...int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(statuses[min(int(attempts.Add(1))-1, len(statuses)-1)])
		_, _ = w.Write([]byte("payload"))
	}))
	t.Cleanup(server.Close)
	return server, &attempts
}

func TestRequestsRetryServerFailuresOnly(t *testing.T) {
	fast := []time.Duration{time.Millisecond}
	server, attempts := statusServer(t, http.StatusBadGateway, http.StatusOK)
	c := Client{HTTP: server.Client(), retryDelays: fast}
	var data []byte
	err := platform.Retry(context.Background(), c.retryDelays, func() (err error) {
		data, err = c.download(context.Background(), server.URL)
		return err
	})
	if err != nil || string(data) != "payload" || attempts.Load() != 2 {
		t.Fatalf("catalog got %q after %d attempts: %v", data, attempts.Load(), err)
	}
	server, attempts = statusServer(t, http.StatusServiceUnavailable, http.StatusOK)
	h := History{HTTP: server.Client(), retryDelays: fast}
	if body, err := h.get(context.Background(), server.URL, "", ""); err != nil || string(body) != "payload" || attempts.Load() != 2 {
		t.Fatalf("history got %q after %d attempts: %v", body, attempts.Load(), err)
	}
	server, attempts = statusServer(t, http.StatusForbidden)
	h = History{HTTP: server.Client(), retryDelays: fast}
	if _, err := h.get(context.Background(), server.URL, "", ""); err == nil || attempts.Load() != 1 {
		t.Fatalf("403 retried: %d attempts, %v", attempts.Load(), err)
	}
}

func TestNormalizationKeepsDeclaredDependencyTags(t *testing.T) {
	raw := json.RawMessage(`{"name":"demo","versions":{"stable":"1"},"urls":{"stable":{"url":"https://example.com/demo-1.tar.lz","using":null}},"dependencies":["plain","linked"],"no_linkage_dependencies":["linked"],"build_dependencies":["tool","both"],"test_dependencies":["both"],"recommended_dependencies":["rec"],"optional_dependencies":["opt"],"uses_from_macos":[{"old":"build"},{"system":"build"}],"uses_from_macos_bounds":[{"since":"tahoe"},{}]}`)
	f, err := normalize(raw, model.Platform{OS: "darwin", Arch: "arm64", Major: 15})
	if err != nil {
		t.Fatal(err)
	}
	want := []model.DeclaredDependency{
		{Name: "lzip", Build: true, Test: true, Implicit: true},
		{Name: "plain"},
		{Name: "linked", NoLinkage: true},
		{Name: "tool", Build: true},
		{Name: "both", Build: true, Test: true},
		{Name: "rec", Recommended: true},
		{Name: "opt", Optional: true},
		{Name: "old", Build: true},
	}
	if !slices.Equal(f.DeclaredDependencies, want) {
		t.Fatalf("declared = %+v", f.DeclaredDependencies)
	}
	if runtime := []string{"plain", "linked", "rec"}; !slices.Equal(f.Dependencies, runtime) {
		t.Fatalf("runtime = %v", f.Dependencies)
	}
	for using, implicit := range map[string]int{`"nounzip"`: 0, `"git"`: 0, `"homebrew_curl"`: 2} {
		raw := json.RawMessage(`{"name":"demo","versions":{"stable":"1"},"urls":{"stable":{"url":"https://example.com/demo.7z","using":` + using + `}}}`)
		f, err := normalize(raw, model.Platform{OS: "darwin", Arch: "arm64", Major: 15})
		if err != nil || len(f.DeclaredDependencies) != implicit {
			t.Fatalf("using %s: %+v, %v", using, f.DeclaredDependencies, err)
		}
	}
}

func TestNormalizationRejectsUnknownRuntimeRequirements(t *testing.T) {
	raw := json.RawMessage(`{"name":"demo","versions":{"stable":"1"},"requirements":[{"name":"future-runtime","contexts":[],"specs":["stable"]}],"uses_from_macos":[{"zlib":"build"},"expat"],"uses_from_macos_bounds":[{},{"since":"tahoe"}]}`)
	f, err := normalize(raw, model.Platform{OS: "darwin", Arch: "arm64", Major: 15})
	if err != nil {
		t.Fatal(err)
	}
	if f.UnsupportedReason == "" {
		t.Fatal("unknown runtime requirement accepted")
	}
	if !reflect.DeepEqual(f.Dependencies, []string{"expat"}) {
		t.Fatalf("dependencies = %v", f.Dependencies)
	}
}

func TestMalformedRequirementVersionFailsClosed(t *testing.T) {
	raw := json.RawMessage(`{"name":"demo","versions":{"stable":"1"},"requirements":[{"name":"macos","version":{"unexpected":"15"},"contexts":[],"specs":["stable"]}]}`)
	f, err := normalize(raw, model.Platform{OS: "darwin", Arch: "arm64", Major: 15})
	if err != nil {
		t.Fatal(err)
	}
	if f.UnsupportedReason == "" {
		t.Fatal("malformed requirement treated as no constraint")
	}
}
