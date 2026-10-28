package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shqingda/gobrew/internal/engine"
	"github.com/shqingda/gobrew/internal/installer"
	"github.com/shqingda/gobrew/internal/model"
	"github.com/shqingda/gobrew/internal/state"
)

type cleanupFixture struct {
	app           application
	engine        *engine.Engine
	prefix, cache string
}

func newCleanupFixture(t *testing.T) cleanupFixture {
	t.Helper()
	for _, key := range []string{"GOBREW_NO_INSTALL_CLEANUP", "GOBREW_NO_CLEANUP_FORMULAE", "GOBREW_CLEANUP_PERIODIC_FULL_DAYS", "GOBREW_CLEANUP_MAX_AGE_DAYS", "GOBREW_NO_ENV_HINTS"} {
		t.Setenv(key, "")
	}
	f := cleanupFixture{prefix: t.TempDir(), cache: t.TempDir()}
	if err := (state.Store{Prefix: f.prefix}).Initialize(); err != nil {
		t.Fatal(err)
	}
	f.engine = engine.New(engine.Config{Prefix: f.prefix, CacheDir: f.cache})
	return f
}

func (f cleanupFixture) install(t *testing.T, name, version string) model.Result {
	t.Helper()
	pkg := f.prepare(t, name, version)
	snap, err := f.engine.Installed()
	if err != nil {
		t.Fatal(err)
	}
	prepared := model.Prepared{
		Plan:     model.Plan{Prefix: f.prefix, StateGeneration: snap.Generation},
		Packages: []model.PreparedPackage{pkg},
	}
	result, err := (installer.Installer{Prefix: f.prefix}).Apply(context.Background(), prepared, nil)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func (f cleanupFixture) prepare(t *testing.T, name, version string) model.PreparedPackage {
	t.Helper()
	tree := t.TempDir()
	if err := os.Mkdir(filepath.Join(tree, "bin"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tree, "bin", name), []byte(version), 0755); err != nil {
		t.Fatal(err)
	}
	return model.PreparedPackage{Package: model.Package{Formula: model.Formula{Name: name, Version: version}}, Tree: tree}
}

func TestAfterInstallCleanupFailureWarnsAndKeepsSuccessfulInstall(t *testing.T) {
	f := newCleanupFixture(t)
	f.install(t, "demo", "1")
	result := f.install(t, "demo", "2")
	// A malformed old marker makes cleanup refuse the old keg, without
	// invalidating the new installation that already succeeded.
	oldMarker := filepath.Join(f.prefix, "Cellar", "demo", "1", ".gobrew-owner")
	if err := os.Remove(oldMarker); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(oldMarker, 0700); err != nil {
		t.Fatal(err)
	}
	notes := f.app.afterInstall(context.Background(), f.engine, result, nil)
	if !strings.Contains(strings.Join(notes, "\n"), "Warning: cleanup failed:") {
		t.Fatalf("cleanup failure was hidden: %v", notes)
	}
	snap, err := f.engine.Installed()
	if err != nil || snap.Records["demo"].Status != model.Ready || snap.Records["demo"].Formula.Version != "2" {
		t.Fatalf("cleanup failure changed successful install: %+v, %v", snap, err)
	}
	if data, err := os.ReadFile(filepath.Join(f.prefix, "bin", "demo")); err != nil || string(data) != "2" {
		t.Fatalf("successful executable lost: %q, %v", data, err)
	}
}

func TestAfterPartialInstallCleanupPreservesPackageNeedingRepair(t *testing.T) {
	f := newCleanupFixture(t)
	f.install(t, "demo", "1")
	good, broken := f.prepare(t, "demo", "2"), f.prepare(t, "broken", "1")
	broken.Package.Formula.Steps = []json.RawMessage{json.RawMessage(`{"type":"run","command":{"path":"/usr/bin/false"}}`)}
	before, err := f.engine.Installed()
	if err != nil {
		t.Fatal(err)
	}
	result, installErr := (installer.Installer{Prefix: f.prefix}).Apply(context.Background(), model.Prepared{
		Plan:     model.Plan{Prefix: f.prefix, StateGeneration: before.Generation},
		Packages: []model.PreparedPackage{good, broken},
	}, nil)
	if !errors.Is(installErr, model.ErrNeedsRepair) || len(result.Succeeded) != 1 || result.Succeeded[0] != "demo" {
		t.Fatalf("fixture did not partially succeed: %+v, %v", result, installErr)
	}
	notes := f.app.afterInstall(context.Background(), f.engine, result, installErr)
	after, err := f.engine.Installed()
	if err != nil || after.Records["demo"].Status != model.Ready || after.Records["broken"].Status != model.NeedsPostInstall {
		t.Fatalf("cleanup changed repair state: %+v, %v", after, err)
	}
	if data, err := os.ReadFile(filepath.Join(after.Records["broken"].Keg, "bin", "broken")); err != nil || string(data) != "1" {
		t.Fatalf("cleanup removed package needed for repair: %q, %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(f.prefix, "Cellar", "demo", "1")); !os.IsNotExist(err) {
		t.Fatalf("successful portion was not cleaned: %v, %v", err, notes)
	}
}

func TestAfterInstallCleansOnlyInstalledFormulae(t *testing.T) {
	f := newCleanupFixture(t)
	for _, name := range []string{"demo", "other"} {
		f.install(t, name, "1")
		f.install(t, name, "2")
	}
	notes := f.app.afterInstall(context.Background(), f.engine, model.Result{Succeeded: []string{"demo"}}, nil)
	oldDemo := filepath.Join(f.prefix, "Cellar", "demo", "1")
	if _, err := os.Stat(oldDemo); !os.IsNotExist(err) {
		t.Fatalf("successful install did not clean old keg: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.prefix, "Cellar", "other", "1")); err != nil {
		t.Fatalf("install cleaned an unrelated formula: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(f.prefix, "bin", "demo")); err != nil || string(data) != "2" {
		t.Fatalf("cleanup affected installed executable: %q, %v", data, err)
	}
	if text := strings.Join(notes, "\n"); !strings.Contains(text, "Removing: "+oldDemo) || !strings.Contains(text, "GOBREW_NO_INSTALL_CLEANUP") {
		t.Fatalf("cleanup was not reported: %s", text)
	}
}

func TestAfterInstallCleanupRespectsOptOutAndExcludedFormulae(t *testing.T) {
	for _, key := range []string{"GOBREW_NO_INSTALL_CLEANUP", "GOBREW_NO_CLEANUP_FORMULAE"} {
		t.Run(key, func(t *testing.T) {
			f := newCleanupFixture(t)
			f.install(t, "demo", "1")
			result := f.install(t, "demo", "2")
			value := "1"
			if key == "GOBREW_NO_CLEANUP_FORMULAE" {
				value = " other, demo "
			}
			t.Setenv(key, value)
			notes := f.app.afterInstall(context.Background(), f.engine, result, nil)
			if _, err := os.Stat(filepath.Join(f.prefix, "Cellar", "demo", "1")); err != nil {
				t.Fatalf("protected old keg removed: %v", err)
			}
			if len(notes) != 0 {
				t.Fatalf("disabled cleanup reported work: %v", notes)
			}
		})
	}
}

func TestAfterInstallStartsCleanupPeriodBeforeFullCleanup(t *testing.T) {
	f := newCleanupFixture(t)
	f.install(t, "other", "1")
	f.install(t, "other", "2")
	result := f.install(t, "demo", "1")
	oldOther := filepath.Join(f.prefix, "Cellar", "other", "1")
	notes := f.app.afterInstall(context.Background(), f.engine, result, nil)
	if _, err := os.Stat(oldOther); err != nil {
		t.Fatalf("first install ran periodic full cleanup: %v", err)
	}
	marker := filepath.Join(f.cache, ".cleaned")
	if _, err := os.Stat(marker); err != nil || len(notes) != 0 {
		t.Fatalf("missing marker did not start a quiet period: %v, %v", err, notes)
	}
	stale := time.Now().Add(-31 * 24 * time.Hour)
	if err := os.Chtimes(marker, stale, stale); err != nil {
		t.Fatal(err)
	}
	result = f.install(t, "demo", "2")
	notes = f.app.afterInstall(context.Background(), f.engine, result, nil)
	if _, err := os.Stat(oldOther); !os.IsNotExist(err) {
		t.Fatalf("due full cleanup retained unreferenced keg: %v", err)
	}
	if !strings.Contains(strings.Join(notes, "\n"), "last 30 days") {
		t.Fatalf("periodic cleanup was not reported: %v", notes)
	}
	if info, err := os.Stat(marker); err != nil || !info.ModTime().After(stale) {
		t.Fatalf("successful full cleanup did not refresh period: %v", err)
	}
}
