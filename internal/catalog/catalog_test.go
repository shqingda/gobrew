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
	"testing"

	"github.com/shqingda/gobrew/internal/model"
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
