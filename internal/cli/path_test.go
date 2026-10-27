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
	lines := strings.Join(duplicateWarnings("/opt/gobrew", dups), "\n")
	if !strings.Contains(lines, "New shells run /opt/gobrew/bin/uvx first") || !strings.Contains(lines, "brew uninstall uv") {
		t.Fatalf("warning = %s", lines)
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
