package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shqingda/gobrew/internal/model"
	"github.com/shqingda/gobrew/internal/shellconfig"
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
	note := a.setupPath(env, "")
	data, err := os.ReadFile(rc)
	if err != nil || !strings.Contains(string(data), "'/home/me/.local/bin':'/opt/gobrew/bin':'/opt/gobrew/sbin'\"${PATH+:$PATH}\"") {
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
	if note := a.setupPath(env, ""); note != "" {
		t.Fatalf("configured shell got note %q", note)
	}
	if _, err := os.Stat(rc); !os.IsNotExist(err) {
		t.Fatal("configured shell rewrote the startup file")
	}
	t.Setenv("PATH", "/usr/bin")
	t.Setenv("GOBREW_NO_MODIFY_PATH", "1")
	// gobrew is not on PATH here, so the manual line must name it by path.
	exe, _ := os.Executable()
	if note := a.setupPath(env, ""); !strings.Contains(note, `eval "$(`) || !strings.Contains(note, exe+" shellenv") {
		t.Fatalf("opt-out note = %q, want the absolute %s", note, exe)
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
	if code := Run(context.Background(), []string{"--prefix", prefix, "--cache-dir", t.TempDir(), "doctor", "--json"}, &stdout, &stderr, BuildInfo{}); code != 0 {
		t.Fatalf("exit = %d: %s %s", code, &stdout, &stderr)
	}
	var out struct {
		Data []struct{ Problem, Severity string } `json:"data"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil || len(out.Data) == 0 || !strings.Contains(out.Data[0].Problem, "precedes") {
		t.Fatalf("doctor = %s (%v)", &stdout, err)
	}
	// The environment checks are warnings too: a missing catalog and an
	// unmanaged test executable leave the installation healthy.
	for _, f := range out.Data {
		if f.Severity != "warning" {
			t.Fatalf("doctor = %s", &stdout)
		}
	}
	if !strings.Contains(stdout.String(), "no formula catalog") {
		t.Fatalf("doctor did not report the missing catalog: %s", &stdout)
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

func TestCompletionsAreWrittenForBothNamesAndRefreshOnlyExistingBlocks(t *testing.T) {
	bin := t.TempDir()
	if err := os.Mkdir(filepath.Join(bin, ".gobrew"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := writeCompletions(bin); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"_gobrew": "#compdef gobrew gob\n", "gobrew.bash": "complete -o default -F __start_gob gob", "gobrew.fish": "complete -c gob -e"} {
		data, err := os.ReadFile(filepath.Join(completionsDir(bin), name))
		if err != nil || !strings.Contains(string(data), want) {
			t.Fatalf("%s lacks %q: %v", name, want, err)
		}
	}
	if err := writeCompletions(bin); err != nil {
		t.Fatal("rewrite:", err)
	}
	rc := filepath.Join(t.TempDir(), ".zshrc")
	profile := shellconfig.Profile{Shell: "zsh", Path: rc}
	env := shellconfig.Env{Paths: []string{bin}, Completions: completionsDir(bin)}
	if changed, err := profile.Refresh(env); err != nil || changed {
		t.Fatalf("refresh added a block: %v, %v", changed, err)
	}
	if err := os.WriteFile(rc, []byte("# mine\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if changed, err := profile.Refresh(env); err != nil || changed {
		t.Fatalf("refresh added a block to a foreign file: %v, %v", changed, err)
	}
	if _, err := profile.Ensure(shellconfig.Env{Paths: []string{bin}}); err != nil {
		t.Fatal(err)
	}
	if changed, err := profile.Refresh(env); err != nil || !changed {
		t.Fatalf("refresh did not update the block: %v, %v", changed, err)
	}
	if data, _ := os.ReadFile(rc); !strings.HasPrefix(string(data), "# mine\n") || !strings.Contains(string(data), "compdef _gobrew gobrew gob") {
		t.Fatalf("startup file = %q", data)
	}
}
