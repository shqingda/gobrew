package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/shqingda/gobrew/internal/model"
	"github.com/shqingda/gobrew/internal/platform"
	"github.com/spf13/cobra"
)

// searchCommand mirrors brew search [--formula] [--desc] text|/regex/.
func (a *application) searchCommand() *cobra.Command {
	var descriptions, pullRequest, open, closed bool
	databases := make([]bool, len(searchDatabases))
	cmd := a.commandFor("search [text|/regex/]", "Search formula names, or descriptions with --desc; list all without text", cobra.ArbitraryArgs, func(ctx context.Context, args []string) error {
		for _, flag := range []struct {
			name string
			set  bool
		}{{"open", open}, {"closed", closed}} {
			if flag.set && !pullRequest {
				return usageError{fmt.Errorf("`--%s` cannot be passed without `--pull-request`", flag.name)}
			}
		}
		if pullRequest {
			only := ""
			if open && !closed {
				only = "open"
			} else if closed && !open {
				only = "closed"
			}
			return a.searchPullRequests(ctx, args, only)
		}
		for i, on := range databases {
			if on {
				return a.searchDatabase(ctx, searchDatabases[i], args)
			}
		}
		e, err := a.getEngine()
		if err != nil {
			return err
		}
		query := strings.Join(args, " ")
		if err := a.ensureCatalog(ctx, e); err != nil {
			return err
		}
		formulae, err := e.Search(ctx, query, descriptions)
		if err != nil {
			return err
		}
		installed := map[string]bool{}
		if snapshot, err := e.Installed(); err == nil {
			for name := range snapshot.Records {
				installed[name] = true
			}
		}
		a.setResult(formulae, func(w io.Writer) error { return renderSearch(w, formulae, installed, descriptions) })
		if len(formulae) == 0 {
			return notFound(fmt.Sprintf("No formulae found for %q.", query))
		}
		if newProgress(a.stderr).tty && !strings.HasPrefix(query, "/") && strings.ContainsAny(query, `\|()[]{}^$*+?`) {
			a.warn("Did you mean to perform a regular expression search?\nSurround your query with /slashes/ to search locally by regex.")
		}
		return nil
	})
	cmd.Flags().BoolVar(&descriptions, "desc", false, "Search descriptions instead of names")
	// Accepted and ignored: every gobrew search result is a formula.
	cmd.Flags().Bool("formula", false, "Search only formulae (gobrew has no casks)")
	cmd.Flags().BoolVar(&pullRequest, "pull-request", false, "Search for GitHub pull requests containing text")
	cmd.Flags().BoolVar(&open, "open", false, "Search for only open GitHub pull requests")
	cmd.Flags().BoolVar(&closed, "closed", false, "Search for only closed GitHub pull requests")
	cmd.MarkFlagsMutuallyExclusive("desc", "pull-request")
	cmd.MarkFlagsMutuallyExclusive("open", "closed")
	names := make([]string, len(searchDatabases))
	for i, db := range searchDatabases {
		names[i] = db.name
		cmd.Flags().BoolVar(&databases[i], db.name, false, "Search for text in the given database")
	}
	cmd.MarkFlagsMutuallyExclusive(names...)
	cmd.Example = "  gobrew search jq\n  gobrew search --desc json\n  gobrew search '/^python@/'"
	return cmd
}

// searchDatabases are brew search's PACKAGE_MANAGERS, in its order.
var searchDatabases = []struct{ name, url string }{
	{"alpine", "https://pkgs.alpinelinux.org/packages?name=%s"},
	{"repology", "https://repology.org/projects/?search=%s"},
	{"macports", "https://ports.macports.org/search/?q=%s"},
	{"fink", "https://pdb.finkproject.org/pdb/browse.php?summary=%s"},
	{"opensuse", "https://software.opensuse.org/search?q=%s"},
	{"fedora", "https://packages.fedoraproject.org/search?query=%s"},
	{"archlinux", "https://archlinux.org/packages/?q=%s"},
	{"debian", "https://packages.debian.org/search?keywords=%s&searchon=names&suite=all&section=all"},
	{"ubuntu", "https://packages.ubuntu.com/search?keywords=%s&searchon=names&suite=all&section=all"},
}

