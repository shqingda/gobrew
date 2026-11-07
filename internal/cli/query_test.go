package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/shqingda/gobrew/internal/engine"
	"github.com/shqingda/gobrew/internal/model"
)

func TestDepsTreeMatchesHomebrewLayout(t *testing.T) {
	roots := []engine.DepNode{
		{Name: "hello"},
		{Name: "wget", Dependencies: []engine.DepNode{
			{Name: "libidn2", Dependencies: []engine.DepNode{{Name: "libunistring"}, {Name: "wget", Circular: true}}},
			{Name: "libunistring", Pruned: true},
		}},
	}
	var got bytes.Buffer
	if err := renderDepsTree(&got, roots); err != nil {
		t.Fatal(err)
	}
	want := "hello\n\nwget\n├── libidn2\n│   ├── libunistring\n│   └── wget (CIRCULAR DEPENDENCY)\n└── libunistring (PRUNED)\n\n"
	if got.String() != want {
		t.Fatalf("tree = %q, want %q", got.String(), want)
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

func TestHumanBytesMatchesHomebrew(t *testing.T) {
	for n, want := range map[int64]string{0: "0B", 999: "999B", 1000: "1KB", 1500: "1.5KB", 999_949: "999.9KB", 999_950: "1MB", 40_600_000: "40.6MB", 2_000_000_000: "2GB", 5_000_000_000_000: "5000GB"} {
		if got := humanBytes(n); got != want {
			t.Errorf("humanBytes(%d) = %s, want %s", n, got, want)
		}
	}
	if got := abv(1234567, 2000); got != "1,234,567 files, 2KB" {
		t.Fatalf("abv = %s", got)
	}
}

func TestKegListingsMatchHomebrew(t *testing.T) {
	keg := t.TempDir()
	for name, content := range map[string]string{
		model.OwnerMarker:        "owner",
		"INSTALL_RECEIPT.json":   "{}",
		"README.md":              "doc",
		"LICENSE-MIT":            "doc",
		"sbom.spdx.json":         "{}",
		".brew/tool.rb":          "rb",
		"bin/tool":               "x",
		"lib/libtool.1.dylib":    "x",
		"lib/libtool.a":          "x",
		"lib/libtool.la":         "x",
		"lib/pkgconfig/tool.pc":  "x",
		"share/man/man1/tool.1":  "x",
		"share/doc/tool/a":       "x",
		"share/doc/tool/b":       "x",
		"share/one/only":         "x",
		"libexec/tool/.DS_Store": "x",
		"libexec/tool/helper":    "x",
	} {
		path := filepath.Join(keg, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("libtool.1.dylib", filepath.Join(keg, "lib/libtool.dylib")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("share", filepath.Join(keg, "alias")); err != nil {
		t.Fatal(err)
	}

	got, err := prettyKeg(keg)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"alias -> share",
		"bin/tool",
		"lib/libtool.1.dylib",
		"lib/pkgconfig/tool.pc",
		"lib/ (3 other files)",
		"libexec/tool/ (2 files)",
		"sbom.spdx.json",
		"share/doc/ (2 files)",
		"share/man/man1/tool.1",
		"share/one/only",
	}
	for i := range want {
		want[i] = keg + "/" + want[i]
	}
	if !slices.Equal(got, want) {
		t.Fatalf("pretty listing:\n%s", strings.Join(got, "\n"))
	}

	files, err := kegFiles(keg)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(files, filepath.Join(keg, model.OwnerMarker)) || !slices.Contains(files, filepath.Join(keg, "alias")) || !slices.Contains(files, filepath.Join(keg, "INSTALL_RECEIPT.json")) {
		t.Fatalf("files = %v", files)
	}
}

func TestCaveatsExpandHomebrewPlaceholders(t *testing.T) {
	t.Setenv("HOME", "/Users/me")
	f := model.Formula{Caveats: "Set $HOMEBREW_PREFIX/opt/tmux and $HOMEBREW_CELLAR/x in /$HOME/.tmux.conf\n"}
	want := "Set /opt/gobrew/opt/tmux and /opt/gobrew/Cellar/x in /Users/me/.tmux.conf"
	if got := formulaCaveats(f, "/opt/gobrew"); got != want {
		t.Fatalf("caveats = %q, want %q", got, want)
	}
}

func TestInfoFollowsHomebrewLayout(t *testing.T) {
	keg := t.TempDir()
	for name, content := range map[string]string{model.OwnerMarker: "owner", "bin/tool": "1234"} {
		path := filepath.Join(keg, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	f := model.Formula{
		Name: "tool", Version: "2", Revision: 1, Description: "A tool", Homepage: "https://example.test", License: "MIT",
		Aliases: []string{"t"}, Dependencies: []string{"lib", "gone"}, KegOnly: true,
		Bottles:   map[string]model.Bottle{"arm64_sequoia": {}},
		Conflicts: []string{"zed", "other"}, ConflictReasons: []string{"both install tool", ""},
		SourcePath: "Formula/t/tool.rb", Caveats: "Read the manual.\n",
	}
	record := model.Record{
		Formula: model.Formula{Name: "tool", Version: "1"}, Keg: keg, Requested: true, Linked: true,
		Status: model.NeedsPostInstall, PostInstallError: "exit status 1",
		Dependencies: []model.RuntimeDependency{{Name: "lib"}, {Name: "gone"}},
	}
	var got bytes.Buffer
	if err := renderInfo(&got, f, "/opt/gobrew", map[string]model.Record{"tool": record, "lib": {}}); err != nil {
		t.Fatal(err)
	}
	want := `==> tool: 1 → stable 2_1 (bottled) [keg-only]
A tool
https://example.test
Aliases: t
Conflicts with:
  other
  zed (because both install tool)
Installed (on request)
Needs post-install: exit status 1
From: https://github.com/Homebrew/homebrew-core/blob/HEAD/Formula/t/tool.rb
License: MIT
==> Installed Versions
tool 1 (1 file, 4B) [Linked]
==> Dependencies
Required (2): lib, gone
Recursive Runtime (2): 1 installed ✔, 1 missing ✘
==> Caveats
tool is keg-only, which means it was not symlinked into /opt/gobrew.
To link it anyway, run:
  gobrew link --force tool

Read the manual.
`
	if got.String() != want {
		t.Fatalf("info:\n%s\nwant:\n%s", got.String(), want)
	}

	got.Reset()
	f = model.Formula{Name: "plain", Version: "1", Dependencies: []string{"lib"}}
	if err := renderInfo(&got, f, "/opt/gobrew", nil); err != nil {
		t.Fatal(err)
	}
	if want := "==> plain: stable 1\nNot installed\n==> Dependencies\nRequired (1): lib\n"; got.String() != want {
		t.Fatalf("not installed = %q", got.String())
	}
}
