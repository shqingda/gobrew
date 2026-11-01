package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shqingda/gobrew/internal/engine"
	"github.com/shqingda/gobrew/internal/model"
	"github.com/shqingda/gobrew/internal/state"
	"github.com/shqingda/gobrew/internal/testutil"
	bolt "go.etcd.io/bbolt"
)

type updateFailure struct {
	calls        int
	catalogCalls int
	cancel       context.CancelFunc
}

func (f *updateFailure) RoundTrip(req *http.Request) (*http.Response, error) {
	f.calls++
	if req.URL.Host == "formulae.brew.sh" {
		f.catalogCalls++
	}
	if f.cancel != nil {
		f.cancel()
	}
	return nil, errors.New("refresh unavailable")
}

func TestCommandAutoUpdateTriggersAndFallsBackToLocalCatalog(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		args                     []string
		fresh, disabled          bool
		wantRefresh, wantFailure bool
	}{
		{"install", []string{"install", "demo"}, false, false, true, false},
		{"upgrade", []string{"upgrade", "demo"}, false, false, true, false},
		{"outdated", []string{"outdated"}, false, false, true, false},
		{"fresh", []string{"install", "demo"}, true, false, false, false},
		{"disabled", []string{"install", "demo"}, false, true, false, false},
		{"reinstall", []string{"reinstall", "demo"}, false, false, false, true},
		{"version-install", []string{"version-install", "demo", "1"}, false, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GOBREW_NO_AUTO_UPDATE", "")
			if tc.disabled {
				t.Setenv("GOBREW_NO_AUTO_UPDATE", "1")
			}
			a, requests, current := updateApp(t)
			if tc.fresh {
				now := time.Now()
				if err := os.Chtimes(current, now, now); err != nil {
					t.Fatal(err)
				}
			}
			var stdout bytes.Buffer
			code := a.run(context.Background(), append(tc.args, "--json"), &stdout, BuildInfo{})
			if (code != 0) != tc.wantFailure || !json.Valid(stdout.Bytes()) || a.stderr.(*bytes.Buffer).Len() != 0 {
				t.Fatalf("exit=%d stdout=%s stderr=%s", code, &stdout, a.stderr)
			}
			if (requests.catalogCalls == 1) != tc.wantRefresh || requests.catalogCalls > 1 {
				t.Fatalf("catalog refreshes=%d, want refresh=%v", requests.catalogCalls, tc.wantRefresh)
			}
			if tc.wantFailure && !strings.Contains(stdout.String(), "refresh unavailable") {
				t.Fatalf("reinstall failed before reaching the offline artifact download: %s", &stdout)
			}
		})
	}
}

