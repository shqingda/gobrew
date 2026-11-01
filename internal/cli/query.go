package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/shqingda/gobrew/internal/model"
	"github.com/shqingda/gobrew/internal/shellconfig"
	"github.com/spf13/cobra"
)

// searchCommand mirrors brew search [--formula] [--desc] text|/regex/.
func (a *application) searchCommand() *cobra.Command {
	var descriptions, formulaOnly bool
	cmd := a.commandFor("search <text|/regex/>...", "Search formula names, or descriptions with --desc", cobra.MinimumNArgs(1), func(ctx context.Context, args []string) error {
		e, err := a.getEngine()
		if err != nil {
			return err
		}
		query := strings.Join(args, " ")
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
			fmt.Fprintln(a.stderr, "Warning: Did you mean to perform a regular expression search?\nSurround your query with /slashes/ to search locally by regex.")
		}
		return nil
	})
	cmd.Flags().BoolVar(&descriptions, "desc", false, "Search descriptions instead of names")
	cmd.Flags().BoolVar(&formulaOnly, "formula", false, "Search only formulae (gobrew has no casks)")
	cmd.Example = "  gobrew search jq\n  gobrew search --desc json\n  gobrew search '/^python@/'"
	return cmd
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

func (a *application) infoCommand() *cobra.Command {
	var installed bool
	cmd := a.commandFor("info <formula>...", "Show formula details and compatibility information", cobra.MinimumNArgs(1), func(ctx context.Context, names []string) error {
		e, err := a.getEngine()
		if err != nil {
			return err
		}
		formulae := make([]model.Formula, 0, len(names))
		if installed {
			snapshot, err := e.Installed()
			if err != nil {
				return err
			}
			for _, name := range names {
				r, ok := snapshot.Records[strings.TrimPrefix(name, "homebrew/core/")]
				if !ok {
					return fmt.Errorf("%w: %s is not installed", model.ErrNotFound, name)
				}
				formulae = append(formulae, r.Formula)
			}
		} else {
			catalog, err := e.Catalog(ctx)
			if err != nil {
				return err
			}
			for _, name := range names {
				formula, err := catalog.Lookup(name)
				if err != nil {
					return err
				}
				formulae = append(formulae, formula)
			}
		}
		records := map[string]model.Record{}
		if snapshot, err := e.Installed(); err == nil {
			records = snapshot.Records
		}
		a.setResult(formulae, func(w io.Writer) error {
			for i, f := range formulae {
				if i > 0 {
					if _, err := fmt.Fprintln(w); err != nil {
						return err
					}
				}
				r, ok := records[f.Name]
				lines := infoLines(f, e.Prefix(), r, ok)
				for _, line := range lines {
					if line != "" {
						if _, err := fmt.Fprintln(w, cleanText(line)); err != nil {
							return err
						}
					}
				}
			}
			return nil
		})
		return nil
	})
	cmd.Flags().BoolVar(&installed, "installed", false, "Read installed metadata without opening the catalog")
	return cmd
}

// infoLines returns the text lines of one formula's info section.
func infoLines(f model.Formula, prefix string, r model.Record, installed bool) []string {
	lines := []string{f.Name + " " + f.PkgVersion(), f.Description, f.Homepage}
	if len(f.Dependencies) > 0 {
		lines = append(lines, "Dependencies: "+strings.Join(f.Dependencies, ", "))
	}
	if f.KegOnly {
		lines = append(lines, "Keg-only: not linked into the shared prefix by default.")
	}
	if f.Disabled {
		lines = append(lines, "Disabled upstream.")
	}
	if f.Deprecated {
		lines = append(lines, "Deprecated upstream.")
	}
	if f.UnsupportedReason != "" {
		lines = append(lines, "Unsupported: "+f.UnsupportedReason)
	}
	if f.Caveats != "" {
		lines = append(lines, f.Caveats)
	}
	if installed {
		lines = append(lines, shadowText(prefix, r))
	}
	return lines
}

func (a *application) listCommand() *cobra.Command {
	var pinned, onRequest, notOnRequest bool
	cmd := a.commandFor("list", "List installed formulae without network access", cobra.NoArgs, func(_ context.Context, _ []string) error {
		e, err := a.getEngine()
		if err != nil {
			return err
		}
		snapshot, err := e.Installed()
		if err != nil {
			return err
		}
		records := make([]model.Record, 0, len(snapshot.Records))
		for _, record := range snapshot.Records {
			if pinned && !record.Pinned || onRequest && !record.Requested || notOnRequest && record.Requested {
				continue
			}
			records = append(records, record)
		}
		slices.SortFunc(records, func(a, b model.Record) int { return strings.Compare(a.Formula.Name, b.Formula.Name) })
		a.setResult(records, func(w io.Writer) error {
			rows := make([][]string, 0, len(records))
			for _, r := range records {
				flags := []string{}
				if r.Requested {
					flags = append(flags, "requested")
				}
				if r.Pinned {
					flags = append(flags, "pinned")
				}
				if !r.Linked {
					flags = append(flags, "unlinked")
				}
				rows = append(rows, []string{r.Formula.Name, r.Formula.PkgVersion(), r.Status, strings.Join(flags, ",")})
			}
			return table(w, []string{"FORMULA", "VERSION", "STATUS", "FLAGS"}, rows)
		})
		return nil
	})
	cmd.Flags().BoolVar(&pinned, "pinned", false, "Only show pinned formulae")
	cmd.Flags().BoolVar(&onRequest, "installed-on-request", false, "Only show formulae installed on request")
	cmd.Flags().BoolVar(&notOnRequest, "no-installed-on-request", false, "Only show formulae installed as dependencies")
	cmd.MarkFlagsMutuallyExclusive("installed-on-request", "no-installed-on-request")
	return cmd
}

func (a *application) deps(ctx context.Context, names []string) error {
	e, err := a.getEngine()
	if err != nil {
		return err
	}
	deps, err := e.Deps(ctx, names[0])
	if err != nil {
		return err
	}
	a.names(deps, "No dependencies.")
	return nil
}

func (a *application) usesCommand() *cobra.Command {
	var installed bool
	cmd := a.commandFor("uses <formula>", "List formulae with a direct dependency on a formula", cobra.ExactArgs(1), func(ctx context.Context, names []string) error {
		e, err := a.getEngine()
		if err != nil {
			return err
		}
		uses, err := e.Uses(ctx, names[0], installed)
		if err != nil {
			return err
		}
		a.names(uses, "No dependent formulae.")
		return nil
	})
	cmd.Flags().BoolVar(&installed, "installed", false, "Only inspect installed formulae, without network access")
	return cmd
}

func (a *application) shellenvCommand() *cobra.Command {
	var shell string
	cmd := a.commandFor("shellenv", "Print shell commands that put gobrew tools on PATH", cobra.NoArgs, func(_ context.Context, _ []string) error {
		e, err := a.getEngine()
		if err != nil {
			return err
		}
		prefix := e.Prefix()
		env := shellconfig.Env{Paths: []string{filepath.Join(prefix, "bin"), filepath.Join(prefix, "sbin")}, Prefix: prefix}
		script, err := env.Script(shell)
		if err != nil {
			return usageError{err}
		}
		a.setResult(map[string]string{"shell": shell, "script": script}, func(w io.Writer) error { _, err := io.WriteString(w, script); return err })
		return nil
	})
	cmd.Flags().StringVar(&shell, "shell", "zsh", "Shell syntax: zsh, bash, sh, or fish")
	return cmd
}