// searchPullRequests ports GitHub.print_pull_requests_matching: open pull
// requests, then at most 20 closed ones.
func (a *application) searchPullRequests(ctx context.Context, args []string, only string) error {
	if len(args) == 0 {
		return usageError{errors.New("this command requires at least 1 text or regex argument")}
	}
	e, err := a.getEngine()
	if err != nil {
		return err
	}
	query := strings.Join(args, " ")
	prs, err := e.PullRequests(ctx, query, only)
	if err != nil {
		return err
	}
	a.setResult(prs, func(w io.Writer) error { return renderPullRequests(w, prs, query) })
	return nil
}

// renderPullRequests prints open pull requests, then at most 20 closed ones.
func renderPullRequests(w io.Writer, prs []model.PullRequest, query string) error {
	p := newProgress(w)
	write := func(lines []string) error {
		return writeLines(w, lines)
	}
	var open, closed []string
	for _, pr := range prs {
		line := pr.Title + " (" + pr.URL + ")"
		if pr.State == "open" {
			open = append(open, line)
		} else {
			closed = append(closed, line)
		}
	}
	if len(open) > 0 {
		if err := p.title("Open pull requests"); err != nil {
			return err
		}
		if err := write(open); err != nil {
			return err
		}
	}
	if len(closed) > 0 {
		if len(open) > 0 {
			if err := write([]string{""}); err != nil {
				return err
			}
		}
		if err := p.title("Closed pull requests"); err != nil {
			return err
		}
		shown := closed[:min(len(closed), 20)]
		if len(closed) > 20 {
			shown = append(slices.Clone(shown), "...")
		}
		if err := write(shown); err != nil {
			return err
		}
	}
	if len(open) == 0 && len(closed) == 0 {
		return write([]string{"No pull requests found for " + strconv.Quote(query)})
	}
	return nil
}

// searchDatabase opens another package database's search page for the text,
// like brew search --debian.
func (a *application) searchDatabase(ctx context.Context, db struct{ name, url string }, args []string) error {
	if len(args) == 0 {
		return usageError{errors.New("this command requires at least 1 text or regex argument")}
	}
	u := fmt.Sprintf(db.url, platform.FormComponent(strings.Join(args, " ")))
	a.setResult(u, func(io.Writer) error {
		return openURLs(ctx, u)
	})
	return nil
}

// renderSearch lists matches in Homebrew's layout: columns on a terminal,
// one name per line otherwise, and "name: description" with --desc.
func renderSearch(w io.Writer, formulae []model.Formula, installed map[string]bool, descriptions bool) error {
	p := newProgress(w)
	if descriptions {
		if err := p.title("Formulae"); err != nil {
			return err
		}
		for _, f := range formulae {
			if _, err := fmt.Fprintln(w, cleanText(f.Name+": "+strings.Join(strings.Fields(f.Description), " "))); err != nil {
				return err
			}
		}
		return nil
	}
	names := make([]string, len(formulae))
	widths := make([]int, len(formulae))
	for i, f := range formulae {
		names[i], widths[i] = cleanText(f.Name), len(f.Name)
		if p.tty && installed[f.Name] {
			mark := " \u2714"
			if os.Getenv("GOBREW_NO_EMOJI") != "" {
				mark = " (installed)"
			}
			if p.color {
				names[i] = "\033[1m" + names[i] + "\033[32m" + mark + "\033[0m"
			} else {
				names[i] += mark
			}
			widths[i] += len([]rune(mark))
		}
	}
	if !p.tty {
		for _, name := range names {
			if _, err := fmt.Fprintln(w, name); err != nil {
				return err
			}
		}
		return nil
	}
	if err := p.title("Formulae"); err != nil {
		return err
	}
	_, err := io.WriteString(w, columns(names, widths, p.width))
	return err
}

// columns ports Homebrew's Formatter.columns: fill columns top to bottom,
// two spaces apart, without trailing whitespace; one per line if too narrow.
func columns(items []string, widths []int, width int) string {
	if len(items) == 0 {
		return ""
	}
	const gap = 2
	longest := slices.Max(widths)
	cols := (width + gap) / (longest + gap)
	if cols < 2 {
		return strings.Join(items, "\n") + "\n"
	}
	rows := (len(items) + cols - 1) / cols
	cols = (len(items) + rows - 1) / rows
	colWidth := (width+gap)/cols - gap
	var b strings.Builder
	for row := 0; row < rows; row++ {
		for i := row; i < len(items); i += rows {
			b.WriteString(items[i])
			if i+rows < len(items) {
				b.WriteString(strings.Repeat(" ", colWidth-widths[i]+gap))
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}
