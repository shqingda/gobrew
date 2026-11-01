package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/shqingda/gobrew/internal/engine"
)

type offline struct{}

func (offline) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("network disabled in test")
}

// lockedPrefix returns a missing prefix whose parent the test user cannot
// write, standing in for /opt/gobrew under root-owned /opt.
func lockedPrefix(t *testing.T) (string, func()) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root can write any parent")
	}
	parent := filepath.Join(t.TempDir(), "opt")
	if err := os.Mkdir(parent, 0555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0755) })
	prefix := filepath.Join(parent, "gobrew")
	helper := func() {
		if err := os.Chmod(parent, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(prefix, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(parent, 0555); err != nil {
			t.Fatal(err)
		}
	}
	return prefix, helper
}

type fakeApp struct {
	*application
	calls [][]string
}

func newFakeApp(t *testing.T, prefix string, terminal bool, helper func() error) *fakeApp {
	f := &fakeApp{}
	cache := filepath.Join(t.TempDir(), "cache")
	f.application = &application{
		stderr:       &bytes.Buffer{},
		systemPrefix: prefix,
		terminal:     func() bool { return terminal },
		engine:       engine.New(engine.Config{Prefix: prefix, CacheDir: cache, HTTP: &http.Client{Transport: offline{}}, Downloads: 1, Jobs: 1, BlobBudget: 1, TreeBudget: 1}),
	}
	f.config.Prefix, f.config.CacheDir = prefix, cache
	f.elevate = func(_ context.Context, _ io.Writer, command []string) error {
		if !filepath.IsAbs(command[0]) {
			t.Errorf("helper executable %q is not absolute", command[0])
		}
		f.calls = append(f.calls, command)
		return helper()
	}
	return f
}

func (f *fakeApp) run(t *testing.T, args ...string) (int, string) {
	t.Helper()
	var stdout bytes.Buffer
	code := f.application.run(context.Background(), args, &stdout, BuildInfo{})
	return code, stdout.String() + f.stderr.(*bytes.Buffer).String()
}

func stateExists(prefix string) bool {
	_, err := os.Stat(filepath.Join(prefix, ".gobrew", "state.db"))
	return err == nil
}

func TestFirstInstallInitializesWithOneElevation(t *testing.T) {
	prefix, helper := lockedPrefix(t)
	f := newFakeApp(t, prefix, true, func() error { helper(); return nil })
	// Planning fails offline, but only after setup: nothing else needs privilege.
	if code, out := f.run(t, "install", "uv"); code == 0 || !strings.Contains(out, "network disabled") {
		t.Fatalf("install did not reach planning after setup: exit = %d: %s", code, out)
	}
	want := [][]string{{"/bin/sh", "-c", createOwnedPrefix, "sh", strconv.Itoa(os.Getuid()), strconv.Itoa(os.Getgid()), prefix}}
	if !slices.EqualFunc(f.calls, want, slices.Equal) {
		t.Fatalf("elevation calls = %v, want %v", f.calls, want)
	}
	if !stateExists(prefix) {
		t.Fatal("first install did not finish initialization as the user")
	}
	if code, out := f.run(t, "install", "uv"); code == 0 || len(f.calls) != 1 {
		t.Fatalf("second install elevated again or succeeded offline: exit %d, calls %d: %s", code, len(f.calls), out)
	}
}

func TestElevationIsNeverAttemptedWithoutAnInteractiveTerminal(t *testing.T) {
	for _, tc := range []struct {
		name     string
		terminal bool
		args     []string
	}{
		{"no-terminal", false, []string{"install", "uv"}},
		{"json", true, []string{"install", "uv", "--json"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prefix, helper := lockedPrefix(t)
			f := newFakeApp(t, prefix, tc.terminal, func() error { helper(); return nil })
			code, out := f.run(t, tc.args...)
			if code != 1 || len(f.calls) != 0 {
				t.Fatalf("exit = %d, calls = %d: %s", code, len(f.calls), out)
			}
			if !strings.Contains(out, "run once in a terminal: sudo /bin/mkdir -p '/") {
				t.Fatalf("missing absolute manual command: %s", out)
			}
			if slices.Contains(tc.args, "--json") && !strings.Contains(out, `"code":"privilege_required"`) {
				t.Fatalf("missing JSON error code: %s", out)
			}
		})
	}
}

func TestDeclinedOrForeignElevationLeavesNoState(t *testing.T) {
	prefix, _ := lockedPrefix(t)
	f := newFakeApp(t, prefix, true, func() error { return errors.New("exit status 1") })
	if code, out := f.run(t, "install", "uv"); code != 1 || !strings.Contains(out, "did not complete") {
		t.Fatalf("exit = %d: %s", code, out)
	}
	if _, err := os.Lstat(prefix); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("declined authorization created %s", prefix)
	}
	// Only the fixed system prefix may be elevated; any other path just fails.
	f = newFakeApp(t, prefix, true, func() error { return nil })
	f.systemPrefix = "/opt/gobrew"
	if code, out := f.run(t, "install", "uv"); code != 1 || len(f.calls) != 0 {
		t.Fatalf("custom prefix elevated: exit = %d, calls = %d: %s", code, len(f.calls), out)
	}
}

