package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shqingda/gobrew/internal/model"
	"github.com/shqingda/gobrew/internal/state"
)

func TestPathSetupTouchesOnlyTheDefaultPrefixAndOnlyWhenMissing(t *testing.T) {
	rc := filepath.Join(os.Getenv("HOME"), ".zshrc")
	t.Cleanup(func() { _ = os.Remove(rc) })
	a := &application{systemPrefix: "/opt/gobrew"}
	if env := a.pathEnv("", "/tmp/dev-prefix"); len(env.Paths) != 0 {
		t.Fatalf("custom prefix reached the startup file: %+v", env)
	}
	env := a.pathEnv("/home/me/.local/bin", "/opt/gobrew")
	t.Setenv("PATH", "/usr/bin:/bin")
	note := a.setupPath(env)
	data, err := os.ReadFile(rc)
	if err != nil || !strings.Contains(string(data), "'/home/me/.local/bin':'/opt/gobrew/bin':'/opt/gobrew/sbin':\"$PATH\"") {
		t.Fatalf("startup file = %q, %v", data, err)
	}
	if !strings.Contains(note, "Added gobrew to PATH in ~/.zshrc") || !strings.Contains(note, "exec zsh") {
		t.Fatalf("note = %q", note)
	}
	// Once the running shell has the directories, nothing is read or written.
	if err := os.Remove(rc); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", strings.Join(env.Paths, ":")+":/usr/bin")
	if note := a.setupPath(env); note != "" {
		t.Fatalf("configured shell got note %q", note)
	}
	if _, err := os.Stat(rc); !os.IsNotExist(err) {
		t.Fatal("configured shell rewrote the startup file")
	}
	t.Setenv("PATH", "/usr/bin")
	t.Setenv("GOBREW_NO_MODIFY_PATH", "1")
	if note := a.setupPath(env); !strings.Contains(note, "shellenv") {
		t.Fatalf("opt-out note = %q", note)
	}
	if _, err := os.Stat(rc); !os.IsNotExist(err) {
		t.Fatal("opt-out still wrote the startup file")
	}
}

