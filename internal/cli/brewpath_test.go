package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/shqingda/gobrew/internal/engine"
)

func TestPathCommandKeepsPrefixOption(t *testing.T) {
	root := (&application{}).root()
	for _, tc := range []struct{ args, want []string }{
		{[]string{"--prefix"}, []string{"__prefix"}},
		{[]string{"--prefix", "jq", "--installed"}, []string{"__prefix", "jq", "--installed"}},
		{[]string{"--prefix", "/opt/x", "install", "jq"}, []string{"--prefix", "/opt/x", "install", "jq"}},
		{[]string{"--prefix", "relative", "ls"}, []string{"--prefix", "relative", "ls"}},
		{[]string{"--prefix", "jq", "lz4"}, []string{"__prefix", "jq", "lz4"}},
		{[]string{"--prefix", "/opt/x", "--prefix", "jq"}, []string{"--prefix", "/opt/x", "__prefix", "jq"}},
		{[]string{"--prefix=/opt/x", "--cellar"}, []string{"--prefix=/opt/x", "__cellar"}},
		{[]string{"--cache-dir", "/c", "--json", "-q", "--cache", "jq"}, []string{"--cache-dir", "/c", "--json", "-q", "__cache", "jq"}},
		{[]string{"--downloads", "2", "list"}, []string{"--downloads", "2", "list"}},
		{[]string{"install", "--cache"}, []string{"install", "--cache"}},
		{[]string{"--", "--cellar"}, []string{"--", "--cellar"}},
	} {
		if got := pathCommand(root, tc.args); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("pathCommand(%q) = %q, want %q", tc.args, got, tc.want)
		}
	}
}

func TestPathCommandsFollowHomebrew(t *testing.T) {
	t.Setenv("GOBREW_NO_AUTO_UPDATE", "1")
	base, _, _ := updateApp(t)
	prefix, cache := base.config.Prefix, base.config.CacheDir
	run := func(args ...string) (int, string, string) {
		t.Helper()
		var stdout, stderr bytes.Buffer
		a := &application{config: base.config, engine: engine.New(base.config), stderr: &stderr, systemPrefix: engine.SystemPrefix}
		code := a.run(context.Background(), args, &stdout, BuildInfo{})
		return code, stdout.String(), stderr.String()
	}
	opt := filepath.Join(prefix, "opt", "demo")
	blob := filepath.Join(cache, "blobs", strings.Repeat("b", 64))
	if err := os.Symlink(filepath.Join(prefix, "Cellar", "demo", "1"), opt); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args           []string
		code           int
		stdout, stderr string
	}{
		{[]string{"--prefix"}, 0, prefix + "\n", ""},
		{[]string{"--prefix", "demo"}, 0, opt + "\n", ""},
		{[]string{"--prefix", "--installed", "demo"}, 0, opt + "\n", ""},
		{[]string{"--prefix", "demo", "nothere"}, 1, "", "Error: No available formula with the name \"nothere\".\n"},
		{[]string{"--prefix", "--installed"}, 2, "", "Error: `--installed` requires a formula argument.\nRun 'gobrew --prefix --help' for usage.\n"},
		{[]string{"--prefix", "--unbrewed", "demo"}, 2, "", "Error: `--unbrewed` does not take a formula argument.\nRun 'gobrew --prefix --help' for usage.\n"},
		{[]string{"--cellar"}, 0, filepath.Join(prefix, "Cellar") + "\n", ""},
		{[]string{"--cellar", "homebrew/core/demo"}, 0, filepath.Join(prefix, "Cellar", "demo") + "\n", ""},
		{[]string{"--cache"}, 0, cache + "\n", ""},
		{[]string{"--cache", "--formula", "demo"}, 0, blob + "\n", ""},
		// A newer macOS falls back to the sequoia bottle; an older one has none.
		{[]string{"--cache", "--bottle-tag=arm64_tahoe", "demo"}, 0, blob + "\n", ""},
		{[]string{"--cache", "--bottle-tag", "arm64_sonoma", "demo"}, 0, "", "Warning: Bottle for tag :arm64_sonoma is unavailable.\n"},
		{[]string{"--cache", "--bottle-tag=linux", "demo"}, 0, "", "Warning: Bottle for tag :x86_64_linux is unavailable.\n"},
		{[]string{"--json", "--prefix"}, 0, `{"schema_version":1,"command":"--prefix","data":["` + prefix + `"]}` + "\n", ""},
	} {
		code, stdout, stderr := run(tc.args...)
		if code != tc.code || stdout != tc.stdout || stderr != tc.stderr {
			t.Errorf("%q: exit=%d stdout=%q stderr=%q, want %d %q %q", tc.args, code, stdout, stderr, tc.code, tc.stdout, tc.stderr)
		}
	}

	// brew prints the installed formulae before naming the missing ones.
	if err := os.Remove(opt); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(opt, 0755); err != nil { // not a link: not opt-linked
		t.Fatal(err)
	}
	code, stdout, stderr := run("--prefix", "--installed", "demo")
	if code != 1 || stdout != opt+"\n" || stderr != "Error: The following formulae are not installed:\ndemo\n" {
		t.Fatalf("missing opt link: exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestUnbrewedListsUnownedRegularFiles(t *testing.T) {
	base, _, _ := updateApp(t)
	prefix := base.config.Prefix
	for _, file := range []string{"bin/stray", "share/doc/notes.txt", "bin/.DS_Store", "lib/node_modules/npm/x", "lib/python3.13/site-packages/y", "etc/conf", "var/log/z", "a/.keepme"} {
		path := filepath.Join(prefix, file)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("../Cellar/demo/1/bin/demo", filepath.Join(prefix, "bin", "demo")); err != nil {
		t.Fatal(err)
	}
	got, err := base.engine.Unbrewed()
	if want := []string{"bin/stray", "share/doc/notes.txt"}; err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("unbrewed = %q, %v; want %q", got, err, want)
	}
}
