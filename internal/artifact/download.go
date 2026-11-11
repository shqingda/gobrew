package artifact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shqingda/gobrew/internal/model"
	"github.com/shqingda/gobrew/internal/platform"
)

// BlobPath is where a verified bottle download with the given SHA-256 lives.
func BlobPath(cacheDir, sha string) string { return filepath.Join(cacheDir, "blobs", sha) }

func (m *Manager) download(ctx context.Context, p model.Package, events chan<- model.Event) (string, error) {
	sha := p.Bottle.SHA256
	unlock, err := platform.AcquireLock(ctx, filepath.Join(m.cfg.CacheDir, "locks", "blob-"+sha))
	if err != nil {
		return "", err
	}
	defer unlock()
	return m.downloadLocked(ctx, p, events)
}

// downloadLocked is called while the blob lease spans download and extraction.
func (m *Manager) downloadLocked(ctx context.Context, p model.Package, events chan<- model.Event) (string, error) {
	sha := p.Bottle.SHA256
	path := BlobPath(m.cfg.CacheDir, sha)
	if info, err := os.Lstat(path); err == nil && info.Mode().IsRegular() {
		// Published blobs were verified while downloading. A local user's own
		// cache is trusted; doctor/re-download is the explicit integrity scan.
		touchRecency(path)
		return path, nil
	}
	u, err := url.Parse(p.Bottle.URL)
	if err != nil {
		return "", err
	}
	if u.User != nil || u.Fragment != "" || !(u.Scheme == "https" || (u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1"))) {
		return "", fmt.Errorf("invalid bottle URL")
	}
	client := *m.cfg.HTTP
	previousCheck := client.CheckRedirect
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("too many redirects")
		}
		if u.Scheme == "https" && req.URL.Scheme != "https" {
			return fmt.Errorf("refusing HTTPS downgrade")
		}
		if req.URL.Host != via[0].URL.Host {
			req.Header.Del("Authorization")
		}
		if previousCheck != nil {
			return previousCheck(req, via)
		}
		return nil
	}
	model.Emit(events, model.Event{Phase: "download", Package: p.Formula.Name})
	var n int64
	err = platform.Retry(ctx, m.retryDelays, func() (err error) {
		n, err = m.fetchBlob(ctx, &client, u, p, path, events)
		return err
	})
	if err != nil {
		return "", err
	}
	model.Emit(events, model.Event{Phase: "download", Package: p.Formula.Name, Completed: n, Total: n})
	return path, nil
}

// fetchBlob makes one attempt: it writes a new temporary file and publishes it
// only after the whole download matches the signed digest.
func (m *Manager) fetchBlob(ctx context.Context, client *http.Client, u *url.URL, p model.Package, path string, events chan<- model.Event) (int64, error) {
	sha := p.Bottle.SHA256
	idle := platform.NewIdle(ctx, m.idleTimeout())
	defer idle.Stop()
	response, err := m.registryGet(idle.Context(), client, u, u, nil)
	if err != nil {
		return 0, idle.Err(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return 0, downloadStatusError(response.StatusCode)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return 0, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".download-"+sha+"-")
	if err != nil {
		return 0, err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	hash := sha256.New()
	counter := &progressWriter{events: events, event: model.Event{Phase: "download", Package: p.Formula.Name, Total: max(response.ContentLength, 0)}}
	n, err := io.Copy(io.MultiWriter(tmp, hash, counter), idle.Reader(response.Body))
	if err != nil {
		return 0, err
	}
	if response.ContentLength >= 0 && n != response.ContentLength {
		return 0, fmt.Errorf("truncated bottle: got %d bytes, expected %d", n, response.ContentLength)
	}
	if actual := hex.EncodeToString(hash.Sum(nil)); actual != sha {
		return 0, fmt.Errorf("bottle checksum mismatch: expected %s, got %s", sha, actual)
	}
	if err := platform.SyncFile(tmp); err != nil {
		return 0, err
	}
	if err := tmp.Close(); err != nil {
		return 0, err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return 0, err
	}
	return n, platform.SyncDir(filepath.Dir(path))
}

func (m *Manager) idleTimeout() time.Duration {
	if m.idle > 0 {
		return m.idle
	}
	return platform.DefaultIdleTimeout
}

// registryGet sends one GET. GHCR requests start with Homebrew's anonymous
// credential, which public homebrew/core content accepts without a token
// round trip, and retry once with a scoped pull token if it is rejected.
func (m *Manager) registryGet(ctx context.Context, client *http.Client, u, bottle *url.URL, header http.Header) (*http.Response, error) {
	token := ""
	if u.Hostname() == "ghcr.io" {
		token = "QQ=="
	}
	request := func(token string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, err
		}
		for key, values := range header {
			req.Header[key] = values
		}
		req.Header.Set("User-Agent", model.UserAgent)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		return client.Do(req)
	}
	response, err := request(token)
	if err != nil || response.StatusCode != http.StatusUnauthorized || u.Hostname() != "ghcr.io" {
		return response, err
	}
	response.Body.Close()
	token, err = m.ghcrToken(ctx, bottle)
	if err != nil {
		return nil, err
	}
	return request(token)
}

func (m *Manager) ghcrToken(ctx context.Context, u *url.URL) (string, error) {
	parts := strings.Split(strings.TrimPrefix(u.Path, "/v2/"), "/blobs/")
	if len(parts) != 2 || !strings.HasPrefix(parts[0], "homebrew/core/") {
		return "", fmt.Errorf("unsupported GHCR repository")
	}
	tokenURL := "https://ghcr.io/token?service=ghcr.io&scope=" + url.QueryEscape("repository:"+parts[0]+":pull")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tokenURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", model.UserAgent)
	response, err := m.cfg.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", platform.StatusError(response.StatusCode, fmt.Sprintf("GHCR authentication returned HTTP %d", response.StatusCode))
	}
	var token struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&token); err != nil {
		return "", err
	}
	if token.Token == "" {
		return "", fmt.Errorf("GHCR returned an empty token")
	}
	return token.Token, nil
}

// progressWriter reports download progress at most every 100ms. Events are
// best effort, so the final completed event is still sent separately.
type progressWriter struct {
	events chan<- model.Event
	event  model.Event
	last   time.Time
}

func (w *progressWriter) Write(b []byte) (int, error) {
	w.event.Completed += int64(len(b))
	if w.events != nil && time.Since(w.last) >= 100*time.Millisecond {
		w.last = time.Now()
		model.Emit(w.events, w.event)
	}
	return len(b), nil
}

// downloadStatusError explains the HTTP failures a user can act on.
func downloadStatusError(status int) error {
	switch status {
	case http.StatusNotFound:
		return fmt.Errorf("download returned HTTP %d; the bottle is gone upstream, so update the formula catalog and retry", status)
	case http.StatusTooManyRequests, http.StatusForbidden:
		return fmt.Errorf("download returned HTTP %d; the registry is limiting requests, retry later", status)
	}
	return platform.StatusError(status, fmt.Sprintf("download returned HTTP %d", status))
}
