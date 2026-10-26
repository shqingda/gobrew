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

	"github.com/shqingda/gobrew/internal/model"
	"github.com/shqingda/gobrew/internal/platform"
)

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
	path := filepath.Join(m.cfg.CacheDir, "blobs", sha)
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
	request := func(token string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "gobrew")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		return client.Do(req)
	}
	model.Emit(events, model.Event{Phase: "download", Package: p.Formula.Name})
	response, err := request("")
	if err != nil {
		return "", err
	}
	if response.StatusCode == http.StatusUnauthorized && u.Hostname() == "ghcr.io" {
		response.Body.Close()
		token, err := m.ghcrToken(ctx, u)
		if err != nil {
			return "", err
		}
		response, err = request(token)
		if err != nil {
			return "", err
		}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download returned HTTP %d", response.StatusCode)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".download-")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, hash), response.Body)
	if err != nil {
		return "", err
	}
	if response.ContentLength >= 0 && n != response.ContentLength {
		return "", fmt.Errorf("truncated bottle: got %d bytes, expected %d", n, response.ContentLength)
	}
	if actual := hex.EncodeToString(hash.Sum(nil)); actual != sha {
		return "", fmt.Errorf("bottle checksum mismatch: expected %s, got %s", sha, actual)
	}
	if err := platform.SyncFile(tmp); err != nil {
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", err
	}
	if err := platform.SyncDir(filepath.Dir(path)); err != nil {
		return "", err
	}
	model.Emit(events, model.Event{Phase: "download", Package: p.Formula.Name, Completed: n, Total: n})
	return path, nil
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
	req.Header.Set("User-Agent", "gobrew")
	response, err := m.cfg.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GHCR authentication returned HTTP %d", response.StatusCode)
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
