package cli

import (
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"time"

	"github.com/shqingda/gobrew/internal/model"
	"golang.org/x/sys/unix"
)

// progress renders engine events in Homebrew's style: "==>" titles for each
// step and, on a terminal, one transient bar for all concurrent downloads.
// Only the event goroutine in withEvents calls it.
type progress struct {
	w        io.Writer
	tty      bool
	color    bool
	width    int
	order    []string
	total    map[string]int64
	done     map[string]int64
	finished map[string]bool
	shown    bool
	drawn    time.Time
	// running is the package whose post-install step, or the step, started
	// at since; step is set while a "step" event's stage runs.
	running string
	step    string
	since   time.Time
}

func newProgress(w io.Writer) *progress {
	p := &progress{w: w, total: map[string]int64{}, done: map[string]int64{}, finished: map[string]bool{}}
	if f, ok := w.(*os.File); ok {
		if size, err := unix.IoctlGetWinsize(int(f.Fd()), unix.TIOCGWINSZ); err == nil {
			p.tty, p.width = true, int(size.Col)
			p.color = colorAllowed()
		}
	}
	return p
}

// colorAllowed follows NO_COLOR, Homebrew's HOMEBREW_NO_COLOR and TERM=dumb.
func colorAllowed() bool {
	return os.Getenv("NO_COLOR") == "" && os.Getenv("HOMEBREW_NO_COLOR") == "" && os.Getenv("GOBREW_NO_COLOR") == "" && os.Getenv("TERM") != "dumb"
}

// colorful reports whether w is a terminal that may receive color.
func colorful(w io.Writer) bool { return newProgress(w).color }

func (p *progress) event(event model.Event) error {
	name := cleanText(event.Package)
	switch event.Phase {
	case "download":
		if _, seen := p.total[name]; !seen {
			p.total[name], p.order = 0, append(p.order, name)
			if err := p.title("Downloading " + name); err != nil {
				return err
			}
		}
		// Without a known size (a compressed response), still count bytes.
		p.done[name] = event.Completed
		if event.Total > 0 {
			p.total[name] = event.Total
			p.finished[name] = event.Completed >= event.Total
		}
		return p.draw(false)
	case "outdated":
		// Homebrew prints this plain line before upgrading on install.
		if err := p.clear(); err != nil {
			return err
		}
		_, err := fmt.Fprintf(p.w, "%s %s is already installed but outdated (so it will be upgraded).\n", name, cleanText(event.Message))
		return err
	case "cache":
		return p.title("Using prepared " + name)
	case "extract":
		p.finished[name] = true
		return p.title("Pouring " + name)
	case "postinstall":
		p.running, p.since = name, time.Now()
		return p.title("Running post-install for " + name)
	case "installed":
		if p.running != name {
			return nil
		}
		elapsed := time.Since(p.since)
		p.running = ""
		if p.tty || elapsed < time.Second {
			return p.clear()
		}
		// Logs have no live timer, so record how long the step took.
		_, err := fmt.Fprintf(p.w, "Post-install for %s finished in %.1fs\n", name, elapsed.Seconds())
		return err
	case "output":
		if err := p.clear(); err != nil {
			return err
		}
		_, err := fmt.Fprintln(p.w, cleanText(event.Message))
		return err
	case "relocate":
		return nil
	case "step":
		// A titled stage with no byte count, such as verifying the catalog.
		// An empty message ends it; a terminal shows the elapsed time meanwhile.
		if event.Message == "" {
			if p.running == "" || p.step == "" {
				return nil
			}
			p.running, p.step = "", ""
			return p.clear()
		}
		p.step = cleanText(event.Message)
		p.running, p.since = p.step, time.Now()
		return p.title(p.step)
	}
	line := event.Phase + " " + name
	if event.Message != "" {
		line += ": " + cleanText(event.Message)
	}
	return p.title(line)
}

func (p *progress) title(text string) error {
	if err := p.clear(); err != nil {
		return err
	}
	var err error
	if p.color {
		_, err = fmt.Fprintf(p.w, "\033[34;1m==>\033[0m \033[1m%s\033[0m\n", text)
	} else {
		_, err = fmt.Fprintf(p.w, "==> %s\n", text)
	}
	if err != nil {
		return err
	}
	return p.draw(true)
}

func (p *progress) clear() error {
	if !p.shown {
		return nil
	}
	p.shown = false
	_, err := io.WriteString(p.w, "\r\033[K")
	return err
}

// tick shows how long a running post-install step or stage has taken, so a
// slow initialization such as ca-certificates does not look stalled.
func (p *progress) tick() error {
	if !p.tty || p.running == "" || time.Since(p.since) < time.Second {
		return nil
	}
	for _, name := range p.order {
		if !p.finished[name] {
			return nil // The download bar owns the transient line.
		}
	}
	p.shown = true
	_, err := fmt.Fprintf(p.w, "\r\033[K%.0fs elapsed", time.Since(p.since).Seconds())
	return err
}

// draw shows the combined bar while any download is unfinished. Redraws are
// limited to 20 per second; titles force one so the bar stays below them.
func (p *progress) draw(force bool) error {
	if !p.tty {
		return nil
	}
	var done, total int64
	active := false
	for _, name := range p.order {
		done, total = done+p.done[name], total+p.total[name]
		active = active || !p.finished[name]
	}
	if !active {
		return p.clear()
	}
	if !force && time.Since(p.drawn) < 50*time.Millisecond {
		return nil
	}
	p.drawn = time.Now()
	tail := " " + humanBytes(done)
	fraction := 0.0
	if total > 0 {
		fraction = min(float64(done)/float64(total), 1)
		tail = fmt.Sprintf(" %5.1f%% %s/%s", fraction*100, humanBytes(done), humanBytes(total))
	}
	bar := max(p.width-len(tail)-1, 10)
	filled := int(fraction * float64(bar))
	line := strings.Repeat("#", filled) + strings.Repeat(" ", bar-filled) + tail
	p.shown = true
	_, err := io.WriteString(p.w, "\r\033[K"+line)
	return err
}

// humanBytes ports Formatter.disk_usage_readable: decimal units, one place,
// and no trailing ".0".
func humanBytes(n int64) string {
	round := func(value float64) float64 { return math.Round(value*10) / 10 }
	size, unit := float64(n), "B"
	for _, next := range []string{"KB", "MB", "GB"} {
		if round(size) < 1000 {
			break
		}
		size, unit = size/1000, next
	}
	if size = round(size); size == math.Trunc(size) {
		return fmt.Sprintf("%.0f%s", size, unit)
	}
	return fmt.Sprintf("%.1f%s", size, unit)
}
