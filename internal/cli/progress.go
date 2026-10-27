package cli

import (
	"fmt"
	"io"
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
}

func newProgress(w io.Writer) *progress {
	p := &progress{w: w, total: map[string]int64{}, done: map[string]int64{}, finished: map[string]bool{}}
	if f, ok := w.(*os.File); ok {
		if size, err := unix.IoctlGetWinsize(int(f.Fd()), unix.TIOCGWINSZ); err == nil {
			p.tty, p.width = true, int(size.Col)
			p.color = os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb"
		}
	}
	return p
}

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
		if event.Total > 0 {
			p.total[name], p.done[name] = event.Total, event.Completed
			p.finished[name] = event.Completed >= event.Total
		}
		return p.draw(false)
	case "cache":
		return p.title("Using prepared " + name)
	case "extract":
		p.finished[name] = true
		return p.title("Pouring " + name)
	case "postinstall":
		return p.title("Running post-install for " + name)
	case "relocate", "installed":
		return nil
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

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	value, suffix := float64(n)/unit, "KB"
	for _, next := range []string{"MB", "GB", "TB"} {
		if value < unit {
			break
		}
		value, suffix = value/unit, next
	}
	return fmt.Sprintf("%.1f%s", value, suffix)
}
