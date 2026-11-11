package catalog

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func TestUpdateReclaimsOldGenerationsOnly(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	version := "1.0"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(signed(t, key, `[{"name":"demo","tap":"homebrew/core","versions":{"stable":"`+version+`"},"bottle":{"stable":{"files":{}}}}]`))
	}))
	defer server.Close()
	client := Client{Dir: t.TempDir(), HTTP: server.Client(), URL: server.URL, Key: &key.PublicKey, Platform: model.Platform{OS: "darwin", Arch: "arm64", Major: 15}}
	first, err := client.Update(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(client.Dir, "snapshots", first.Generation+".db")
	abandoned := filepath.Join(client.Dir, "snapshots", ".index-crashed")
	if err := os.WriteFile(abandoned, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	version = "2.0"
	second, err := client.Update(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Both are younger than the grace period: a concurrent update may still
	// be about to record them in CURRENT.
	for _, path := range []string{old, abandoned} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("young %s removed: %v", filepath.Base(path), err)
		}
	}
	past := time.Now().Add(-2 * snapshotGrace)
	for _, path := range []string{old, abandoned, filepath.Join(client.Dir, "snapshots", second.Generation+".db")} {
		if err := os.Chtimes(path, past, past); err != nil {
			t.Fatal(err)
		}
	}
	listed, err := client.PruneSnapshots(time.Now(), true)
	if err != nil || len(listed) != 2 {
		t.Fatalf("dry run = %v, %v", listed, err)
	}
	if _, err := os.Stat(old); err != nil {
		t.Fatal("dry run removed a generation")
	}
	removed, err := client.PruneSnapshots(time.Now(), false)
	if err != nil || len(removed) != 2 {
		t.Fatalf("removed = %v, %v", removed, err)
	}
	for _, path := range []string{old, abandoned} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s kept: %v", filepath.Base(path), err)
		}
	}
	loaded, err := client.Load()
	if err != nil || loaded.Generation != second.Generation {
		t.Fatalf("current generation lost: %v, %v", loaded.Generation, err)
	}
}

// A macOS release newer than this build knows gets the newest known version's
// variation, the same one whose bottle resolver.BottleFor selects.
func TestNormalizationOnAnUnknownNewerMacOSUsesTheNewestKnownVariation(t *testing.T) {
	raw := json.RawMessage(`{"name":"demo","versions":{"stable":"1"},"dependencies":["base"],"variations":{"arm64_golden_gate":{"dependencies":["newest"]},"arm64_tahoe":{"dependencies":["older"]}}}`)
	for major, want := range map[int]string{27: "newest", 28: "newest", 26: "older"} {
		f, err := normalize(raw, model.Platform{OS: "darwin", Arch: "arm64", Major: major})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(f.Dependencies, []string{want}) {
			t.Fatalf("macOS %d dependencies = %v, want [%s]", major, f.Dependencies, want)
		}
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

func TestUpdateReusesTheIndexOfAnUnchangedCatalog(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(signed(t, key, `[{"name":"demo","tap":"homebrew/core","versions":{"stable":"1.0"},"bottle":{"stable":{"files":{}}}}]`))
	}))
	defer server.Close()
	client := Client{Dir: t.TempDir(), HTTP: server.Client(), URL: server.URL, Key: &key.PublicKey, Platform: model.Platform{OS: "darwin", Arch: "arm64", Major: 15}}
	first, err := client.Update(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// A rebuild must create a temporary index here, which now fails.
	snapshots := filepath.Join(client.Dir, "snapshots")
	if err := os.Chmod(snapshots, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(snapshots, 0700) })
	past := time.Now().Add(-2 * snapshotGrace)
	if err := os.Chtimes(filepath.Join(client.Dir, "CURRENT"), past, past); err != nil {
		t.Fatal(err)
	}
	second, err := client.Update(context.Background())
	if err != nil || second.Generation != first.Generation || second.Formulae["demo"].Version != "1.0" {
		t.Fatalf("second = %+v, %v", second, err)
	}
	if fetched, err := client.Fetched(); err != nil || time.Since(fetched) > time.Minute {
		t.Fatalf("the refresh time did not move: %v, %v", fetched, err)
	}
}
