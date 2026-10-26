package artifact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/shqingda/gobrew/internal/model"
)

const maxManifestSize = 8 << 20

type ociDescriptor struct {
	Digest      string            `json:"digest"`
	Annotations map[string]string `json:"annotations"`
}

// Modern bottles omit INSTALL_RECEIPT.json. Homebrew obtains their build-time
// runtime dependencies from sh.brew.tab in the official GHCR OCI metadata.
// https://github.com/Homebrew/brew/blob/main/Library/Homebrew/resource.rb
// A missing receipt is never interpreted as an empty dependency set.
func (m *Manager) runtimeDependencies(ctx context.Context, keg string, p model.Package) ([]model.RuntimeDependency, error) {
	deps, err := readReceipt(keg, p)
	if err == nil {
		return deps, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	base, err := url.Parse(p.Bottle.URL)
	if err != nil {
		return nil, err
	}
	// Runtime facts must come from the bottle's official repository. Loopback
	// is permitted only to exercise the exact wire contract in offline tests.
	loopback := base.Hostname() == "127.0.0.1" || base.Hostname() == "localhost" || base.Hostname() == "::1"
	if !(base.Scheme == "https" && base.Hostname() == "ghcr.io") && !(base.Scheme == "http" && loopback) {
		return nil, fmt.Errorf("%w: bottle has no receipt and no official OCI metadata source", model.ErrUnsupported)
	}
	imageName := strings.NewReplacer("@", "/", "+", "x").Replace(p.Formula.Name)
	repository := "/v2/homebrew/core/" + imageName
	if base.Path != repository+"/blobs/sha256:"+p.Bottle.SHA256 || base.User != nil || base.RawQuery != "" {
		return nil, errors.New("bottle URL does not match official formula identity")
	}
	if p.Bottle.Tag == "" || strings.ContainsAny(p.Bottle.Tag, "/\\\x00") {
		return nil, errors.New("missing or invalid bottle platform tag")
	}
	version := p.Formula.PkgVersion()
	indexTag := version
	imageRef := version + "." + p.Bottle.Tag
	if p.Formula.BottleRebuild > 0 {
		indexTag += "-" + strconv.Itoa(p.Formula.BottleRebuild)
		imageRef += "." + strconv.Itoa(p.Formula.BottleRebuild)
	}
	indexURL := *base
	indexURL.Path = repository + "/manifests/" + indexTag
	body, err := m.fetchOCI(ctx, &indexURL, base, "")
	if err != nil {
		return nil, fmt.Errorf("fetch bottle OCI index: %w", err)
	}
	var index struct {
		SchemaVersion int             `json:"schemaVersion"`
		Manifests     []ociDescriptor `json:"manifests"`
	}
	if err := json.Unmarshal(body, &index); err != nil {
		return nil, fmt.Errorf("invalid OCI index: %w", err)
	}
	if index.SchemaVersion != 2 {
		return nil, errors.New("unsupported OCI index schema")
	}
	var descriptor *ociDescriptor
	for _, candidate := range index.Manifests {
		if candidate.Annotations["sh.brew.bottle.digest"] == p.Bottle.SHA256 && candidate.Annotations["org.opencontainers.image.ref.name"] == imageRef {
			if descriptor != nil {
				return nil, errors.New("ambiguous OCI bottle metadata")
			}
			copy := candidate
			descriptor = &copy
		}
	}
	if descriptor == nil {
		return nil, fmt.Errorf("OCI index has no metadata for bottle %s (%s)", imageRef, p.Bottle.SHA256)
	}
	if !validDigest(descriptor.Digest) {
		return nil, errors.New("invalid OCI descriptor digest")
	}
	manifestURL := *base
	manifestURL.Path = repository + "/manifests/" + descriptor.Digest
	body, err = m.fetchOCI(ctx, &manifestURL, base, descriptor.Digest)
	if err != nil {
		return nil, fmt.Errorf("fetch bottle OCI manifest: %w", err)
	}
	var manifest struct {
		SchemaVersion int               `json:"schemaVersion"`
		Layers        []ociDescriptor   `json:"layers"`
		Annotations   map[string]string `json:"annotations"`
	}
	if err := json.Unmarshal(body, &manifest); err != nil {
		return nil, fmt.Errorf("invalid OCI manifest: %w", err)
	}
	if manifest.SchemaVersion != 2 || len(manifest.Layers) != 1 || manifest.Layers[0].Digest != "sha256:"+p.Bottle.SHA256 {
		return nil, errors.New("OCI manifest does not reference selected bottle digest")
	}
	// Read the tab from the content-addressed manifest, not merely mutable tag
	// annotations. Its layer must match the bottle digest in the signed catalog.
	if manifest.Annotations["sh.brew.bottle.digest"] != p.Bottle.SHA256 || manifest.Annotations["org.opencontainers.image.ref.name"] != imageRef {
		return nil, errors.New("OCI manifest identity does not match selected bottle")
	}
	tab := manifest.Annotations["sh.brew.tab"]
	if tab == "" || tab != descriptor.Annotations["sh.brew.tab"] {
		return nil, errors.New("missing or inconsistent OCI bottle receipt")
	}
	return parseRuntimeDependencies([]byte(tab))
}

func parseRuntimeDependencies(data []byte) ([]model.RuntimeDependency, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return nil, fmt.Errorf("invalid bottle receipt: %w", err)
	}
	raw, ok := object["runtime_dependencies"]
	if !ok || string(raw) == "null" {
		return nil, errors.New("bottle receipt does not declare runtime dependencies")
	}
	var deps []model.RuntimeDependency
	if err := json.Unmarshal(raw, &deps); err != nil {
		return nil, fmt.Errorf("invalid runtime dependencies: %w", err)
	}
	seen := map[string]bool{}
	for i := range deps {
		d := &deps[i]
		d.Name = strings.TrimPrefix(d.Name, "homebrew/core/")
		if d.Name == "" || d.Name == "." || d.Name == ".." || strings.ContainsAny(d.Name, "/\\\x00") || d.Version == "" || d.Revision < 0 || d.BottleRebuild < 0 || seen[d.Name] {
			return nil, fmt.Errorf("invalid runtime dependency %q", d.Name)
		}
		seen[d.Name] = true
	}
	return deps, nil
}

