package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestInstallTimingsRequireSuccessfulCompleteEnvelope(t *testing.T) {
	for _, test := range []struct {
		name string
		json string
		ok   bool
	}{
		{"valid no-op", `{"schema_version":1,"command":"install","data":{"timings_ms":{"plan":0.3,"prepare":0,"apply":0}}}`, true},
		{"missing stage", `{"schema_version":1,"command":"install","data":{"timings_ms":{"plan":1,"prepare":2}}}`, false},
		{"negative stage", `{"schema_version":1,"command":"install","data":{"timings_ms":{"plan":-1,"prepare":2,"apply":3}}}`, false},
		{"command error", `{"schema_version":1,"command":"install","error":{"message":"failed"},"data":{"timings_ms":{"plan":1,"prepare":2,"apply":3}}}`, false},
		{"repair pending", `{"schema_version":1,"command":"install","data":{"needs_repair":["demo"],"timings_ms":{"plan":1,"prepare":2,"apply":3}}}`, false},
		{"wrong schema", `{"schema_version":2,"command":"install","data":{"timings_ms":{"plan":1,"prepare":2,"apply":3}}}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := installTimings([]byte(test.json))
			if (err == nil) != test.ok {
				t.Fatalf("timings error = %v, want success %t", err, test.ok)
			}
		})
	}
}

func TestLogicalDiskPartitionsDoNotFollowLinks(t *testing.T) {
	prefix, cache, outside := t.TempDir(), t.TempDir(), t.TempDir()
	for path, size := range map[string]int{
		filepath.Join(prefix, ".gobrew", "state.db"):            3,
		filepath.Join(prefix, "Cellar", "demo", "binary"):       5,
		filepath.Join(prefix, "etc", "config"):                  2,
		filepath.Join(prefix, "var", "data"):                    4,
		filepath.Join(cache, "artifacts", "blobs", "blob"):      7,
		filepath.Join(cache, "artifacts", "trees", "tree", "f"): 11,
		filepath.Join(cache, "catalog", "snapshot"):             13,
		filepath.Join(cache, "artifacts", "locks", "lock"):      2,
		filepath.Join(outside, "foreign"):                       1000,
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, make([]byte, size), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(prefix, "foreign-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(prefix, "Cellar", "demo"), filepath.Join(prefix, "linked-keg")); err != nil {
		t.Fatal(err)
	}
	usage, err := measureDisk(context.Background(), "gobrew", prefix, cache)
	if err != nil {
		t.Fatal(err)
	}
	if usage.PrefixTotal != 14 || usage.CacheTotal != 33 || usage.Prefix["state"] != 3 || usage.Prefix["kegs"] != 5 || usage.Cache["raw"] != 7 || usage.Cache["prepared"] != 11 || usage.Cache["catalog"] != 13 || usage.Cache["other"] != 2 {
		t.Fatalf("wrong logical partition sizes: %+v", usage)
	}
	if err := os.RemoveAll(filepath.Join(cache, "artifacts")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(cache, "artifacts")); err != nil {
		t.Fatal(err)
	}
	usage, err = measureDisk(context.Background(), "gobrew", prefix, cache)
	if err != nil || usage.CacheTotal != 13 || usage.Cache["raw"] != 0 || usage.Cache["prepared"] != 0 {
		t.Fatalf("followed a partition parent symlink: %+v, %v", usage, err)
	}
}
