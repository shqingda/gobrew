package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func archiveFixture(t *testing.T, kind byte, duplicate bool) []byte {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	n := 1
	if duplicate {
		n = 2
	}
	for range n {
		h := &tar.Header{Name: "gobrew_0.2.0_darwin_arm64/gobrew", Mode: 0755, Typeflag: kind, Linkname: "/etc/passwd"}
		if kind == tar.TypeReg {
			h.Size = 7
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if kind == tar.TypeReg {
			if _, err := tw.Write([]byte("updated")); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestUpdateIntegrityAndNoDowngrade(t *testing.T) {
	m, source := fixture(t)
	ctx := context.Background()
	if _, err := m.Install(ctx, source, "v0.1.0"); err != nil {
		t.Fatal(err)
	}
	archive := archiveFixture(t, tar.TypeReg, false)
	checksum := fmt.Sprintf("%x  gobrew_0.2.0_darwin_arm64.tar.gz\n", sha256.Sum256(archive))
	corrupt := true
	client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		var data []byte
		switch {
		case strings.HasSuffix(r.URL.Path, "/latest"):
			data = []byte(`{"tag_name":"v0.2.0"}`)
		case strings.HasSuffix(r.URL.Path, "/checksums.txt"):
			data = []byte(checksum)
		default:
			data = archive
			if corrupt {
				data = []byte("corrupted")
			}
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(data)), Header: make(http.Header)}, nil
	})}
	before, err := os.ReadFile(filepath.Join(m.root(), "receipt.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.update(ctx, client); err == nil {
		t.Fatal("accepted corruption")
	}
	after, _ := os.ReadFile(filepath.Join(m.root(), "receipt.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("changed state on failed verification")
	}
	corrupt = false
	result, err := m.update(ctx, client)
	if err != nil || !result.Changed || result.Version != "v0.2.0" {
		t.Fatalf("%+v %v", result, err)
	}
	result, err = m.update(ctx, client)
	if err != nil || result.Changed {
		t.Fatalf("no-op: %+v %v", result, err)
	}
	if got, err := newer("v0.1.9", "v0.2.0"); err != nil || got {
		t.Fatalf("downgrade: %v %v", got, err)
	}
}

func TestReleaseArchiveRejectsLinksAndDuplicateExecutables(t *testing.T) {
	for _, tc := range []struct {
		kind      byte
		duplicate bool
	}{{tar.TypeSymlink, false}, {tar.TypeReg, true}} {
		if _, err := executable(archiveFixture(t, tc.kind, tc.duplicate), "gobrew_0.2.0_darwin_arm64/gobrew"); err == nil {
			t.Fatal("accepted unsafe member")
		}
	}
}
