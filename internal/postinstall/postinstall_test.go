package postinstall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/shqingda/gobrew/internal/model"
)

func steps(t *testing.T, raw string) []json.RawMessage {
	t.Helper()
	var out []json.RawMessage
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestValidationFailsBeforeMutation(t *testing.T) {
	prefix := t.TempDir()
	keg := filepath.Join(prefix, "Cellar", "demo", "1.0")
	if err := os.MkdirAll(keg, 0755); err != nil {
		t.Fatal(err)
	}
	for _, unsupported := range []string{`{"type":"arbitrary_script"}`, `{"type":"mkdir_p","path":null}`, `{"type":"mkdir_p","path":{"path":"{{HOMEBREW_PREFIX}}/created"},"new_option":true}`, `{"type":"run","command":{"path":"true"},"sudo":true}`, `{"type":"write","path":{"path":"{{new_token}}"},"content":"x"}`, `{"type":"set_permissions","paths":[],"permissions":"4755"}`} {
		input := steps(t, `[{"type":"mkdir_p","path":{"path":"{{HOMEBREW_PREFIX}}/created"}},`+unsupported+`]`)
		if err := Run(context.Background(), input, Env{Prefix: prefix, Keg: keg, Name: "demo", Version: "1.0"}); !errors.Is(err, model.ErrUnsupported) {
			t.Fatalf("accepted unsupported step: %s (%v)", unsupported, err)
		}
		if _, err := os.Stat(filepath.Join(prefix, "created")); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("validation mutated prefix")
		}
	}
}

func TestStepsGuardsPathsAndGlobs(t *testing.T) {
	prefix := t.TempDir()
	keg := filepath.Join(prefix, "Cellar", "demo", "1.2")
	if err := os.MkdirAll(filepath.Join(keg, "share", "man"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"npm.1", "npx.1", "unrelated.1"} {
		if err := os.WriteFile(filepath.Join(keg, "share", "man", name), []byte(name), 0644); err != nil {
			t.Fatal(err)
		}
	}
	input := steps(t, `[
	 {"type":"mkdir_p","path":{"path":"{{pkgetc}}"}},
	 {"type":"write","path":{"path":"{{pkgetc}}/config"},"content":"version={{version.major_minor}}"},
	 {"type":"write","path":{"path":"{{pkgetc}}/config"},"content":"must not overwrite"},
	 {"type":"mkdir_p","path":{"path":"{{HOMEBREW_PREFIX}}/share/man/man1"}},
	 {"type":"symlink","source":{"path":"{{share}}/man/{npm,npx}*"},"target":{"path":"{{HOMEBREW_PREFIX}}/share/man/man1"},"force":true,"source_glob":true},
	 {"type":"touch","path":{"path":"{{pkgetc}}/skipped"},"guards":[{"condition":"unless_exists","path":"{{pkgetc}}/config","id":"one"}]},
	 {"type":"set_permissions","paths":[{"path":"{{pkgetc}}/config"}],"permissions":"0600","non_recursive":true}
	]`)
	if err := Run(context.Background(), input, Env{Prefix: prefix, Keg: keg, Name: "demo", Version: "1.2"}); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(prefix, "etc/demo/config"))
	if err != nil || string(content) != "version=1.2" {
		t.Fatalf("config: %q %v", content, err)
	}
	if _, err := os.Lstat(filepath.Join(prefix, "share/man/man1/npm.1")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(prefix, "share/man/man1/unrelated.1")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("glob overmatched")
	}
	if _, err := os.Stat(filepath.Join(prefix, "etc/demo/skipped")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("guard ignored")
	}
}

