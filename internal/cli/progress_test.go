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
	if want := "==> Downloading uv\n==> Pouring uv\n==> Running post-install for uv\nWarning: [31mcheck[0m\nPost-install for uv finished in 2.0s\n"; plain.String() != want || strings.Contains(plain.String(), "\033") {
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
