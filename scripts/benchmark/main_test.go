package main

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBaselineEnvironmentSeparatesUserConfiguration(t *testing.T) {
	t.Setenv("HOMEBREW_CACHE", "/user-cache")
	t.Setenv("HOMEBREW_TEMP", "/tmp")
	t.Setenv("HOMEBREW_NO_INSTALL_FROM_API", "1")
	t.Setenv("XDG_CONFIG_HOME", "/user-config")
	cache := filepath.Join(t.TempDir(), "brew-cache")
	values := map[string]string{}
	for _, entry := range brewEnvironment(cache) {
		key, value, _ := strings.Cut(entry, "=")
		values[key] = value
	}
	if values["HOMEBREW_CACHE"] != cache || values["XDG_CONFIG_HOME"] != filepath.Join(filepath.Dir(cache), "brew-config") || values["HOMEBREW_NO_INSTALL_FROM_API"] != "" {
		t.Fatal("baseline inherited user Homebrew configuration")
	}
	if values["HOMEBREW_TEMP"] != filepath.Join(filepath.Dir(cache), "brew-temp") {
		t.Fatal("baseline temporary directory overlaps the disposable prefix")
	}
	config := filepath.Join(t.TempDir(), "brew.env")
	if err := os.WriteFile(config, []byte("HOMEBREW_CACHE=/user-cache\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := rejectBaselineConfig(config); err == nil {
		t.Fatal("baseline accepted an overriding environment file")
	}
}

func TestPerformanceGateRequiresCompleteFasterColdAndWarmSamples(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func([]measurement) []measurement
		pass   bool
	}{
		{"faster", nil, true},
		{"cold slower", func(ms []measurement) []measurement { ms[0].Milliseconds = []float64{3, 3, 3, 3, 3}; return ms }, false},
		{"warm equal", func(ms []measurement) []measurement { ms[1].Milliseconds = []float64{2, 2, 2, 2, 2}; return ms }, false},
		{"missing baseline", func(ms []measurement) []measurement { return ms[:3] }, false},
		{"missing no-op", func(ms []measurement) []measurement { return ms[:5] }, false},
		{"too few samples", func(ms []measurement) []measurement { ms[0].Milliseconds = ms[0].Milliseconds[:4]; return ms }, false},
		{"invalid sample", func(ms []measurement) []measurement { ms[0].Milliseconds[0] = math.NaN(); return ms }, false},
		{"zero sample", func(ms []measurement) []measurement { ms[0].Milliseconds[0] = 0; return ms }, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			ms := []measurement{}
			for _, name := range []string{"gobrew.cold", "gobrew.warm", "gobrew.noop", "brew.cold", "brew.warm", "brew.noop"} {
				value := 1.0
				if name == "brew.cold" || name == "brew.warm" || name == "gobrew.noop" {
					value = 2
				}
				ms = append(ms, measurement{Name: name, Milliseconds: []float64{value, value, value, value, value}})
			}
			if test.change != nil {
				ms = test.change(ms)
			}
			gate, err := checkPerformance(ms)
			if gate.Passed != test.pass || (err == nil) != test.pass {
				t.Fatalf("gate = %+v, err = %v, want pass %t", gate, err, test.pass)
			}
		})
	}
}

func TestBaselineResetRestoresConfigurationWithoutFollowingSymlinks(t *testing.T) {
	prefix, backup, outside := t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(prefix, "etc"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prefix, "etc", "original.conf"), []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := copyPath(filepath.Join(prefix, "etc"), filepath.Join(backup, "etc")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "untouched"), []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(prefix, "etc")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(prefix, "etc")); err != nil {
		t.Fatal(err)
	}
	b := brewBaseline{prefix: prefix, backup: backup}
	if err := b.reset(context.Background()); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{filepath.Join(prefix, "etc", "original.conf"): "original", filepath.Join(outside, "untouched"): "outside"} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("%s = %q, %v", path, got, err)
		}
	}
}