func TestMoveSymlinkPreservesReferencedConfiguration(t *testing.T) {
	prefix := t.TempDir()
	keg := filepath.Join(prefix, "Cellar", "demo", "1")
	for _, path := range []string{keg, filepath.Join(prefix, "etc")} {
		if err := os.MkdirAll(path, 0755); err != nil {
			t.Fatal(err)
		}
	}
	config := filepath.Join(prefix, "etc", "user.conf")
	if err := os.WriteFile(config, []byte("user configuration"), 0644); err != nil {
		t.Fatal(err)
	}
	source, destination := filepath.Join(keg, "config-link"), filepath.Join(keg, "moved-link")
	if err := os.Symlink(config, source); err != nil {
		t.Fatal(err)
	}
	input := steps(t, `[{"type":"move","source":{"base":"prefix","path":"config-link"},"target":{"base":"prefix","path":"moved-link"}}]`)
	if err := Run(context.Background(), input, Env{Prefix: prefix, Keg: keg, Name: "demo", Version: "1"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(config)
	if err != nil || string(data) != "user configuration" {
		t.Fatalf("move changed referenced configuration: %q %v", data, err)
	}
	if got, err := os.Readlink(destination); err != nil || got != config {
		t.Fatalf("destination is not the original symlink: %q %v", got, err)
	}
	if _, err := os.Lstat(source); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source link survived move: %v", err)
	}
}

func TestMissingTransferSourcePreservesExistingConfiguration(t *testing.T) {
	for _, kind := range []string{"move", "copy"} {
		t.Run(kind, func(t *testing.T) {
			prefix := t.TempDir()
			keg := filepath.Join(prefix, "Cellar", "demo", "1")
			for _, path := range []string{keg, filepath.Join(prefix, "etc")} {
				if err := os.MkdirAll(path, 0755); err != nil {
					t.Fatal(err)
				}
			}
			config := filepath.Join(prefix, "etc", "user.conf")
			if err := os.WriteFile(config, []byte("keep me"), 0644); err != nil {
				t.Fatal(err)
			}
			input := steps(t, fmt.Sprintf(`[{"type":%q,"source":{"base":"prefix","path":"missing"},"target":{"base":"etc","path":"user.conf"}}]`, kind))
			if err := Run(context.Background(), input, Env{Prefix: prefix, Keg: keg, Name: "demo", Version: "1"}); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("missing source error: %v", err)
			}
			if data, err := os.ReadFile(config); err != nil || string(data) != "keep me" {
				t.Fatalf("failed transfer changed configuration: %q %v", data, err)
			}
		})
	}
}

func TestPostInstallCannotEscapeThroughSymlinkAndObservesCancellation(t *testing.T) {
	prefix, outside := t.TempDir(), t.TempDir()
	keg := filepath.Join(prefix, "Cellar", "demo", "1")
	if err := os.MkdirAll(keg, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(prefix, "etc")); err != nil {
		t.Fatal(err)
	}
	env := Env{Prefix: prefix, Keg: keg, Name: "demo", Version: "1"}
	if err := Run(context.Background(), steps(t, `[{"type":"write","path":{"path":"{{etc}}/escape"},"content":"bad"}]`), env); err == nil {
		t.Fatal("symlink escape accepted")
	}
	if _, err := os.Stat(filepath.Join(outside, "escape")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("wrote outside prefix")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := Run(ctx, steps(t, `[{"type":"run","command":{"path":"/bin/sleep"},"args":["5"],"allow_failure":true}]`), env); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancellation lost: %v", err)
	}
}

func TestPythonVenvPermissionsUseFrameworkVersionAndRecursiveGlob(t *testing.T) {
	prefix := t.TempDir()
	keg := filepath.Join(prefix, "Cellar", "python@3.15", "3.15.1_1")
	scripts := filepath.Join(keg, "Frameworks", "Python.framework", "Versions", "3.15", "lib", "python3.15", "venv", "scripts")
	for _, name := range []string{"common/activate", "posix/nested/activate.csh"} {
		path := filepath.Join(scripts, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("activation script"), 0444); err != nil {
			t.Fatal(err)
		}
	}
	// This is the current official Python step, with the platform guard made
	// explicit for whichever host runs the test. It must reach nested scripts.
	platform := "macos"
	if runtime.GOOS == "linux" {
		platform = "linux"
	}
	input := steps(t, `[{"paths":[{"base":"frameworks","path":"Python.framework/Versions/{{version.major_minor}}/lib/python{{version.major_minor}}/venv/scripts/**/*"}],"permissions":"u+w","non_recursive":true,"guards":[{"condition":"on","value":"`+platform+`","id":"1"}],"type":"set_permissions"}]`)
	if err := Run(context.Background(), input, Env{Prefix: prefix, Keg: keg, Name: "python@3.15", Version: "3.15.1"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"common/activate", "posix/nested/activate.csh"} {
		info, err := os.Stat(filepath.Join(scripts, name))
		if err != nil || info.Mode().Perm() != 0644 {
			t.Fatalf("script %s did not become user-writable: %v %v", name, info, err)
		}
	}
}
