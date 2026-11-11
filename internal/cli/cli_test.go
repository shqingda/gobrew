package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestOfflineCommandsDoNotCreateState(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"help", []string{"--help"}, "Usage:"},
		{"help-json", []string{"--help", "--json"}, `"schema_version":1`},
		{"version", []string{"-v", "--json"}, `"version":"test"`},
		{"version-flag", []string{"--version", "--json"}, `"command":"version"`},
		{"list", []string{"list", "--json"}, `"data":[]`},
		{"shellenv", []string{"shellenv"}, "GOBREW_PREFIX="},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			prefix, cache := filepath.Join(dir, "prefix"), filepath.Join(dir, "cache")
			args := append([]string{"--prefix", prefix, "--cache-dir", cache}, tc.args...)
			var stdout, stderr bytes.Buffer
			if code := Run(context.Background(), args, &stdout, &stderr, BuildInfo{Version: "test"}); code != 0 {
				t.Fatalf("exit = %d, stderr = %s, stdout = %s", code, &stderr, &stdout)
			}
			if !strings.Contains(stdout.String(), tc.want) {
				t.Fatalf("stdout = %q, want %q", stdout.String(), tc.want)
			}
			if requestedJSON(tc.args) && !json.Valid(stdout.Bytes()) {
				t.Fatalf("expected one JSON document: %s", &stdout)
			}
			for _, path := range []string{prefix, cache} {
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("offline command created %s: %v", path, err)
				}
			}
		})
	}
}

func TestJSONUsageErrorsHaveOneDocument(t *testing.T) {
	for _, args := range [][]string{
		{"install", "--json"},
		{"--json", "--unknown"},
		{"unknown", "--json"},
		{"help", "unknown", "--json"},
		{"self", "unknown", "--json"},
		{"self", "install", "extra", "--json"},
		{"self", "install", "--bin-dir", "relative", "--json"},
		// Commands Homebrew does not have are not gobrew commands, except self.
		{"init", "--json"},
		{"import-homebrew", "--json"},
		{"completion", "zsh", "--json"},
		{"--prefix", "relative", "list", "--json"},
	} {
		var stdout, stderr bytes.Buffer
		if code := Run(context.Background(), args, &stdout, &stderr, BuildInfo{}); code != 2 {
			t.Fatalf("%v: exit = %d, stdout = %s, stderr = %s", args, code, &stdout, &stderr)
		}
		var result struct {
			SchemaVersion int `json:"schema_version"`
			Error         struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
			t.Fatalf("%v: invalid JSON: %v: %s", args, err, &stdout)
		}
		if result.SchemaVersion != 1 || result.Error.Code != "usage" {
			t.Fatalf("%v: wrong error envelope: %s", args, &stdout)
		}
		if stderr.Len() != 0 {
			t.Fatalf("%v: JSON error also wrote stderr: %s", args, &stderr)
		}
	}
}

func TestSelfRefusesUnmanagedExecutables(t *testing.T) {
	for _, action := range []string{"update", "uninstall"} {
		var stdout, stderr bytes.Buffer
		if code := Run(context.Background(), []string{"self", action, "--json"}, &stdout, &stderr, BuildInfo{}); code != 3 {
			t.Fatalf("%s: exit = %d, stdout = %s, stderr = %s", action, code, &stdout, &stderr)
		}
		if !strings.Contains(stdout.String(), `"command":"self `+action+`"`) || !strings.Contains(stdout.String(), "gobrew self install") {
			t.Fatalf("%s: wrong output: %s", action, &stdout)
		}
	}
}

func TestCancellationIsReportedBeforeMutation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stdout, stderr bytes.Buffer
	if code := Run(ctx, []string{"install", "jq", "--json"}, &stdout, &stderr, BuildInfo{}); code != 130 {
		t.Fatalf("exit = %d, stdout = %s, stderr = %s", code, &stdout, &stderr)
	}
	if !strings.Contains(stdout.String(), `"code":"canceled"`) {
		t.Fatalf("wrong cancellation output: %s", &stdout)
	}
}

func TestShellenvQuotesPrefix(t *testing.T) {
	var stdout, stderr bytes.Buffer
	prefix := "/tmp/a'b space/$(printf unexpected)"
	args := []string{"--prefix", prefix, "shellenv"}
	if code := Run(context.Background(), args, &stdout, &stderr, BuildInfo{}); code != 0 {
		t.Fatalf("exit = %d: %s", code, &stderr)
	}
	cmd := exec.Command("sh", "-c", stdout.String()+"\nprintf '%s\\n%s' \"$GOBREW_PREFIX\" \"$PATH\"")
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	got, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("shellenv script failed: %v: %s", err, got)
	}
	want := prefix + "\n" + prefix + "/bin:" + prefix + "/sbin:/usr/bin:/bin"
	if string(got) != want {
		t.Fatalf("shell expanded the prefix: got %q, want %q", got, want)
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

func TestBrewOnlyInputsAreRefusedPlainly(t *testing.T) {
	dir := t.TempDir()
	base := []string{"--prefix", filepath.Join(dir, "prefix"), "--cache-dir", filepath.Join(dir, "cache")}
	for _, tc := range []struct {
		args []string
		code int
		want string
	}{
		{[]string{"tap", "user/repo"}, 3, "Homebrew's `tap` command is not available"},
		{[]string{"install", "--cask", "firefox"}, 3, "casks are not supported"},
		{[]string{"install", "--casks", "firefox"}, 3, "casks are not supported"},
		{[]string{"instll", "jq"}, 2, "Did you mean this?"},
		{[]string{"upgrade", "jq", "--minimum-version", "1", "--min-version", "1"}, 2, "minimum-version"},
	} {
		var stdout, stderr bytes.Buffer
		code := Run(context.Background(), append(slices.Clone(base), tc.args...), &stdout, &stderr, BuildInfo{})
		if code != tc.code || !strings.Contains(stderr.String(), tc.want) {
			t.Errorf("%v: exit = %d, stderr = %s", tc.args, code, &stderr)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "prefix")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refused input touched the prefix: %v", err)
	}
}

func TestShortVFlagIsVersionAloneAndVerboseAfterACommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"--prefix", t.TempDir(), "-v"}, &stdout, &stderr, BuildInfo{Version: "1.2.3"}); code != 0 || !strings.Contains(stdout.String(), "gobrew 1.2.3") {
		t.Fatalf("exit = %d, stdout = %s, stderr = %s", code, &stdout, &stderr)
	}
	stdout.Reset()
	if code := Run(context.Background(), []string{"--prefix", t.TempDir(), "list", "-v"}, &stdout, &stderr, BuildInfo{Version: "1.2.3"}); code != 0 || strings.Contains(stdout.String(), "1.2.3") {
		t.Fatalf("exit = %d, stdout = %s, stderr = %s", code, &stdout, &stderr)
	}
}

func TestHelpEnvironmentListsEveryVariable(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"help", "environment"}, &stdout, &stderr, BuildInfo{}); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, &stderr)
	}
	sources, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	read := regexp.MustCompile(`(?:Getenv|LookupEnv)\("((?:GOBREW|HOMEBREW)_[A-Z_]+)"\)`)
	for _, source := range sources {
		if strings.HasSuffix(source, "_test.go") {
			continue
		}
		data, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range read.FindAllStringSubmatch(string(data), -1) {
			if !strings.Contains(stdout.String(), m[1]) {
				t.Errorf("%s reads %s, which `gobrew help environment` does not list", source, m[1])
			}
		}
	}
}
