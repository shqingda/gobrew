package artifact

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/shqingda/gobrew/internal/model"
	"github.com/shqingda/gobrew/internal/platform"
)

type tarEntry struct {
	name, body, link string
	kind             byte
}

func bottleFixture(t *testing.T, entries []tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		kind := e.kind
		if kind == 0 {
			kind = tar.TypeReg
		}
		header := &tar.Header{Name: e.name, Typeflag: kind, Mode: 0755, Linkname: e.link}
		if kind == tar.TypeReg {
			header.Size = int64(len(e.body))
		}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if kind == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
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
	return buf.Bytes()
}

func TestPrepareCachesVerifiedIndependentTrees(t *testing.T) {
	data := bottleFixture(t, []tarEntry{{name: "demo/1.0/bin/demo", body: "#!/bin/sh\necho @@HOMEBREW_PREFIX@@ @@HOMEBREW_LIBRARY@@\n"}, {name: "demo/1.0/INSTALL_RECEIPT.json", body: `{"runtime_dependencies":[{"full_name":"dep","version":"2.0","revision":1}]}`}})
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); _, _ = w.Write(data) }))
	defer server.Close()
	digest := sha256.Sum256(data)
	root := t.TempDir()
	cfg := Config{CacheDir: filepath.Join(root, "cache"), Prefix: filepath.Join(root, "prefix"), Platform: model.Platform{OS: "darwin", Arch: "arm64", Major: 26}, HTTP: server.Client()}
	manager := New(cfg)
	plan := model.Plan{ID: "first", Prefix: cfg.Prefix, Platform: cfg.Platform, Packages: []model.Package{{Formula: model.Formula{Name: "demo", Version: "1.0"}, Bottle: model.Bottle{SHA256: hex.EncodeToString(digest[:]), URL: server.URL}}}}
	prepared, err := manager.Prepare(context.Background(), plan, nil)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Packages[0].CacheHit || !prepared.Packages[0].LinkageChecked || len(prepared.Packages[0].RuntimeDependencies) != 1 {
		t.Fatal("cold preparation or receipt incorrect")
	}
	materialized := filepath.Join(root, "materialized")
	if err := platform.CloneTree(context.Background(), prepared.Packages[0].Tree, materialized); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(materialized, "bin/demo"), []byte("modified"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := manager.Release(prepared); err != nil {
		t.Fatal(err)
	}
	plan.ID = "second"
	warm, err := manager.Prepare(context.Background(), plan, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Release(warm)
	if !warm.Packages[0].CacheHit || !warm.Packages[0].LinkageChecked || requests.Load() != 1 {
		t.Fatalf("warm cache missed; requests=%d", requests.Load())
	}
	content, err := os.ReadFile(filepath.Join(warm.Packages[0].Tree, "bin/demo"))
	if err != nil || !strings.Contains(string(content), filepath.Join(cfg.Prefix, "Library")) || strings.Contains(string(content), "@@HOMEBREW_") {
		t.Fatalf("tree changed or not relocated: %q %v", content, err)
	}
	other := New(Config{CacheDir: cfg.CacheDir, Prefix: cfg.Prefix + "-other", Platform: cfg.Platform})
	if manager.treeKey(plan.Packages[0]) == other.treeKey(plan.Packages[0]) {
		t.Fatal("target prefix absent from cache identity")
	}
	manager.cfg.TreeBudget = 1
	if removed, err := manager.Cleanup(context.Background()); err != nil || len(removed) != 0 {
		t.Fatalf("leased tree evicted: %v %v", removed, err)
	}
}

func TestRejectsCorruptDownloadAndUnsafeArchives(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("corrupt")) }))
	defer server.Close()
	manager := New(Config{CacheDir: t.TempDir(), HTTP: server.Client()})
	p := model.Package{Formula: model.Formula{Name: "demo", Version: "1"}, Bottle: model.Bottle{SHA256: strings.Repeat("0", 64), URL: server.URL}}
	if _, err := manager.download(context.Background(), p, nil); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("got %v", err)
	}
	for name, entries := range map[string][]tarEntry{
		"traversal":        {{name: "../escape", body: "bad"}},
		"absolute":         {{name: "/escape", body: "bad"}},
		"link escape":      {{name: "demo/link", link: "../../escape", kind: tar.TypeSymlink}},
		"link parent":      {{name: "demo/link", link: "target", kind: tar.TypeSymlink}, {name: "demo/link/child", body: "bad"}},
		"device":           {{name: "device", kind: tar.TypeFifo}},
		"ownership marker": {{name: "demo/1/.gobrew-owner", link: "/usr/local/foreign", kind: tar.TypeSymlink}},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			blob := filepath.Join(root, "bottle")
			if err := os.WriteFile(blob, bottleFixture(t, entries), 0600); err != nil {
				t.Fatal(err)
			}
			if err := extractBottle(context.Background(), blob, filepath.Join(root, "out")); err == nil {
				t.Fatal("unsafe archive accepted")
			}
		})
	}
}

