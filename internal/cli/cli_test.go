package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
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
		{"version", []string{"version", "--json"}, `"version":"test"`},
		{"version-flag", []string{"--version", "--json"}, `"command":"version"`},
		{"list", []string{"list", "--json"}, `"data":[]`},
		{"completion", []string{"completion", "zsh"}, "#compdef gobrew"},
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
		{"completion", "invalid", "--json"},
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