func TestCancellationDuringAutoUpdateDoesNotInstall(t *testing.T) {
	t.Setenv("GOBREW_NO_AUTO_UPDATE", "")
	a, requests, _ := updateApp(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	requests.cancel = cancel
	before, err := a.engine.Installed()
	if err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	code := a.run(ctx, []string{"install", "demo", "--json"}, &stdout, BuildInfo{})
	if code != 130 || !json.Valid(stdout.Bytes()) || !strings.Contains(stdout.String(), `"code":"canceled"`) {
		t.Fatalf("exit=%d stdout=%s", code, &stdout)
	}
	after, err := a.engine.Installed()
	if err != nil || after.Generation != before.Generation || requests.calls != 1 {
		t.Fatalf("canceled update continued installation: generation=%d, requests=%d, error=%v", after.Generation, requests.calls, err)
	}
}

func TestInstalledNoopDoesNotClaimLatestWithoutCatalog(t *testing.T) {
	t.Setenv("GOBREW_NO_AUTO_UPDATE", "1")
	a, requests, current := updateApp(t)
	if err := os.RemoveAll(filepath.Dir(current)); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	code := a.run(context.Background(), []string{"install", "demo"}, &stdout, BuildInfo{})
	output := a.stderr.(*bytes.Buffer).String()
	if code != 0 || requests.calls != 0 {
		t.Fatalf("offline no-op: exit=%d requests=%d stdout=%s stderr=%s", code, requests.calls, &stdout, output)
	}
	if strings.Contains(output, "up-to-date") || !strings.Contains(output, "demo 1 is already installed") || !strings.Contains(output, "gobrew reinstall demo") {
		t.Fatalf("no-op must report the installed version without claiming it is latest: %s", output)
	}
}

// A local catalog fixture lets commands continue after a failed refresh without
// making tests depend on a live Homebrew snapshot or weakening verification.
func updateApp(t *testing.T) (*application, *updateFailure, string) {
	t.Helper()
	return updateAppWithCatalog(t, "1")
}

// updateAppWithCatalog installs demo 1 while the local catalog offers version.
func updateAppWithCatalog(t *testing.T, version string) (*application, *updateFailure, string) {
	t.Helper()
	prefix, cache := t.TempDir(), t.TempDir()
	store := state.Store{Prefix: prefix}
	if err := store.Initialize(); err != nil {
		t.Fatal(err)
	}
	f := model.Formula{Name: "demo", Version: "1", Bottles: map[string]model.Bottle{"arm64_sequoia": {
		Tag: "arm64_sequoia", Cellar: ":any", SHA256: strings.Repeat("b", 64),
		URL: "https://ghcr.io/v2/homebrew/core/demo/blobs/sha256:" + strings.Repeat("b", 64),
	}}}
	keg, owner := testutil.OwnedKeg(t, prefix, "demo", "1")
	if err := store.WithLock(context.Background(), func(tx *state.Session) error {
		return tx.Save(model.Snapshot{Records: map[string]model.Record{"demo": {
			Formula: f, Keg: keg, Owner: owner, Status: model.Ready, Requested: true,
		}}})
	}); err != nil {
		t.Fatal(err)
	}
	host := model.Platform{OS: "darwin", Arch: "arm64", Major: 15}
	generation := strings.Repeat("c", 64)
	dir := filepath.Join(cache, "catalog")
	if err := os.MkdirAll(filepath.Join(dir, "snapshots"), 0700); err != nil {
		t.Fatal(err)
	}
	db, err := bolt.Open(filepath.Join(dir, "snapshots", generation+".db"), 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		meta, err := tx.CreateBucket([]byte("meta"))
		if err != nil {
			return err
		}
		platform, _ := json.Marshal(host)
		for key, value := range map[string][]byte{"generation": []byte(generation), "schema": []byte("1"), "platform": platform} {
			if err := meta.Put([]byte(key), value); err != nil {
				return err
			}
		}
		formulae, err := tx.CreateBucket([]byte("formulae"))
		if err != nil {
			return err
		}
		offered := f
		offered.Version = version
		data, _ := json.Marshal(offered)
		return formulae.Put([]byte("demo"), data)
	})
	if err := errors.Join(err, db.Close()); err != nil {
		t.Fatal(err)
	}
	current := filepath.Join(dir, "CURRENT")
	if err := os.WriteFile(current, []byte(generation+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-25 * time.Hour)
	if err := os.Chtimes(current, stale, stale); err != nil {
		t.Fatal(err)
	}
	failure := &updateFailure{}
	config := engine.Config{Prefix: prefix, CacheDir: cache, Platform: host, HTTP: &http.Client{Transport: failure}}
	a := &application{config: config, engine: engine.New(config), stderr: &bytes.Buffer{}, systemPrefix: engine.SystemPrefix}
	return a, failure, current
}

func TestDryRunDoesNotAutoUpdate(t *testing.T) {
	t.Setenv("GOBREW_NO_AUTO_UPDATE", "")
	for _, action := range []string{"install", "upgrade"} {
		t.Run(action, func(t *testing.T) {
			a, requests, current := updateApp(t)
			before, _ := os.Stat(current)
			snapshot, _ := a.engine.Installed()
			var stdout bytes.Buffer
			code := a.run(context.Background(), []string{action, "demo", "--dry-run", "--json"}, &stdout, BuildInfo{})
			if code != 0 || !json.Valid(stdout.Bytes()) || requests.calls != 0 {
				t.Fatalf("dry-run: exit=%d requests=%d output=%s stderr=%s", code, requests.calls, &stdout, a.stderr)
			}
			var result struct {
				Data model.Plan `json:"data"`
			}
			if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || result.Data.Request.Action != action || len(result.Data.Unchanged) != 1 || result.Data.Unchanged[0] != "demo" {
				t.Fatalf("dry-run did not return the requested plan: %s (%v)", &stdout, err)
			}
			after, _ := os.Stat(current)
			installed, _ := a.engine.Installed()
			if !after.ModTime().Equal(before.ModTime()) || installed.Generation != snapshot.Generation {
				t.Fatal("dry-run changed catalog freshness or installed state")
			}
			if _, err := os.Stat(filepath.Join(a.config.CacheDir, ".cleaned")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("dry-run started a cleanup period")
			}
		})
	}
}

func TestHeldUpgradeExitsOneWithItsOwnJSONCode(t *testing.T) {
	t.Setenv("GOBREW_NO_INSTALL_UPGRADE", "1")
	a, requests, _ := updateAppWithCatalog(t, "2")
	var stdout bytes.Buffer
	code := a.run(context.Background(), []string{"install", "demo", "--json"}, &stdout, BuildInfo{})
	if code != 1 || requests.calls != 0 || !strings.Contains(stdout.String(), `"code":"held"`) || !strings.Contains(stdout.String(), "gobrew upgrade demo") {
		t.Fatalf("held install: exit=%d requests=%d stdout=%s", code, requests.calls, &stdout)
	}
}

func TestUsageErrorsPointToTheCommandHelp(t *testing.T) {
	a, _, _ := updateApp(t)
	code := a.run(context.Background(), []string{"install"}, &bytes.Buffer{}, BuildInfo{})
	if output := a.stderr.(*bytes.Buffer).String(); code != 2 || !strings.Contains(output, "Run 'gobrew install --help' for usage.") {
		t.Fatalf("exit=%d stderr=%s", code, output)
	}
}

func TestInstallCompletionUsesTheLocalCatalogOffline(t *testing.T) {
	a, requests, _ := updateApp(t)
	names, err := a.engine.FormulaNames("de")
	if err != nil || strings.Join(names, ",") != "demo" || requests.calls != 0 {
		t.Fatalf("names=%v err=%v requests=%d", names, err, requests.calls)
	}
	if names, err := a.engine.FormulaNames("x"); err != nil || len(names) != 0 {
		t.Fatalf("unexpected names %v: %v", names, err)
	}
}

func TestUpdateSkipsBuildsThatCannotSelfUpdate(t *testing.T) {
	for _, version := range []string{"dev", "v0.3.0"} {
		// The test binary is neither a release build nor a managed install.
		a := &application{build: BuildInfo{Version: version}}
		result, err := a.updateCLI(context.Background())
		if err != nil || result.Skipped == "" || result.Changed {
			t.Fatalf("%s: updateCLI = %+v, %v", version, result, err)
		}
	}
}

func TestAutoUpdateRunsOnlyWhenStaleAndNeverBlocksTheCommand(t *testing.T) {
	t.Setenv("GOBREW_NO_AUTO_UPDATE", "")
	f := newFakeApp(t, filepath.Join(t.TempDir(), "prefix"), false, func() error { return nil })
	current := filepath.Join(f.config.CacheDir, "catalog", "CURRENT")
	if err := os.MkdirAll(filepath.Dir(current), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(current, []byte("x\n"), 0600); err != nil {
		t.Fatal(err)
	}
	e, _ := f.getEngine()
	stderr := f.stderr.(*bytes.Buffer)
	if err := f.autoUpdate(context.Background(), e, true); err != nil || stderr.Len() != 0 {
		t.Fatalf("fresh catalog auto-updated: %v %q", err, stderr)
	}
	stale := time.Now().Add(-25 * time.Hour)
	if err := os.Chtimes(current, stale, stale); err != nil {
		t.Fatal(err)
	}
	// The offline engine makes the refresh fail; Homebrew then warns and continues.
	if err := f.autoUpdate(context.Background(), e, true); err != nil {
		t.Fatal(err)
	}
	out := stderr.String()
	for _, want := range []string{"==> Auto-updating gobrew...", "`$GOBREW_NO_AUTO_UPDATE=1`", "Warning: auto-update failed"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in %q", want, out)
		}
	}
	stderr.Reset()
	t.Setenv("GOBREW_AUTO_UPDATE_SECS", "999999999")
	if err := f.autoUpdate(context.Background(), e, true); err != nil || stderr.Len() != 0 {
		t.Fatalf("custom interval ignored: %v %q", err, stderr)
	}
}
