package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/shqingda/gobrew/internal/engine"
	"github.com/shqingda/gobrew/internal/model"
	"github.com/shqingda/gobrew/internal/shellconfig"
	"github.com/spf13/cobra"
)

// searchCommand mirrors brew search [--formula] [--desc] text|/regex/.
func (a *application) searchCommand() *cobra.Command {
	var descriptions, formulaOnly bool
	cmd := a.commandFor("search [text|/regex/]", "Search formula names, or descriptions with --desc; list all without text", cobra.ArbitraryArgs, func(ctx context.Context, args []string) error {
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
	cmd := a.commandFor("info [formula...]", "Show formula details and compatibility information, or installation statistics", cobra.ArbitraryArgs, func(ctx context.Context, names []string) error {
		e, err := a.getEngine()
		if err != nil {
			return err
		}
		if len(names) == 0 {
			return a.infoStatistics(e)
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
				if err := renderInfo(w, f, e.Prefix(), records); err != nil {
					return err
				}
			}
			return nil
		})
		return nil
	})
	cmd.Flags().BoolVar(&installed, "installed", false, "Read installed metadata without opening the catalog")
	return cmd
}

// infoStatistics is brew info without arguments: "N kegs, M files, SIZE",
// or nothing while the Cellar does not exist.
func (a *application) infoStatistics(e *engine.Engine) error {
	snapshot, err := e.Installed()
	if err != nil {
		return err
	}
	cellar := filepath.Join(e.Prefix(), "Cellar")
	files, size := diskUsage(cellar)
	stats := map[string]int64{"kegs": int64(len(snapshot.Records)), "files": int64(files), "bytes": size}
	a.setResult(stats, func(w io.Writer) error {
		if _, err := os.Stat(cellar); err != nil {
			return nil
		}
		kegs := fmt.Sprintf("%d kegs", len(snapshot.Records))
		if len(snapshot.Records) == 1 {
			kegs = "1 keg"
		}
		_, err := fmt.Fprintln(w, kegs+", "+abv(files, size))
		return err
	})
	return nil
}