func validDigest(digest string) bool {
	if len(digest) != 71 || !strings.HasPrefix(digest, "sha256:") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(digest, "sha256:"))
	return err == nil && strings.ToLower(digest) == digest
}

func (m *Manager) fetchOCI(ctx context.Context, u, bottleURL *url.URL, expected string) ([]byte, error) {
	client := *m.cfg.HTTP
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("OCI metadata redirect is not allowed") }
	request := func(token string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "gobrew")
		req.Header.Set("Accept", "application/vnd.oci.image.index.v1+json, application/vnd.oci.image.manifest.v1+json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		return client.Do(req)
	}
	token := ""
	if u.Hostname() == "ghcr.io" {
		token = "QQ=="
	}
	response, err := request(token)
	if err != nil {
		return nil, err
	}
	if response.StatusCode == http.StatusUnauthorized && u.Hostname() == "ghcr.io" {
		response.Body.Close()
		token, err = m.ghcrToken(ctx, bottleURL)
		if err != nil {
			return nil, err
		}
		response, err = request(token)
		if err != nil {
			return nil, err
		}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OCI metadata returned HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxManifestSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxManifestSize {
		return nil, errors.New("OCI metadata exceeds size limit")
	}
	digest := sha256.Sum256(data)
	actual := "sha256:" + hex.EncodeToString(digest[:])
	if expected != "" && actual != expected {
		return nil, errors.New("OCI manifest checksum mismatch")
	}
	if header := response.Header.Get("Docker-Content-Digest"); header != "" && header != actual {
		return nil, errors.New("OCI response digest mismatch")
	}
	return data, nil
}
