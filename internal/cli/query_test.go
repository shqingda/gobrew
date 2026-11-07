package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

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
	if err := renderDepsTree(&got, roots, false); err != nil {
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

func TestKegOnlyCaveatMatchesHomebrew(t *testing.T) {
	t.Setenv("HOME", "/Users/me")
	t.Setenv("SHELL", "/bin/zsh")
	t.Setenv("PATH", "")
	t.Setenv("ZDOTDIR", "")
	keg := t.TempDir()
	for _, dir := range []string{"bin", "lib", "include"} {
		if err := os.Mkdir(filepath.Join(keg, dir), 0755); err != nil {
			t.Fatal(err)
		}
	}
	a := &application{}
	f := model.Formula{Name: "sqlite", KegOnly: true, KegOnlyReason: ":provided_by_macos"}
	want := `sqlite is keg-only, which means it was not symlinked into /opt/gobrew,
because macOS already provides this software and installing another version in
parallel can cause all kinds of trouble.

If you need to have sqlite first in your PATH, run:
  printf '%s\n' export\ PATH\=/opt/gobrew/opt/sqlite/bin:\$PATH >> ~/.zshrc

For compilers to find sqlite you may need to set:
  export LDFLAGS=-L/opt/gobrew/opt/sqlite/lib
  export CPPFLAGS=-I/opt/gobrew/opt/sqlite/include`
	if got := a.kegOnlyCaveat(f, "/opt/gobrew", keg); got != want {
		t.Fatalf("caveat:\n%s\nwant:\n%s", got, want)
	}
	// Before installation only the reason applies; older catalogs have none.
	f.KegOnlyReason, f.KegOnlyExplanation = "", ""
	if got, want := a.kegOnlyCaveat(f, "/opt/gobrew", ""), "sqlite is keg-only, which means it was not symlinked into /opt/gobrew."; got != want {
		t.Fatalf("no reason = %q", got)
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
	a := &application{}
	if err := a.renderInfo(&got, f, "/opt/gobrew", map[string]model.Record{"tool": record, "lib": {}}, nil); err != nil {
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
Read the manual.
`
	if got.String() != want {
		t.Fatalf("info:\n%s\nwant:\n%s", got.String(), want)
	}

	got.Reset()
	f = model.Formula{Name: "plain", Version: "1", Dependencies: []string{"lib"}}
	counts := []model.Analytics{{Category: "install", Periods: []model.AnalyticsPeriod{{Days: 30, Count: 64196}, {Days: 90, Count: 1180298}}}, {Category: "build-error", Periods: []model.AnalyticsPeriod{{Days: 30, Count: 1}}}}
	if err := a.renderInfo(&got, f, "/opt/gobrew", nil, counts); err != nil {
		t.Fatal(err)
	}
	if want := "==> plain: stable 1\nNot installed\n==> Dependencies\nRequired (1): lib\n==> Analytics\ninstall: 64,196 (30 days), 1,180,298 (90 days)\nbuild-error: 1 (30 days)\n"; got.String() != want {
		t.Fatalf("not installed = %q", got.String())
	}
}

func TestListingSortsLikeLs(t *testing.T) {
	cellar := t.TempDir()
	base := time.Now().Add(-time.Hour)
	records := make([]model.Record, 0, 3)
	for name, age := range map[string]time.Duration{"old": 2 * time.Minute, "tie-b": time.Minute, "tie-a": time.Minute} {
		rack := filepath.Join(cellar, name)
		if err := os.MkdirAll(filepath.Join(rack, "1"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(rack, base.Add(-age), base.Add(-age)); err != nil {
			t.Fatal(err)
		}
		records = append(records, model.Record{Formula: model.Formula{Name: name}, Keg: filepath.Join(rack, "1")})
	}
	for _, test := range []struct {
		byTime, reverse bool
		want            string
	}{
		{false, false, "old tie-a tie-b"},
		{false, true, "tie-b tie-a old"},
		{true, false, "tie-a tie-b old"},
		{true, true, "old tie-b tie-a"},
	} {
		sortListing(records, test.byTime, test.reverse)
		names := make([]string, len(records))
		for i, r := range records {
			names[i] = r.Formula.Name
		}
		if got := strings.Join(names, " "); got != test.want {
			t.Fatalf("byTime=%v reverse=%v: got %q, want %q", test.byTime, test.reverse, got, test.want)
		}
	}
}

func TestDepsGraphAndAnnotatedTreeMatchHomebrew(t *testing.T) {
	code := dotCode([]engine.DepEdge{
		{From: "jq", Dependency: model.DeclaredDependency{Name: "oniguruma"}},
		{From: "oniguruma", Dependency: model.DeclaredDependency{Name: "autoconf", Build: true}},
		{From: "demo", Dependency: model.DeclaredDependency{Name: "lzip", Build: true, Test: true, Implicit: true}},
		{From: "demo", Dependency: model.DeclaredDependency{Name: "x", Recommended: true}},
	})
	want := `digraph {
  "jq" -> "oniguruma"
  "oniguruma" -> "autoconf" [style = dotted] # :build
  "demo" -> "lzip" [style = dotted, arrowhead = empty] # :build, :test, :implicit
  "demo" -> "x" [color = green] # :recommended
}`
	if code != want {
		t.Fatalf("dot = %s", code)
	}
	if got := graphvizURL("a -> \"b\""); got != "https://dreampuf.github.io/GraphvizOnline/#a%20-%3E%20%22b%22" {
		t.Fatalf("url = %s", got)
	}
	var tree bytes.Buffer
	roots := []engine.DepNode{{Name: "jq", Dependencies: []engine.DepNode{{Name: "plain"}, {Name: "autoconf", Tags: []string{"build"}}}}}
	if err := renderDepsTree(&tree, roots, true); err != nil {
		t.Fatal(err)
	}
	if want := "jq\n├── plain \n└── autoconf  [build]\n\n"; tree.String() != want {
		t.Fatalf("tree = %q", tree.String())
	}
}

func TestDeclaredReasonNamesTheLastOptionLikeHomebrew(t *testing.T) {
	for _, test := range []struct {
		opts         engine.DepsOptions
		tree         bool
		allInstalled bool
		want         string
	}{
		{allInstalled: true},
		{want: "`--installed` was not passed"},
		{opts: engine.DepsOptions{Installed: true}},
		{opts: engine.DepsOptions{Direct: true}, allInstalled: true, want: "--direct was passed"},
		{opts: engine.DepsOptions{Direct: true, IncludeBuild: true}, tree: true, want: "--include-build was passed"},
	} {
		if got := declaredReason(test.opts, test.tree, false, test.allInstalled); got != test.want {
			t.Fatalf("%+v: got %q, want %q", test, got, test.want)
		}
	}
}

func TestDeclaredGroupsSplitLikeBrewInfo(t *testing.T) {
	f := model.Formula{Dependencies: []string{"a", "r"}, DeclaredDependencies: []model.DeclaredDependency{
		{Name: "lzip", Build: true, Test: true, Implicit: true}, {Name: "a"}, {Name: "b", Build: true}, {Name: "r", Recommended: true}, {Name: "o", Optional: true},
	}}
	got := dependencyLines(f, model.Record{}, true, map[string]model.Record{"o": {}}, true)
	want := []string{"Required (1): a \u2718", "Recommended (1): r", "Optional (1): o \u2714"}
	if !slices.Equal(got, want) {
		t.Fatalf("lines = %q", got)
	}
	f.DeclaredDependencies = nil
	if got := dependencyLines(f, model.Record{}, false, nil, false); !slices.Equal(got, []string{"Required (2): a, r"}) {
		t.Fatalf("older catalog = %q", got)
	}
}

func TestDeprecationMessageMatchesHomebrew(t *testing.T) {
	today := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		f    model.Formula
		want string
	}{
		{model.Formula{}, ""},
		{model.Formula{Deprecated: true}, "deprecated!"},
		{model.Formula{Deprecated: true, DeprecationReason: "unmaintained", DeprecationDate: "2026-07-17", DisableDate: "2027-01-05"}, "deprecated because it is not maintained upstream! It will be disabled on 2027-01-05."},
		{model.Formula{Deprecated: true, DeprecationReason: "needs EOL `pcre`", DeprecationDate: "2026-03-01"}, "deprecated because it needs EOL `pcre`! It will be disabled on 2027-03-01."},
		{model.Formula{Deprecated: true, DeprecationDate: "2025-01-01"}, "deprecated!"},
		{model.Formula{Disabled: true, DisableReason: "repo_removed", DisableDate: "2026-01-02", DisableFormula: "next"}, "disabled because it has a removed upstream repository! It was disabled on 2026-01-02.\nReplacement:\n  gobrew install next"},
		{model.Formula{Deprecated: true, DeprecationCask: "app"}, "deprecated!\nReplacement:\n  brew install --cask app"},
	} {
		if got := test.f.DeprecationMessage(today); got != test.want {
			t.Fatalf("got %q, want %q", got, test.want)
		}
	}
}

func TestListStatusesKeepAnyMatch(t *testing.T) {
	dependency := model.Record{}
	if got := listStatuses(dependency, true, false, false); len(got) != 0 {
		t.Fatalf("dependency kept: %v", got)
	}
	if got := listStatuses(dependency, true, false, true); !slices.Equal(got, []string{"poured from bottle"}) {
		t.Fatalf("got %v", got)
	}
	if got := listStatuses(model.Record{Requested: true}, true, false, true); !slices.Equal(got, []string{"installed on request", "poured from bottle"}) {
		t.Fatalf("got %v", got)
	}
}

func TestInstallTimesFollowHomebrewLayout(t *testing.T) {
	var times *installTimes
	times.observe(model.Event{Package: "jq"}, time.Now())
	if times.lines() != nil {
		t.Fatal("times recorded without --display-times")
	}
	times = &installTimes{}
	start := time.Unix(100, 0)
	times.observe(model.Event{Phase: "download", Package: "oniguruma"}, start)
	times.observe(model.Event{Phase: "download", Package: "jq"}, start.Add(time.Second))
	times.observe(model.Event{Phase: "installed", Package: "oniguruma"}, start.Add(1500*time.Millisecond))
	times.observe(model.Event{Phase: "installed", Package: "jq"}, start.Add(3250*time.Millisecond))
	times.observe(model.Event{Phase: "switching"}, start.Add(4*time.Second))
	want := []string{"==> Installation times", "oniguruma                 1.500 s", "jq                        2.250 s"}
	if got := times.lines(); !slices.Equal(got, want) {
		t.Fatalf("lines = %q", got)
	}
}
