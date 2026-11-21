package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/shqingda/gobrew/internal/model"
)

func TestProgressRendersHomebrewStyle(t *testing.T) {
	var plain bytes.Buffer
	p := newProgress(&plain)
	for _, e := range []model.Event{{Phase: "download", Package: "uv"}, {Phase: "download", Package: "uv", Completed: 5, Total: 10}, {Phase: "extract", Package: "uv"}, {Phase: "relocate", Package: "uv"}, {Phase: "postinstall", Package: "uv"}} {
		if err := p.event(e); err != nil {
			t.Fatal(err)
		}
	}
	// Logs get post-install output and, lacking a live timer, its duration.
	_ = p.event(model.Event{Phase: "output", Package: "uv", Message: "Warning: \033[31mcheck\033[0m"})
	p.since = time.Now().Add(-2 * time.Second)
	_ = p.event(model.Event{Phase: "installed", Package: "uv"})
	if want := "==> Downloading uv\n==> Pouring uv\n==> Running post-install for uv\nWarning: check\nPost-install for uv finished in 2.0s\n"; plain.String() != want || strings.Contains(plain.String(), "\033") {
		t.Fatalf("non-terminal output = %q", plain.String())
	}
	var term bytes.Buffer
	p = newProgress(&term)
	p.tty, p.width = true, 40
	_ = p.event(model.Event{Phase: "download", Package: "uv"})
	p.drawn = time.Time{} // skip the redraw throttle
	_ = p.event(model.Event{Phase: "download", Package: "uv", Completed: 512, Total: 1024})
	_ = p.event(model.Event{Phase: "extract", Package: "uv"})
	out := term.String()
	if !strings.Contains(out, "\r\033[K###") || !strings.Contains(out, " 50.0% 512B/1KB") || !strings.HasSuffix(out, "==> Pouring uv\n") {
		t.Fatalf("terminal output = %q", out)
	}
}

// The dependents check prints brew's wording: one aligned block of version
// changes, the opt-out hint, and pinned dependents as one warning.
func TestDependentLinesFollowBrew(t *testing.T) {
	t.Setenv("GOBREW_NO_ENV_HINTS", "")
	batch := []model.Event{{Package: "app", Message: "1.0 -> 1.1", Completed: 1}, {Package: "longer-tool", Message: "10.2_1 -> 10.3"}}
	got := dependentLines("dependent", batch, false)
	want := []string{
		"==> Upgrading 2 dependents of upgraded formula:",
		"Disable this behaviour by setting `GOBREW_NO_INSTALLED_DEPENDENTS_CHECK=1`.",
		"Hide these hints with `GOBREW_NO_ENV_HINTS=1` (see `gobrew help environment`).",
		"app          1.0    -> 1.1",
		"longer-tool  10.2_1 -> 10.3",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	t.Setenv("GOBREW_NO_ENV_HINTS", "1")
	got = dependentLines("dependent", batch[:1], true)
	if strings.Join(got, "\n") != "==> Would upgrade 1 dependent of upgraded formula:\napp 1.0 -> 1.1" {
		t.Fatalf("dry run = %q", got)
	}
	got = dependentLines("pinned-dependent", []model.Event{{Package: "a 1.0"}, {Package: "b 2.0"}}, false)
	if strings.Join(got, "\n") != "Warning: Not upgrading 2 pinned dependents:\na 1.0, b 2.0" {
		t.Fatalf("pinned = %q", got)
	}
}

func TestCleanTextRemovesWholeEscapeSequences(t *testing.T) {
	for in, want := range map[string]string{
		"\033[1;31mred\033[0m":   "red",
		"a\033[2Kb\033[?25lc":    "abc",
		"\033(Bplain\033":        "plain",
		"line\r\nnext\ttab\x07!": "line\nnext\ttab!",
		"plain":                  "plain",
	} {
		if got := cleanText(in); got != want {
			t.Errorf("cleanText(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestColorFollowsBrewPrecedence(t *testing.T) {
	for _, key := range []string{"NO_COLOR", "HOMEBREW_NO_COLOR", "GOBREW_NO_COLOR", "GOBREW_COLOR", "HOMEBREW_COLOR", "TERM"} {
		t.Setenv(key, "")
	}
	if colorAllowed(false) || !colorAllowed(true) {
		t.Fatal("default must follow the terminal")
	}
	t.Setenv("GOBREW_COLOR", "1")
	if !colorAllowed(false) {
		t.Fatal("GOBREW_COLOR did not force color")
	}
	t.Setenv("NO_COLOR", "1")
	if colorAllowed(true) {
		t.Fatal("NO_COLOR must win over GOBREW_COLOR")
	}
}

func TestProgressLineFitsANarrowTerminal(t *testing.T) {
	var term bytes.Buffer
	p := newProgress(&term)
	p.tty, p.width = true, 16
	_ = p.event(model.Event{Phase: "download", Package: "uv", Completed: 512, Total: 1024})
	line := term.String()[strings.LastIndex(term.String(), "\r\033[K")+4:]
	if len(line) >= 16 || strings.Contains(line, "#") {
		t.Fatalf("line %q does not fit 16 columns", line)
	}
}

func TestUninstallLinesFollowBrew(t *testing.T) {
	kegs := map[string]model.Removed{
		"jq":        {Path: "/opt/gobrew/Cellar/jq/1.8.2", Files: 18, Bytes: 1_200_000},
		"oniguruma": {Path: "/opt/gobrew/Cellar/oniguruma/6.9.10", Files: 1, Bytes: 2_000},
	}
	got := strings.Join(append(uninstallingLines([]string{"jq"}, kegs), autoremoveLines([]string{"oniguruma"}, kegs, false)...), "\n")
	want := "Uninstalling /opt/gobrew/Cellar/jq/1.8.2... (18 files, 1.2MB)\n" +
		"==> Autoremoving 1 unneeded formula:\noniguruma\n" +
		"Uninstalling /opt/gobrew/Cellar/oniguruma/6.9.10... (1 file, 2KB)"
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	if got := autoremoveLines([]string{"b", "a"}, nil, true); strings.Join(got, "|") != "==> Would autoremove 2 unneeded formulae:|a|b" {
		t.Fatalf("dry run = %q", got)
	}
}