func homebrewWith(t *testing.T, command, formula string) {
	t.Helper()
	brew := os.Getenv("HOMEBREW_PREFIX")
	t.Cleanup(func() { _ = os.RemoveAll(brew) })
	if err := os.MkdirAll(filepath.Join(brew, "bin"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../Cellar/"+formula+"/1.0/bin/"+command, filepath.Join(brew, "bin", command)); err != nil {
		t.Fatal(err)
	}
}

func TestHomebrewDuplicatesNameTheOwningFormula(t *testing.T) {
	homebrewWith(t, "uvx", "uv")
	records := map[string]model.Record{"uv": {Links: map[string]string{"bin/uv": "x", "bin/uvx": "x", "share/man/man1/uv.1": "x"}}}
	dups := homebrewDuplicates(records, []string{"uv"})
	if len(dups) != 1 || dups[0] != (duplicate{"uv", "uvx", "uv"}) {
		t.Fatalf("duplicates = %+v", dups)
	}
}

func TestDoctorPathWarningsKeepAHealthyExitStatus(t *testing.T) {
	homebrewWith(t, "uv", "uv")
	prefix := filepath.Join(t.TempDir(), "prefix")
	if err := os.MkdirAll(filepath.Join(prefix, "bin"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := (state.Store{Prefix: prefix}).Initialize(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Join(os.Getenv("HOMEBREW_PREFIX"), "bin")+":"+filepath.Join(prefix, "bin"))
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"--prefix", prefix, "doctor", "--json"}, &stdout, &stderr, BuildInfo{}); code != 0 {
		t.Fatalf("exit = %d: %s %s", code, &stdout, &stderr)
	}
	var out struct {
		Data []struct{ Problem, Severity string } `json:"data"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil || len(out.Data) != 1 || out.Data[0].Severity != "warning" || !strings.Contains(out.Data[0].Problem, "precedes") {
		t.Fatalf("doctor = %s (%v)", &stdout, err)
	}
}

func TestProgressRendersHomebrewStyle(t *testing.T) {
	var plain bytes.Buffer
	p := newProgress(&plain)
	for _, e := range []model.Event{{Phase: "download", Package: "uv"}, {Phase: "download", Package: "uv", Completed: 5, Total: 10}, {Phase: "extract", Package: "uv"}, {Phase: "relocate", Package: "uv"}, {Phase: "postinstall", Package: "uv"}} {
		if err := p.event(e); err != nil {
			t.Fatal(err)
		}
	}
	if want := "==> Downloading uv\n==> Pouring uv\n==> Running post-install for uv\n"; plain.String() != want {
		t.Fatalf("non-terminal output = %q", plain.String())
	}
	var term bytes.Buffer
	p = newProgress(&term)
	p.tty, p.width = true, 40
	_ = p.event(model.Event{Phase: "download", Package: "uv"})
	p.drawn = time.Time{} // skip the redraw throttle
	_ = p.event(model.Event{Phase: "download", Package: "uv", Completed: 512, Total: 1024})
	_ = p.event(model.Event{Phase: "extract", Package: "uv"})
	out := term.String()
	if !strings.Contains(out, "\r\033[K###") || !strings.Contains(out, " 50.0% 512B/1.0KB") || !strings.HasSuffix(out, "==> Pouring uv\n") {
		t.Fatalf("terminal output = %q", out)
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

func TestHomebrewCommandAliasesResolve(t *testing.T) {
	root := (&application{}).root()
	for canonical, aliases := range brewAliases {
		for _, alias := range aliases {
			cmd, _, err := root.Find([]string{alias})
			if err != nil || cmd.Name() != canonical {
				t.Fatalf("%s -> %v, %v; want %s", alias, cmd, err, canonical)
			}
		}
	}
	var stdout, stderr bytes.Buffer
	// -S reaches search, which then fails offline on the empty temporary cache.
	Run(context.Background(), []string{"-S", "--json", "--cache-dir", t.TempDir(), "--prefix", t.TempDir(), "x"}, &stdout, &stderr, BuildInfo{})
	if !strings.Contains(stdout.String(), `"command":"search"`) {
		t.Fatalf("-S did not run search: %s", &stdout)
	}
}

func TestColumnsMatchHomebrewLayout(t *testing.T) {
	items := []string{"aa", "bbbb", "c", "dd", "e"}
	widths := []int{2, 4, 1, 2, 1}
	// Homebrew: 3 columns fit, 2 rows; column width (20+2)/3-2 = 5, then a 2-space gap.
	want := "aa     c      e\nbbbb   dd\n"
	if got := columns(items, widths, 20); got != want {
		t.Fatalf("columns = %q, want %q", got, want)
	}
	if got := columns(items, widths, 5); got != "aa\nbbbb\nc\ndd\ne\n" {
		t.Fatalf("narrow columns = %q", got)
	}
}

func TestShadowCaveatMatchesHomebrew(t *testing.T) {
	prefix := t.TempDir()
	keg := filepath.Join(prefix, "Cellar", "uv", "1")
	other := t.TempDir()
	for _, path := range []string{filepath.Join(keg, "bin", "uv"), filepath.Join(keg, "bin", "uvx"), filepath.Join(other, "uv")} {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(prefix, "bin"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(keg, "bin", "uvx"), filepath.Join(prefix, "bin", "uvx")); err != nil {
		t.Fatal(err)
	}
	r := model.Record{Formula: model.Formula{Name: "uv"}, Keg: keg}
	t.Setenv("PATH", other+":"+filepath.Join(prefix, "bin"))
	text := shadowText(prefix, r)
	want := "The following uv executables are shadowed by commands in " + other + ":\nuv\nRunning these by name will not invoke the version provided by gobrew"
	if !strings.HasPrefix(text, want) || strings.Contains(text, "uvx") || !strings.Contains(text, "GOBREW_NO_PATH_SHADOW_CHECK") {
		t.Fatalf("shadow text = %q", text)
	}
	t.Setenv("GOBREW_NO_PATH_SHADOW_CHECK", "1")
	if text := shadowText(prefix, r); text != "" {
		t.Fatalf("disabled check printed %q", text)
	}
}
