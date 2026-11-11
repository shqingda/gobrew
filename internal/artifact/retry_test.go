package artifact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shqingda/gobrew/internal/model"
)

// retryServer answers attempt n with handlers[n], repeating the last one.
func retryServer(t *testing.T, handlers ...http.HandlerFunc) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(attempts.Add(1)) - 1
		handlers[min(n, len(handlers)-1)](w, r)
	}))
	t.Cleanup(server.Close)
	return server, &attempts
}

// cutOff promises the whole body, sends half of it and drops the connection.
func cutOff(body []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write(body[:len(body)/2])
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	}
}

func replyStatus(status int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }
}

func replyBody(body []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) }
}

func retryManager(t *testing.T, server *httptest.Server) *Manager {
	t.Helper()
	m := New(Config{CacheDir: t.TempDir(), HTTP: server.Client()})
	m.retryDelays = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	return m
}

func retryPackage(server *httptest.Server, body []byte) model.Package {
	sum := sha256.Sum256(body)
	return model.Package{Formula: model.Formula{Name: "demo", Version: "1"}, Bottle: model.Bottle{SHA256: hex.EncodeToString(sum[:]), URL: server.URL}}
}

func TestDownloadRetriesTransportFailuresFromTheStart(t *testing.T) {
	body := []byte(strings.Repeat("bottle", 4096))
	server, attempts := retryServer(t, cutOff(body), replyStatus(http.StatusServiceUnavailable), replyBody(body))
	m := retryManager(t, server)
	events := make(chan model.Event, 64)
	path, err := m.download(context.Background(), retryPackage(server, body), events)
	if err != nil {
		t.Fatal(err)
	}
	if got := attempts.Load(); got != 3 {
		t.Fatalf("attempts = %d, want 3", got)
	}
	close(events)
	var retries []string
	for e := range events {
		if e.Phase == "output" {
			retries = append(retries, e.Message)
		}
	}
	if len(retries) != 2 || !strings.HasPrefix(retries[1], "Retrying download of demo (attempt 3 of 4)") {
		t.Fatalf("retry messages = %q", retries)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != string(body) {
		t.Fatalf("blob differs: %d bytes, %v", len(got), err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary files left behind: %v, %v", entries, err)
	}
}

func TestDownloadGivesUpAfterBoundedAttempts(t *testing.T) {
	server, attempts := retryServer(t, replyStatus(http.StatusBadGateway))
	m := retryManager(t, server)
	_, err := m.download(context.Background(), retryPackage(server, []byte("bottle")), nil)
	if err == nil || err.Error() != "download returned HTTP 502" {
		t.Fatalf("got %v", err)
	}
	if got := attempts.Load(); got != 4 {
		t.Fatalf("attempts = %d, want 4", got)
	}
}

func TestDownloadDoesNotRetryIntegrityOrClientFailures(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"digest mismatch": replyBody([]byte("corrupt")),
		"not found":       replyStatus(http.StatusNotFound),
		"forbidden":       replyStatus(http.StatusForbidden),
		"rate limited":    replyStatus(http.StatusTooManyRequests),
	} {
		t.Run(name, func(t *testing.T) {
			server, attempts := retryServer(t, handler)
			m := retryManager(t, server)
			if _, err := m.download(context.Background(), retryPackage(server, []byte("bottle")), nil); err == nil {
				t.Fatal("download succeeded")
			}
			if got := attempts.Load(); got != 1 {
				t.Fatalf("attempts = %d, want 1", got)
			}
		})
	}
}

func TestRetryWaitStopsWhenCancelled(t *testing.T) {
	server, attempts := retryServer(t, replyStatus(http.StatusServiceUnavailable))
	m := retryManager(t, server)
	m.retryDelays = []time.Duration{time.Hour}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)
	if _, err := m.download(ctx, retryPackage(server, []byte("bottle")), nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("attempts = %d, want 1", got)
	}
}

func TestOCIMetadataRetriesTruncatedResponses(t *testing.T) {
	body := []byte(strings.Repeat(`{"schemaVersion":2}`, 64))
	sum := sha256.Sum256(body)
	server, attempts := retryServer(t, cutOff(body), replyBody(body))
	m := retryManager(t, server)
	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	got, err := m.fetchOCI(context.Background(), u, u, "sha256:"+hex.EncodeToString(sum[:]))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(body) || attempts.Load() != 2 {
		t.Fatalf("got %d bytes after %d attempts", len(got), attempts.Load())
	}
}

func TestOCIMetadataDoesNotRetryDigestMismatch(t *testing.T) {
	server, attempts := retryServer(t, replyBody([]byte("{}")))
	m := retryManager(t, server)
	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.fetchOCI(context.Background(), u, u, "sha256:"+strings.Repeat("0", 64)); err == nil {
		t.Fatal("digest mismatch accepted")
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("attempts = %d, want 1", got)
	}
}

// stall sends half of the body, then waits without closing the connection.
func stall(body []byte, release <-chan struct{}) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write(body[:len(body)/2])
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}
}

// trickle sends the body in small pieces, each well within the idle timeout,
// taking longer in total than the timeout.
func trickle(body []byte, pieces int, gap time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		size := len(body) / pieces
		for i := 0; i < len(body); i += size {
			_, _ = w.Write(body[i:min(i+size, len(body))])
			w.(http.Flusher).Flush()
			time.Sleep(gap)
		}
	}
}

func TestStalledDownloadIsRetriedAndSteadyOneFinishes(t *testing.T) {
	body := []byte(strings.Repeat("bottle", 4096))
	release := make(chan struct{})
	defer close(release)
	server, attempts := retryServer(t, stall(body, release), trickle(body, 8, 20*time.Millisecond))
	m := retryManager(t, server)
	m.idle = 80 * time.Millisecond
	start := time.Now()
	path, err := m.download(context.Background(), retryPackage(server, body), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := attempts.Load(); got != 2 {
		t.Fatalf("attempts = %d, want 2", got)
	}
	// The trickle alone takes about 160ms, twice the idle timeout.
	if elapsed := time.Since(start); elapsed < 2*m.idle {
		t.Fatalf("finished in %s; the steady download was not slower than the timeout", elapsed)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != string(body) {
		t.Fatalf("blob differs: %d bytes, %v", len(got), err)
	}
}