func TestMachOPathRewriteAndBounds(t *testing.T) {
	old := "@@HOMEBREW_PREFIX@@/opt/dep/lib/dep.dylib"
	commandSize := (24 + len(old) + 1 + 7) &^ 7
	data := make([]byte, 32+commandSize)
	binary.LittleEndian.PutUint32(data, 0xfeedfacf)
	binary.LittleEndian.PutUint32(data[16:], 1)
	binary.LittleEndian.PutUint32(data[20:], uint32(commandSize))
	command := data[32:]
	binary.LittleEndian.PutUint32(command, 0xc)
	binary.LittleEndian.PutUint32(command[4:], uint32(commandSize))
	binary.LittleEndian.PutUint32(command[8:], 24)
	copy(command[24:], old)
	changed, fallback, err := patchMachO(data, []replacement{{"@@HOMEBREW_PREFIX@@", "/opt/gobrew"}})
	if err != nil || !changed || len(fallback) != 0 {
		t.Fatalf("rewrite: %v %v %v", changed, fallback, err)
	}
	if !bytes.Contains(data, []byte("/opt/gobrew/opt/dep/lib/dep.dylib\x00")) {
		t.Fatal("incorrect load command")
	}
	binary.LittleEndian.PutUint32(data[36:], uint32(len(data)*2))
	if _, _, err := patchMachO(data, nil); err == nil {
		t.Fatal("malformed command accepted")
	}
}

func TestRelocatesDependencySymlinksToStableOpt(t *testing.T) {
	root := t.TempDir()
	p := model.Package{Formula: model.Formula{Name: "demo", Version: "2.0"}}
	for name, target := range map[string]string{
		"dependency-file": "/opt/homebrew/Cellar/dep/1.2/lib/libdep.dylib",
		"dependency-root": "/opt/homebrew/Cellar/dep/1.2",
		"own-file":        "/opt/homebrew/Cellar/demo/2.0/share/file",
	} {
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := relocateTree(context.Background(), root, "/opt/gobrew", p); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"dependency-file": "/opt/gobrew/opt/dep/lib/libdep.dylib",
		"dependency-root": "/opt/gobrew/opt/dep",
		"own-file":        "/opt/gobrew/Cellar/demo/2.0/share/file",
	} {
		got, err := os.Readlink(filepath.Join(root, name))
		if err != nil || got != want {
			t.Fatalf("%s: got %q, want %q: %v", name, got, want, err)
		}
	}
}

func TestRejectsUnrecognizedBottlePlaceholdersWithoutKnownSubstitutions(t *testing.T) {
	for _, kind := range []string{"text", "symlink", "binary"} {
		root := t.TempDir()
		path := filepath.Join(root, "unknown")
		var err error
		if kind == "symlink" {
			err = os.Symlink("@@HOMEBREW_UNKNOWN@@/file", path)
		} else {
			data := []byte("@@HOMEBREW_UNKNOWN@@/file")
			if kind == "binary" {
				data = append(data, 0)
			}
			err = os.WriteFile(path, data, 0644)
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := relocateTree(context.Background(), root, "/opt/gobrew", model.Package{}); !errors.Is(err, model.ErrUnsupported) {
			t.Fatalf("%s: unknown placeholder accepted: %v", kind, err)
		}
	}
}

func TestBottlePrefixLinksAreRebasedAndCannotRedirectExtraction(t *testing.T) {
	root := t.TempDir()
	prefix := filepath.Join(root, "prefix")
	name := "python@3.15/3.15.1/lib/python3.15/site-packages/pip/_vendor/certifi/cacert.pem"
	target := "../../../../../../../../../etc/ca-certificates/cert.pem"
	for _, attack := range []bool{false, true} {
		t.Run(map[bool]string{false: "official certifi", true: "write through prefix link"}[attack], func(t *testing.T) {
			entries := []tarEntry{{name: name, link: target, kind: tar.TypeSymlink}}
			if attack {
				entries = append(entries, tarEntry{name: name + "/outside", body: "must not escape"})
			}
			blob := filepath.Join(t.TempDir(), "bottle")
			if err := os.WriteFile(blob, bottleFixture(t, entries), 0600); err != nil {
				t.Fatal(err)
			}
			dest := filepath.Join(t.TempDir(), "extracted")
			err := extractBottle(context.Background(), blob, dest, extractionOptions{Prefix: prefix})
			if attack {
				if err == nil {
					t.Fatal("archive redirected extraction through a prefix link")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				got, err := os.Readlink(filepath.Join(dest, filepath.FromSlash(name)))
				want := filepath.Join(prefix, "etc", "ca-certificates", "cert.pem")
				if err != nil || got != want {
					t.Fatalf("link=%q, want=%q: %v", got, want, err)
				}
			}
			if _, err := os.Lstat(prefix); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("extraction touched the planned prefix")
			}
		})
	}
	for _, unsafe := range []string{"../../../.gobrew/state.db", "../../../../etc/passwd", "../../../etc/../../outside", "../../../etc-other/file"} {
		if _, err := safeLink("demo/1/link", unsafe, prefix); err == nil {
			t.Fatalf("unsafe prefix link accepted: %q", unsafe)
		}
	}
}
