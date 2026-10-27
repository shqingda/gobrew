package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/shqingda/gobrew/internal/model"
)

const repository = "https://github.com/shqingda/gobrew"

func releaseClient() *http.Client {
	return &http.Client{Timeout: 3 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		host := req.URL.Hostname()
		if len(via) > 5 || req.URL.Scheme != "https" || (host != "github.com" && host != "api.github.com" && !strings.HasSuffix(host, ".githubusercontent.com")) {
			return errors.New("unsafe release download redirect")
		}
		return nil
	}}
}

func get(ctx context.Context, client *http.Client, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "gobrew-self-update")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("release download: HTTP %d from %s", resp.StatusCode, req.URL.Host)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err == nil && int64(len(body)) > limit {
		err = errors.New("release response exceeds size limit")
	}
	return body, err
}

func latest(ctx context.Context, client *http.Client) (string, error) {
	data, err := get(ctx, client, "https://api.github.com/repos/shqingda/gobrew/releases/latest", 1<<20)
	if err != nil {
		return "", err
	}
	var r struct {
		Tag        string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return "", err
	}
	if r.Draft || r.Prerelease || !versionPattern.MatchString(r.Tag) {
		return "", errors.New("invalid stable release metadata")
	}
	return r.Tag, nil
}

func newer(candidate, current string) (bool, error) {
	if !versionPattern.MatchString(candidate) || !versionPattern.MatchString(current) {
		return false, errors.New("self update requires stable semantic release versions")
	}
	a, b := strings.Split(candidate[1:], "."), strings.Split(current[1:], ".")
	for i := range a {
		x, e1 := strconv.ParseUint(a[i], 10, 64)
		y, e2 := strconv.ParseUint(b[i], 10, 64)
		if e1 != nil || e2 != nil {
			return false, errors.New("invalid release version")
		}
		if x != y {
			return x > y, nil
		}
	}
	return false, nil
}

func download(ctx context.Context, client *http.Client, version string) ([]byte, error) {
	name := "gobrew_" + strings.TrimPrefix(version, "v") + "_darwin_arm64.tar.gz"
	base := repository + "/releases/download/" + version + "/"
	sums, err := get(ctx, client, base+"checksums.txt", 64<<10)
	if err != nil {
		return nil, err
	}
	digest := ""
	for _, line := range strings.Split(string(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == name {
			if digest != "" || !digestPattern.MatchString(fields[0]) {
				return nil, errors.New("invalid release checksum entry")
			}
			digest = fields[0]
		}
	}
	if digest == "" {
		return nil, errors.New("release archive checksum is missing")
	}
	archive, err := get(ctx, client, base+name, 32<<20)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(archive)
	if hex.EncodeToString(sum[:]) != digest {
		return nil, errors.New("release archive checksum mismatch")
	}
	return executable(archive, strings.TrimSuffix(name, ".tar.gz")+"/gobrew")
}

// Never extract paths from a release archive. Read precisely one regular member
// into memory, with separate limits on expanded bytes and executable size.
func executable(archive []byte, member string) ([]byte, error) {
	z, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	defer z.Close()
	expanded := &io.LimitedReader{R: z, N: (100 << 20) + 1}
	reader := tar.NewReader(expanded)
	var binary []byte
	for count := 0; count < 1000; count++ {
		h, err := reader.Next()
		if errors.Is(err, io.EOF) {
			if _, err := io.Copy(io.Discard, expanded); err != nil {
				return nil, err
			}
			if expanded.N == 0 {
				return nil, errors.New("expanded release archive exceeds size limit")
			}
			if len(binary) == 0 {
				return nil, errors.New("release executable is missing")
			}
			return binary, nil
		}
		if err != nil {
			return nil, err
		}
		if h.Name != member {
			continue
		}
		if h.Typeflag != tar.TypeReg || h.Size <= 0 || h.Size > 64<<20 || binary != nil {
			return nil, errors.New("invalid release executable entry")
		}
		binary, err = io.ReadAll(reader)
		if err != nil {
			return nil, err
		}
	}
	return nil, errors.New("too many archive members")
}

func (m Manager) Update(ctx context.Context) (Result, error) { return m.update(ctx, releaseClient()) }

func (m Manager) update(ctx context.Context, client *http.Client) (Result, error) {
	release, err := m.lock(ctx)
	if err != nil {
		return Result{}, err
	}
	r, err := m.read()
	if err == nil {
		err = m.reconcile(r)
	}
	err = errors.Join(err, release())
	if err != nil {
		return Result{}, err
	}
	if r.Active == "" {
		return Result{}, fmt.Errorf("%w: CLI is uninstalled; run the curl installer again", model.ErrNotFound)
	}
	version, err := latest(ctx, client)
	if err != nil {
		return Result{}, err
	}
	upgrade, err := newer(version, r.Version)
	if err != nil {
		return Result{}, err
	}
	if !upgrade {
		return Result{Version: r.Version, BinDir: m.BinDir}, nil
	}
	data, err := download(ctx, client, version)
	if err != nil {
		return Result{}, err
	}
	// Preparation does not hold the CLI lock. Apply rejects a changed active digest.
	temp, err := os.CreateTemp("", "gobrew-update-")
	if err != nil {
		return Result{}, err
	}
	defer os.Remove(temp.Name())
	_, err = temp.Write(data)
	err = errors.Join(err, temp.Close())
	if err != nil {
		return Result{}, err
	}
	return m.install(ctx, filepath.Clean(temp.Name()), version, r.Active)
}