// renderInfo follows brew info's layout. gobrew installs no HEAD builds and has
// no options or analytics, so those parts are absent.
func renderInfo(w io.Writer, f model.Formula, prefix string, records map[string]model.Record) error {
	r, installed := records[f.Name]
	p := newProgress(w)
	write := func(lines ...string) error {
		for _, line := range lines {
			if _, err := fmt.Fprintln(w, cleanText(line)); err != nil {
				return err
			}
		}
		return nil
	}
	spec := "stable " + f.Version
	if installed && r.Formula.PkgVersion() != f.PkgVersion() {
		spec = r.Formula.PkgVersion() + " \u2192 stable " + f.PkgVersion()
	}
	if hasARMBottle(f) {
		spec += " (bottled)"
	}
	if f.KegOnly {
		spec += " [keg-only]"
	}
	if err := p.title(cleanText(f.Name + ": " + spec)); err != nil {
		return err
	}
	var lines []string
	for _, line := range []string{f.Description, f.Homepage} {
		if line != "" {
			lines = append(lines, line)
		}
	}
	if len(f.Aliases) > 0 {
		lines = append(lines, "Aliases: "+strings.Join(f.Aliases, ", "))
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
	if conflicts := conflictLines(f); len(conflicts) > 0 {
		lines = append(append(lines, "Conflicts with:"), conflicts...)
	}
	switch {
	case !installed:
		lines = append(lines, "Not installed")
	case r.Requested:
		lines = append(lines, "Installed (on request)")
	default:
		lines = append(lines, "Installed (as dependency)")
	}
	if installed && r.Status == model.NeedsPostInstall {
		lines = append(lines, strings.TrimRight("Needs post-install: "+r.PostInstallError, ": "))
	}
	if f.SourcePath != "" {
		lines = append(lines, "From: https://github.com/Homebrew/homebrew-core/blob/HEAD/"+f.SourcePath)
	}
	if f.License != "" {
		lines = append(lines, "License: "+f.License)
	}
	if err := write(lines...); err != nil {
		return err
	}
	if installed {
		files, size := diskUsage(r.Keg)
		line := r.Formula.Name + " " + r.Formula.PkgVersion() + " (" + abv(files, size) + ")"
		if r.Linked {
			line += " [Linked]"
		}
		if err := p.title("Installed Versions"); err != nil {
			return err
		}
		if err := write(line); err != nil {
			return err
		}
	}
	if dependencies := dependencyLines(f, r, installed, records, p.tty); len(dependencies) > 0 {
		if err := p.title("Dependencies"); err != nil {
			return err
		}
		if err := write(dependencies...); err != nil {
			return err
		}
	}
	var caveats []string
	if f.KegOnly {
		caveats = append(caveats, f.Name+" is keg-only, which means it was not symlinked into "+prefix+".\nTo link it anyway, run:\n  gobrew link --force "+f.Name)
	}
	if text := formulaCaveats(f, prefix); text != "" {
		caveats = append(caveats, text)
	}
	if installed {
		if text := shadowText(prefix, r); text != "" {
			caveats = append(caveats, text)
		}
	}
	if len(caveats) == 0 {
		return nil
	}
	if err := p.title("Caveats"); err != nil {
		return err
	}
	return write(strings.Join(caveats, "\n\n"))
}

// hasARMBottle mirrors brew's "(bottled)" for the only platform gobrew supports.
func hasARMBottle(f model.Formula) bool {
	for tag := range f.Bottles {
		if tag == "all" || strings.HasPrefix(tag, "arm64_") && !strings.HasSuffix(tag, "_linux") {
			return true
		}
	}
	return false
}

func conflictLines(f model.Formula) []string {
	lines := make([]string, 0, len(f.Conflicts))
	for i, name := range f.Conflicts {
		line := "  " + name
		if i < len(f.ConflictReasons) && f.ConflictReasons[i] != "" {
			line += " (because " + f.ConflictReasons[i] + ")"
		}
		lines = append(lines, line)
	}
	slices.Sort(lines)
	return lines
}

// dependencyLines marks each dependency on a terminal, as brew does, and flags
// a missing one only for an installed formula, which needs it.
func dependencyLines(f model.Formula, r model.Record, installed bool, records map[string]model.Record, tty bool) []string {
	var lines []string
	if len(f.Dependencies) > 0 {
		names := make([]string, len(f.Dependencies))
		for i, name := range f.Dependencies {
			names[i] = name
			if _, ok := records[strings.TrimPrefix(name, "homebrew/core/")]; tty && ok {
				names[i] += " \u2714"
			} else if tty && installed {
				names[i] += " \u2718"
			}
		}
		lines = append(lines, fmt.Sprintf("Required (%d): %s", len(names), strings.Join(names, ", ")))
	}
	if installed && len(r.Dependencies) > 0 {
		present := 0
		for _, dep := range r.Dependencies {
			if _, ok := records[strings.TrimPrefix(dep.Name, "homebrew/core/")]; ok {
				present++
			}
		}
		status := "all installed \u2714"
		if missing := len(r.Dependencies) - present; missing > 0 {
			status = fmt.Sprintf("%d installed \u2714, %d missing \u2718", present, missing)
		}
		lines = append(lines, fmt.Sprintf("Recursive Runtime (%d): %s", len(r.Dependencies), status))
	}
	return lines
}

func (a *application) listCommand() *cobra.Command {
	var pinned, onRequest, notOnRequest, versions, onePerLine bool
	cmd := a.commandFor("list [formula...]", "List installed formulae, or the files of named ones, without network access", cobra.ArbitraryArgs, func(_ context.Context, names []string) error {
		if len(names) > 0 && (onRequest || notOnRequest) {
			return usageError{errors.New("cannot use --installed-on-request or --no-installed-on-request with formula arguments")}
		}
		e, err := a.getEngine()
		if err != nil {
			return err
		}
		snapshot, err := e.Installed()
		if err != nil {
			return err
		}
		named := map[string]bool{}
		var kegs []string
		var missing error
		for _, name := range names {
			name = strings.TrimPrefix(name, "homebrew/core/")
			r, ok := snapshot.Records[name]
			if ok && !slices.Contains(kegs, name) {
				kegs = append(kegs, name)
			}
			if !ok {
				missing = notFound("No such keg: " + filepath.Join(e.Prefix(), "Cellar", name))
			} else if pinned && !r.Pinned {
				fmt.Fprintln(a.stderr, cleanText("Warning: "+name+" not pinned"))
			}
			named[name] = ok
		}
		if len(names) > 0 && !pinned && !versions {
			if missing != nil {
				return missing
			}
			return a.listFiles(snapshot, kegs)
		}
		records := make([]model.Record, 0, len(snapshot.Records))
		for name, record := range snapshot.Records {
			if pinned && !record.Pinned || onRequest && !record.Requested || notOnRequest && record.Requested || len(names) > 0 && !named[name] {
				continue
			}
			records = append(records, record)
		}
		slices.SortFunc(records, func(a, b model.Record) int { return strings.Compare(a.Formula.Name, b.Formula.Name) })
		// Like brew list: names in columns on a terminal, one per line otherwise,
		// and "name version" lines with --versions.
		a.setResult(records, func(w io.Writer) error {
			names := make([]string, len(records))
			widths := make([]int, len(records))
			for i, r := range records {
				names[i] = cleanText(r.Formula.Name)
				if versions {
					names[i] += " " + cleanText(r.Formula.PkgVersion())
				}
				widths[i] = len(names[i])
			}
			p := newProgress(w)
			if versions || onePerLine || !p.tty {
				for _, name := range names {
					if _, err := fmt.Fprintln(w, name); err != nil {
						return err
					}
				}
				return nil
			}
			_, err := io.WriteString(w, columns(names, widths, p.width))
			return err
		})
		return missing
	})
	cmd.Flags().BoolVar(&pinned, "pinned", false, "Only show pinned formulae")
	cmd.Flags().BoolVar(&onRequest, "installed-on-request", false, "Only show formulae installed on request")
	cmd.Flags().BoolVar(&notOnRequest, "no-installed-on-request", false, "Only show formulae installed as dependencies")
	cmd.Flags().BoolVar(&versions, "versions", false, "Show the version number of installed formulae")
	cmd.Flags().BoolVarP(&onePerLine, "1", "1", false, "Force output to be one entry per line")
	cmd.MarkFlagsMutuallyExclusive("installed-on-request", "no-installed-on-request")
	return cmd
}

// listFiles is brew list <formula>: every file off a terminal and a summary on
// one. JSON always carries every file.
func (a *application) listFiles(snapshot model.Snapshot, names []string) error {
	type kegListing struct {
		Name  string   `json:"name"`
		Keg   string   `json:"keg"`
		Files []string `json:"files"`
	}
	listings := make([]kegListing, 0, len(names))
	for _, name := range names {
		keg := snapshot.Records[name].Keg
		files, err := kegFiles(keg)
		if err != nil {
			return err
		}
		listings = append(listings, kegListing{name, keg, files})
	}
	a.setResult(listings, func(w io.Writer) error {
		tty := newProgress(w).tty
		for _, listing := range listings {
			lines := listing.Files
			if tty {
				var err error
				if lines, err = prettyKeg(listing.Keg); err != nil {
					return err
				}
			}
			for _, line := range lines {
				if _, err := fmt.Fprintln(w, cleanText(line)); err != nil {
					return err
				}
			}
		}
		return nil
	})
	return nil
}

// depsCommand mirrors brew deps [--direct] [--union] [--installed]
// [--topological] [--tree [--prune]] [--for-each] [formula...].
func (a *application) depsCommand() *cobra.Command {
	var opts engine.DepsOptions
	var tree, forEach, fullName bool
	cmd := a.commandFor("deps [formula...]", "Show dependencies of formulae (their intersection for several)", cobra.ArbitraryArgs, func(ctx context.Context, names []string) error {
		if len(names) == 0 && !opts.Installed {
			return usageError{errors.New("this command requires a formula argument")}
		}
		e, err := a.getEngine()
		if err != nil {
			return err
		}
		switch {
		case tree:
			roots, err := e.DepsTree(ctx, names, opts)
			if roots != nil {
				a.setResult(roots, func(w io.Writer) error { return renderDepsTree(w, roots) })
			}
			return err
		case forEach || len(names) == 0:
			each, err := e.DepsEach(ctx, names, opts)
			if err != nil {
				return err
			}
			a.setResult(each, func(w io.Writer) error {
				for _, item := range each {
					line := strings.TrimRight(item.Name+": "+strings.Join(item.Dependencies, " "), " ")
					if _, err := fmt.Fprintln(w, cleanText(line)); err != nil {
						return err
					}
				}
				return nil
			})
			return nil
		}
		deps, err := e.Deps(ctx, names, opts)
		if err != nil {
			return err
		}
		a.names(deps, "")
		return nil
	})
	cmd.Flags().BoolVarP(&opts.Direct, "direct", "1", false, "Show only direct dependencies")
	cmd.Flags().BoolVar(&opts.Union, "union", false, "Show the union of dependencies for several formulae")
	cmd.Flags().BoolVar(&opts.Installed, "installed", false, "Show only installed dependencies; without formulae, cover every installed formula")
	cmd.Flags().BoolVarP(&opts.Topological, "topological", "n", false, "Sort dependencies in topological order")
	cmd.Flags().BoolVar(&tree, "tree", false, "Show dependencies as a tree, one for each formula")
	cmd.Flags().BoolVar(&opts.Prune, "prune", false, "Prune parts of a tree already seen")
	cmd.Flags().BoolVar(&forEach, "for-each", false, "List dependencies one formula per line")
	cmd.Flags().BoolVar(&fullName, "full-name", false, "List dependencies by full name (official formulae have no longer one)")
	// brew's other spellings of --direct.
	for _, alias := range []string{"declared", "1"} {
		cmd.Flags().BoolVar(&opts.Direct, alias, false, "Show only direct dependencies")
		_ = cmd.Flags().MarkHidden(alias)
	}
	return cmd
}

// renderDepsTree follows brew deps --tree: each tree ends with a blank line.
func renderDepsTree(w io.Writer, roots []engine.DepNode) error {
	var walk func(nodes []engine.DepNode, prefix string) error
	walk = func(nodes []engine.DepNode, prefix string) error {
		for i, node := range nodes {
			branch, indent := "├── ", "│   "
			if i == len(nodes)-1 {
				branch, indent = "└── ", "    "
			}
			mark := ""
			if node.Circular {
				mark = " (CIRCULAR DEPENDENCY)"
			} else if node.Pruned {
				mark = " (PRUNED)"
			}
			if _, err := fmt.Fprintln(w, cleanText(prefix+branch+node.Name+mark)); err != nil {
				return err
			}
			if err := walk(node.Dependencies, prefix+indent); err != nil {
				return err
			}
		}
		return nil
	}
	for _, root := range roots {
		if _, err := fmt.Fprintln(w, cleanText(root.Name)); err != nil {
			return err
		}
		if err := walk(root.Dependencies, ""); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(w); err != nil {
			return err
		}
	}
	return nil
}

// usesCommand mirrors brew uses [--installed] [--recursive] formula...
func (a *application) usesCommand() *cobra.Command {
	var installed, recursive bool
	cmd := a.commandFor("uses <formula>...", "Show formulae that depend on formulae (their intersection for several)", cobra.MinimumNArgs(1), func(ctx context.Context, names []string) error {
		e, err := a.getEngine()
		if err != nil {
			return err
		}
		uses, err := e.Uses(ctx, names, installed, recursive)
		if err != nil {
			return err
		}
		a.names(uses, "")
		return nil
	})
	cmd.Flags().BoolVar(&installed, "installed", false, "Only list installed formulae, without network access")
	cmd.Flags().BoolVar(&recursive, "recursive", false, "Resolve more than one level of dependencies")
	return cmd
}

func (a *application) shellenvCommand() *cobra.Command {
	var shell string
	cmd := a.commandFor("shellenv [shell]", "Print shell commands that put gobrew tools on PATH", cobra.MaximumNArgs(1), func(_ context.Context, args []string) error {
		// Like brew, an explicit shell wins and $SHELL is only a fallback.
		if len(args) == 1 {
			shell = args[0]
		} else if shell == "" {
			shell = filepath.Base(os.Getenv("SHELL"))
		}
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
	// --shell predates the positional form that brew shellenv uses.
	cmd.Flags().StringVar(&shell, "shell", "", "Shell syntax")
	_ = cmd.Flags().MarkHidden("shell")
	cmd.Example = "  eval \"$(gobrew shellenv zsh)\""
	return cmd
}