func TestReadOnlyCommandsDoNotInitialize(t *testing.T) {
	for _, args := range [][]string{{"install", "--dry-run", "uv"}, {"list"}, {"doctor"}, {"upgrade"}, {"uninstall", "uv"}} {
		prefix := filepath.Join(t.TempDir(), "prefix")
		f := newFakeApp(t, prefix, true, func() error { t.Error("elevated"); return nil })
		f.run(t, args...)
		if _, err := os.Lstat(prefix); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%v created the prefix", args)
		}
	}
}

func TestPrefixEnvironmentMatchesShellenv(t *testing.T) {
	prefix := filepath.Join(t.TempDir(), "env prefix")
	t.Setenv("GOBREW_PREFIX", prefix)
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"shellenv", "sh"}, &stdout, &stderr, BuildInfo{}); code != 0 {
		t.Fatalf("exit = %d: %s", code, &stderr)
	}
	if !strings.Contains(stdout.String(), "export GOBREW_PREFIX='"+prefix+"'") {
		t.Fatalf("GOBREW_PREFIX was ignored: %s", &stdout)
	}
}

// The root helper is plain sh; run it unprivileged against a temporary tree.
func TestCreateOwnedPrefixCreatesOnlyAMissingDirectory(t *testing.T) {
	uid, gid := strconv.Itoa(os.Getuid()), strconv.Itoa(os.Getgid())
	run := func(prefix string) error {
		return exec.Command("/bin/sh", "-c", createOwnedPrefix, "sh", uid, gid, prefix).Run()
	}
	base := t.TempDir()
	prefix := filepath.Join(base, "opt", "gobrew")
	if err := run(prefix); err != nil {
		t.Fatalf("missing parent and prefix were not created: %v", err)
	}
	if info, err := os.Lstat(prefix); err != nil || !info.IsDir() {
		t.Fatalf("prefix missing: %v", err)
	}
	if err := run(prefix); err == nil {
		t.Fatal("helper accepted an existing prefix")
	}
	if err := os.Symlink(filepath.Join(base, "opt"), filepath.Join(base, "link")); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(base, "link", "other")
	if err := run(linked); err == nil {
		t.Fatal("helper followed a symlinked parent")
	}
	if _, err := os.Lstat(filepath.Join(base, "opt", "other")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("helper created a directory through a symlinked parent")
	}
}

func TestVersionInstallRequiresAVersion(t *testing.T) {
	prefix := filepath.Join(t.TempDir(), "prefix")
	f := newFakeApp(t, prefix, true, func() error { t.Error("elevated"); return nil })
	if code, out := f.run(t, "version-install", "uv"); code != 2 || !strings.Contains(out, "version-install uv <version>") {
		t.Fatalf("exit = %d: %s", code, out)
	}
	if _, err := os.Lstat(prefix); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("usage error initialized the prefix")
	}
}
