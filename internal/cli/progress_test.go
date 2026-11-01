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
	if !strings.Contains(out, "\r\033[K###") || !strings.Contains(out, " 50.0% 512B/1.0KB") || !strings.HasSuffix(out, "==> Pouring uv\n") {
		t.Fatalf("terminal output = %q", out)
	}
}
