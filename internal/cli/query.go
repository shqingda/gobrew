package cli

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shqingda/gobrew/internal/engine"
	"github.com/shqingda/gobrew/internal/model"
	"github.com/shqingda/gobrew/internal/platform"
	"github.com/shqingda/gobrew/internal/shellconfig"
	"github.com/spf13/cobra"
)

// searchCommand mirrors brew search [--formula] [--desc] text|/regex/.
func (a *application) searchCommand() *cobra.Command {
	var descriptions, formulaOnly, pullRequest, open, closed bool
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
	cmd.Flags().BoolVar(&formulaOnly, "formula", false, "Search only formulae (gobrew has no casks)")
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

func (a *application) infoCommand() *cobra.Command {
	var installed, analytics, sizes, github bool
	var days, category string
	cmd := a.commandFor("info [formula...]", "Show formula details and compatibility information, or installation statistics", cobra.ArbitraryArgs, func(ctx context.Context, names []string) error {
		if err := analyticsUsage(analytics, days, category, len(names) > 0); err != nil {
			return err
		}
		e, err := a.getEngine()
		if err != nil {
			return err
		}
		// brew info checks --sizes, then --analytics, then --github.
		switch {
		case sizes:
			return a.infoSizes(ctx, e, names)
		case analytics:
			return a.infoAnalytics(ctx, e, names, days, category)
		case github:
			return a.infoGitHub(ctx, e, names)
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
			if err := a.ensureCatalog(ctx, e); err != nil {
				return err
			}
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
		var analytics [][]model.Analytics
		if !installed && !a.json {
			analytics = formulaAnalytics(ctx, e, formulae)
		}
		a.setResult(formulae, func(w io.Writer) error {
			for i, f := range formulae {
				if i > 0 {
					if _, err := fmt.Fprintln(w); err != nil {
						return err
					}
				}
				var counts []model.Analytics
				if analytics != nil {
					counts = analytics[i]
				}
				if err := a.renderInfo(w, f, e.Prefix(), records, counts); err != nil {
					return err
				}
			}
			return nil
		})
		return nil
	})
	cmd.Flags().BoolVar(&installed, "installed", false, "Read installed metadata without opening the catalog")
	cmd.Flags().BoolVar(&analytics, "analytics", false, "List installation and build error data for formula (unless GOBREW_NO_ANALYTICS or GOBREW_NO_GITHUB_API is set)")
	cmd.Flags().StringVar(&days, "days", "", "How many days of analytics data to show: 30, 90 or 365")
	cmd.Flags().StringVar(&category, "category", "", "Which analytics to show: install, install-on-request or build-error")
	cmd.Flags().BoolVar(&sizes, "sizes", false, "Show the size of installed formulae")
	cmd.Flags().BoolVar(&github, "github", false, "Open the GitHub source page for formula in a browser")
	return cmd
}

// formulaSize is one row of brew info --sizes.
type formulaSize struct {
	Name  string `json:"name"`
	Bytes int64  `json:"bytes"`
}

// infoSizes ports brew info --sizes: the disk usage of every installed
// formula, or of the named ones, largest first, with a total.
func (a *application) infoSizes(ctx context.Context, e *engine.Engine, names []string) error {
	snapshot, err := e.Installed()
	if err != nil {
		return err
	}
	if len(names) == 0 {
		names = slices.Sorted(maps.Keys(snapshot.Records))
	} else {
		if err := a.ensureCatalog(ctx, e); err != nil {
			return err
		}
		cat, err := e.Catalog(ctx)
		if err != nil {
			return err
		}
		canonical := make([]string, 0, len(names))
		for _, name := range names {
			f, err := cat.Lookup(name)
			if err != nil {
				return err
			}
			// brew lists a formula named twice once.
			if !slices.Contains(canonical, f.Name) {
				canonical = append(canonical, f.Name)
			}
		}
		names = canonical
	}
	rows := make([]formulaSize, 0, len(names))
	for _, name := range names {
		row := formulaSize{Name: name}
		if r, ok := snapshot.Records[name]; ok {
			_, row.Bytes = diskUsage(r.Keg)
		}
		rows = append(rows, row)
	}
	slices.SortStableFunc(rows, func(x, y formulaSize) int { return cmp.Compare(y.Bytes, x.Bytes) })
	a.setResult(rows, func(w io.Writer) error {
		if len(rows) == 0 {
			return nil
		}
		if err := newProgress(w).title("Formulae sizes:"); err != nil {
			return err
		}
		for _, line := range sizeLines(rows) {
			if _, err := fmt.Fprintln(w, cleanText(line)); err != nil {
				return err
			}
		}
		return nil
	})
	return nil
}

// sizeLines is brew's print_sizes_table without its heading.
func sizeLines(rows []formulaSize) []string {
	var total int64
	nameWidth, sizeWidth := len("Total"), 0
	for _, r := range rows {
		total += r.Bytes
		nameWidth = max(nameWidth, len(r.Name))
		sizeWidth = max(sizeWidth, len(humanBytes(r.Bytes)))
	}
	sizeWidth = max(sizeWidth, len(humanBytes(total)))
	lines := make([]string, 0, len(rows)+1)
	for _, r := range rows {
		lines = append(lines, fmt.Sprintf("%-*s %*s", nameWidth, r.Name, sizeWidth, humanBytes(r.Bytes)))
	}
	return append(lines, fmt.Sprintf("%-*s %*s", nameWidth, "Total", sizeWidth, humanBytes(total)))
}

// infoGitHub ports brew info --github: one browser call for the source
// pages of every named formula.
func (a *application) infoGitHub(ctx context.Context, e *engine.Engine, names []string) error {
	if len(names) == 0 {
		return usageError{errors.New("this command requires a formula argument")}
	}
	if err := a.ensureCatalog(ctx, e); err != nil {
		return err
	}
	cat, err := e.Catalog(ctx)
	if err != nil {
		return err
	}
	urls := make([]string, 0, len(names))
	for _, name := range names {
		f, err := cat.Lookup(name)
		if err != nil {
			return err
		}
		if f.SourcePath == "" {
			return fmt.Errorf("%w: the catalog lists no source file for %s", model.ErrNotFound, f.Name)
		}
		urls = append(urls, githubURL(f))
	}
	a.setResult(urls, func(io.Writer) error {
		return openURLs(ctx, urls...)
	})
	return nil
}

// githubURL is brew's github_info for a homebrew/core formula.
func githubURL(f model.Formula) string {
	return "https://github.com/Homebrew/homebrew-core/blob/HEAD/" + f.SourcePath
}

// analyticsUsage applies brew info's checks on --analytics, --days and
// --category.
func analyticsUsage(analytics bool, days, category string, named bool) error {
	for _, flag := range []struct{ name, value string }{{"days", days}, {"category", category}} {
		if flag.value != "" && !analytics {
			return usageError{fmt.Errorf("`--%s` cannot be passed without `--analytics`", flag.name)}
		}
	}
	if !analytics {
		return nil
	}
	if days != "" && !slices.Contains([]string{"30", "90", "365"}, days) {
		return usageError{errors.New("`--days` must be one of 30, 90, 365")}
	}
	formulaCategories := []string{"install", "install-on-request", "build-error"}
	if category != "" && named && !slices.Contains(formulaCategories, category) {
		return usageError{errors.New("`--category` must be one of " + strings.Join(formulaCategories, ", ") + " when querying formulae")}
	}
	if all := append(formulaCategories, "cask-install", "os-version"); category != "" && !slices.Contains(all, category) {
		return usageError{errors.New("`--category` must be one of " + strings.Join(all, ", "))}
	}
	return nil
}

// analyticsResult is one part of brew info --analytics: a formula's own
// tables, or a ranking, whole or filtered to a name the catalog does not have.
type analyticsResult struct {
	Name      string            `json:"name,omitempty"`
	Analytics []model.Analytics `json:"analytics"`
	ranking   bool
	found     bool
}

// infoAnalytics is brew info --analytics. Named formulae show an Analytics
// section of tables filtered by --days and --category; without names, or for
// a name the catalog lacks, it shows the ranking for --category (default
// install) over --days (default 30).
func (a *application) infoAnalytics(ctx context.Context, e *engine.Engine, names []string, days, category string) error {
	results := make([]analyticsResult, 0, max(len(names), 1))
	var formulae []model.Formula
	if len(names) > 0 {
		if err := a.ensureCatalog(ctx, e); err != nil {
			return err
		}
		cat, err := e.Catalog(ctx)
		if err != nil {
			return err
		}
		for _, name := range names {
			f, err := cat.Lookup(name)
			switch {
			case errors.Is(err, model.ErrNotFound):
				results = append(results, analyticsResult{Name: name, ranking: true})
			case err != nil:
				return err
			default:
				formulae = append(formulae, f)
				results = append(results, analyticsResult{Name: f.Name})
			}
		}
	} else {
		results = append(results, analyticsResult{ranking: true})
	}
	counts := formulaAnalytics(ctx, e, formulae)
	rankingCategory, rankingDays := cmp.Or(category, "install"), cmp.Or(days, "30")
	var ranking []model.AnalyticsCount
	var rankingErr error
	fetched := false
	for i, r := range results {
		if !r.ranking {
			// formulaAnalytics returns nil when analytics are turned off.
			if counts != nil {
				f := counts[0]
				counts = counts[1:]
				results[i].found = f != nil
				results[i].Analytics = filterAnalytics(f, days, category)
			}
			continue
		}
		// Like brew, GOBREW_NO_ANALYTICS turns off only a formula's own
		// counts, not the rankings asked for here.
		if !fetched {
			n, _ := strconv.Atoi(rankingDays)
			ranking, rankingErr = e.GlobalAnalytics(ctx, rankingCategory, n)
			fetched = true
		}
		if rankingErr != nil || len(ranking) == 0 {
			continue
		}
		period := model.AnalyticsPeriod{Results: []model.AnalyticsCount{}}
		period.Days, _ = strconv.Atoi(rankingDays)
		for _, row := range ranking {
			if r.Name == "" || row.Name == r.Name || strings.HasPrefix(row.Name, r.Name+" ") {
				period.Results = append(period.Results, row)
				period.Count += row.Count
			}
		}
		results[i].found = true
		results[i].Analytics = []model.Analytics{{Category: rankingCategory, Periods: []model.AnalyticsPeriod{period}}}
	}
	if results == nil {
		results = []analyticsResult{}
	}
	a.setResult(results, func(w io.Writer) error {
		p := newProgress(w)
		width := p.width
		if !p.tty {
			width = 80
			if columns, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && columns > 0 {
				width = columns
			}
		}
		for i, r := range results {
			if i > 0 {
				if _, err := fmt.Fprintln(w); err != nil {
					return err
				}
			}
			if !r.found {
				continue
			}
			if r.ranking && r.Name != "" && len(r.Analytics[0].Periods[0].Results) == 0 {
				fmt.Fprintf(a.stderr, "Error: No results matching `%s` found!\n", cleanText(r.Name))
				continue
			}
			if !r.ranking {
				if err := p.title("Analytics"); err != nil {
					return err
				}
			}
			for _, analytics := range r.Analytics {
				header := map[string]string{"os-version": "macOS Version", "cask-install": "Token"}[analytics.Category]
				for _, period := range analytics.Periods {
					if err := p.title(fmt.Sprintf("%s (%d days)", analytics.Category, period.Days)); err != nil {
						return err
					}
					for _, line := range analyticsTable(period, width, cmp.Or(header, "Name (with options)")) {
						if _, err := fmt.Fprintln(w, cleanText(line)); err != nil {
							return err
						}
					}
				}
			}
		}
		return nil
	})
	return nil
}

func filterAnalytics(analytics []model.Analytics, days, category string) []model.Analytics {
	result := []model.Analytics{}
	for _, a := range analytics {
		if category != "" && a.Category != category {
			continue
		}
		kept := model.Analytics{Category: a.Category, Periods: []model.AnalyticsPeriod{}}
		for _, p := range a.Periods {
			if days == "" || strconv.Itoa(p.Days) == days {
				kept.Periods = append(kept.Periods, p)
			}
		}
		result = append(result, kept)
	}
	return result
}

// analyticsTable ports Utils::Analytics.table_output for one period.
func analyticsTable(period model.AnalyticsPeriod, width int, header string) []string {
	total, totalPercent := readableNumber(int(period.Count)), "100.00"
	maxIndex := len(strconv.Itoa(len(period.Results)))
	indexWidth := max(len("Index"), len("Total"), maxIndex)
	countWidth := max(len("Count"), len(total))
	percentWidth := max(len("Percent"), len(totalPercent))
	nameWidth := max(width-indexWidth-countWidth-percentWidth-10, 1)
	name := func(s string) string {
		if r := []rune(s); len(r) > nameWidth {
			s = string(r[:nameWidth])
		}
		return fmt.Sprintf("%-*s", nameWidth, s)
	}
	lines := []string{
		fmt.Sprintf("%*s | %s | %*s |  %*s", indexWidth, "Index", name(header), countWidth, "Count", percentWidth, "Percent"),
		strings.Repeat("-", indexWidth) + ":|-" + strings.Repeat("-", nameWidth) + "-|-" + strings.Repeat("-", countWidth) + ":|-" + strings.Repeat("-", percentWidth) + ":",
	}
	for i, r := range period.Results {
		percent := "0.00"
		if period.Count != 0 {
			percent = fmt.Sprintf("%.2f", float64(r.Count*100)/float64(period.Count))
		}
		index := fmt.Sprintf("%0*d", maxIndex, i+1)
		lines = append(lines, fmt.Sprintf("%-*s | %s | %*s | %*s%%", indexWidth, index, name(r.Name), countWidth, readableNumber(int(r.Count)), percentWidth, percent))
	}
	if len(period.Results) > 1 {
		lines = append(lines, fmt.Sprintf("%-*s | %s | %*s | %*s%%", indexWidth, "Total", name(""), countWidth, total, percentWidth, totalPercent))
	}
	return lines
}

// formulaAnalytics fetches every formula's install counts at once. Like brew,
// GOBREW_NO_ANALYTICS or GOBREW_NO_GITHUB_API turns them off, and a failed
// request only leaves the section out.
func formulaAnalytics(ctx context.Context, e *engine.Engine, formulae []model.Formula) [][]model.Analytics {
	if os.Getenv("GOBREW_NO_ANALYTICS") != "" || os.Getenv("GOBREW_NO_GITHUB_API") != "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	result := make([][]model.Analytics, len(formulae))
	var wg sync.WaitGroup
	for i, f := range formulae {
		wg.Go(func() {
			result[i], _ = e.FormulaAnalytics(ctx, f.Name)
		})
	}
	wg.Wait()
	return result
}

// analyticsLines is brew's summary: one line per category, with the total of
// each period.
func analyticsLines(analytics []model.Analytics) []string {
	lines := make([]string, 0, len(analytics))
	for _, a := range analytics {
		periods := make([]string, 0, len(a.Periods))
		for _, p := range a.Periods {
			periods = append(periods, fmt.Sprintf("%s (%d days)", readableNumber(int(p.Count)), p.Days))
		}
		lines = append(lines, a.Category+": "+strings.Join(periods, ", "))
	}
	return lines
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
// no options, so those parts are absent.
func (a *application) renderInfo(w io.Writer, f model.Formula, prefix string, records map[string]model.Record, analytics []model.Analytics) error {
	r, installed := records[f.Name]
	p := newProgress(w)
	write := func(lines ...string) error {
		return writeLines(w, lines)
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
	if message := f.DeprecationMessage(time.Now()); message != "" {
		lines = append(lines, strings.Split(strings.ToUpper(message[:1])+message[1:], "\n")...)
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
		lines = append(lines, "From: "+githubURL(f))
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
	if f.KegOnly && !r.Linked {
		caveats = append(caveats, a.kegOnlyCaveat(f, prefix, r.Keg))
	}
	if text := formulaCaveats(f, prefix); text != "" {
		caveats = append(caveats, text)
	}
	if installed {
		if text := shadowText(prefix, r); text != "" {
			caveats = append(caveats, text)
		}
	}
	if len(caveats) > 0 {
		if err := p.title("Caveats"); err != nil {
			return err
		}
		if err := write(strings.Join(caveats, "\n\n")); err != nil {
			return err
		}
	}
	if len(analytics) == 0 {
		return nil
	}
	if err := p.title("Analytics"); err != nil {
		return err
	}
	return write(analyticsLines(analytics)...)
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
	// brew's ✔ and ✘; GOBREW_NO_EMOJI (HOMEBREW_NO_EMOJI) spells them out.
	installedMark, missingMark := "\u2714", "\u2718"
	if os.Getenv("GOBREW_NO_EMOJI") != "" {
		installedMark, missingMark = "(installed)", "(missing)"
	}
	required, recommended, optional := declaredGroups(f)
	var lines []string
	for _, group := range []struct {
		label string
		names []string
		need  bool
	}{{"Required", required, true}, {"Recommended", recommended, false}, {"Optional", optional, false}} {
		if len(group.names) == 0 {
			continue
		}
		names := make([]string, len(group.names))
		for i, name := range group.names {
			names[i] = name
			if _, ok := records[strings.TrimPrefix(name, "homebrew/core/")]; tty && ok {
				names[i] += " " + installedMark
			} else if tty && installed && group.need {
				names[i] += " " + missingMark
			}
		}
		lines = append(lines, fmt.Sprintf("%s (%d): %s", group.label, len(names), strings.Join(names, ", ")))
	}
	if installed && len(r.Dependencies) > 0 {
		present := 0
		for _, dep := range r.Dependencies {
			if _, ok := records[strings.TrimPrefix(dep.Name, "homebrew/core/")]; ok {
				present++
			}
		}
		status := "all installed " + installedMark
		if missing := len(r.Dependencies) - present; missing > 0 {
			status = fmt.Sprintf("%d installed %s, %d missing %s", present, installedMark, missing, missingMark)
		}
		lines = append(lines, fmt.Sprintf("Recursive Runtime (%d): %s", len(r.Dependencies), status))
	}
	return lines
}

// declaredGroups splits dependencies like brew info. Build and test ones stay
// out: brew shows them only for source builds, which gobrew does not do.
func declaredGroups(f model.Formula) (required, recommended, optional []string) {
	if len(f.DeclaredDependencies) == 0 {
		return f.Dependencies, nil, nil
	}
	for _, dep := range f.DeclaredDependencies {
		switch {
		case dep.Recommended:
			recommended = append(recommended, dep.Name)
		case dep.Optional:
			optional = append(optional, dep.Name)
		case !dep.Build && !dep.Test:
			required = append(required, dep.Name)
		}
	}
	return required, recommended, optional
}

func (a *application) listCommand() *cobra.Command {
	var pinned, onRequest, notOnRequest, poured, fromSource, fullName, versions, multiple, onePerLine, long, reverse, byTime bool
	cmd := a.commandFor("list [formula...]", "List installed formulae, or the files of named ones, without network access", cobra.ArbitraryArgs, func(ctx context.Context, names []string) error {
		var filters []string
		for _, filter := range []struct {
			set  bool
			flag string
		}{{onRequest, "--installed-on-request"}, {notOnRequest, "--no-installed-on-request"}, {poured, "--poured-from-bottle"}, {fromSource, "--built-from-source"}} {
			if filter.set {
				filters = append(filters, "`"+filter.flag+"`")
			}
		}
		if len(names) > 0 && len(filters) > 0 {
			return usageError{errors.New("cannot use " + strings.Join(filters, ", ") + " with formula arguments")}
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
				a.warn(name + " not pinned")
			}
			named[name] = ok
		}
		if len(names) > 0 && !pinned && !versions && !multiple && !fullName {
			if missing != nil {
				return missing
			}
			return a.listFiles(snapshot, kegs)
		}
		if missing != nil && fullName {
			return missing
		}
		records := make([]model.Record, 0, len(snapshot.Records))
		statuses := map[string][]string{}
		for name, record := range snapshot.Records {
			// A prefix keeps one keg per formula, so none has multiple versions.
			if multiple || pinned && !record.Pinned || len(names) > 0 && !named[name] {
				continue
			}
			if len(filters) > 0 {
				if statuses[name] = listStatuses(record, onRequest, notOnRequest, poured); len(statuses[name]) == 0 {
					continue
				}
			}
			records = append(records, record)
		}
		sortListing(records, byTime, reverse)
		// Like brew list: names in columns on a terminal, one per line otherwise,
		// and "name version" lines with --versions.
		a.setResult(records, func(w io.Writer) error {
			if long {
				return a.longListing(ctx, w, filepath.Join(e.Prefix(), "Cellar"), records, reverse, byTime)
			}
			names := make([]string, len(records))
			widths := make([]int, len(records))
			for i, r := range records {
				names[i] = cleanText(r.Formula.Name)
				if versions {
					names[i] += " " + cleanText(r.Formula.PkgVersion())
				}
				if len(filters) > 1 {
					names[i] += ": " + strings.Join(statuses[r.Formula.Name], ", ")
				}
				widths[i] = len(names[i])
			}
			p := newProgress(w)
			if versions || pinned || len(filters) > 0 || onePerLine || !p.tty {
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
	cmd.Flags().BoolVar(&poured, "poured-from-bottle", false, "Only show formulae installed from a bottle, which is all of them")
	cmd.Flags().BoolVar(&fromSource, "built-from-source", false, "Only show formulae compiled from source; gobrew compiles none")
	cmd.Flags().BoolVar(&fullName, "full-name", false, "Print fully-qualified names (official formulae have no longer one)")
	cmd.Flags().BoolVar(&versions, "versions", false, "Show the version number of installed formulae")
	cmd.Flags().BoolVar(&multiple, "multiple", false, "Only show formulae with multiple versions installed; gobrew keeps one keg per formula")
	cmd.Flags().BoolVarP(&onePerLine, "1", "1", false, "Force output to be one entry per line")
	cmd.Flags().BoolVarP(&long, "l", "l", false, "List formulae in long format")
	cmd.Flags().BoolVarP(&reverse, "r", "r", false, "Reverse the sort order")
	cmd.Flags().BoolVarP(&byTime, "t", "t", false, "Sort by time modified, most recent first")
	// brew list's conflicts.
	cmd.MarkFlagsMutuallyExclusive("pinned", "multiple")
	for _, filter := range []string{"installed-on-request", "no-installed-on-request", "poured-from-bottle", "built-from-source"} {
		for _, flag := range []string{"versions", "multiple", "pinned", "l"} {
			cmd.MarkFlagsMutuallyExclusive(filter, flag)
		}
	}
	for _, layout := range []string{"1", "l", "r", "t"} {
		for _, flag := range []string{"versions", "multiple", "pinned"} {
			cmd.MarkFlagsMutuallyExclusive(layout, flag)
		}
	}
	for _, flag := range []string{"versions", "multiple", "pinned", "l", "r", "t"} {
		cmd.MarkFlagsMutuallyExclusive("full-name", flag)
	}
	return cmd
}

// listStatuses is why brew list's filters keep a formula: any one is enough.
// gobrew pours every keg from a bottle.
func listStatuses(r model.Record, onRequest, notOnRequest, poured bool) []string {
	var statuses []string
	if onRequest && r.Requested {
		statuses = append(statuses, "installed on request")
	}
	if notOnRequest && !r.Requested {
		statuses = append(statuses, "installed as dependency")
	}
	if poured {
		statuses = append(statuses, "poured from bottle")
	}
	return statuses
}

// sortListing orders records by name, or like ls -t by rack modification time
// with ties by name, and -r reverses either order.
func sortListing(records []model.Record, byTime, reverse bool) {
	modified := make(map[string]time.Time, len(records))
	if byTime {
		for _, r := range records {
			if info, err := os.Stat(filepath.Dir(r.Keg)); err == nil {
				modified[r.Formula.Name] = info.ModTime()
			}
		}
	}
	slices.SortFunc(records, func(a, b model.Record) int {
		if c := modified[b.Formula.Name].Compare(modified[a.Formula.Name]); c != 0 {
			return c
		}
		return strings.Compare(a.Formula.Name, b.Formula.Name)
	})
	if reverse {
		slices.Reverse(records)
	}
}

// longListing is brew list -l, which also leaves the format to ls.
func (a *application) longListing(ctx context.Context, w io.Writer, cellar string, records []model.Record, reverse, byTime bool) error {
	if len(records) == 0 {
		return nil
	}
	args := []string{"-l"}
	if reverse {
		args = append(args, "-r")
	}
	if byTime {
		args = append(args, "-t")
	}
	cmd := exec.CommandContext(ctx, "/bin/ls", append(args, "--", cellar)...)
	cmd.Env = slices.DeleteFunc(os.Environ(), func(entry string) bool { return strings.HasPrefix(entry, "CLICOLOR") })
	cmd.Stdout, cmd.Stderr = w, a.stderr
	return cmd.Run()
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
	var tree, graph, dot, forEach, fullName bool
	cmd := a.commandFor("deps [formula...]", "Show dependencies of formulae (their intersection for several)", cobra.ArbitraryArgs, func(ctx context.Context, names []string) error {
		if dot && !graph {
			return usageError{errors.New("`--dot` cannot be passed without `--graph`")}
		}
		if opts.Prune && !tree {
			return usageError{errors.New("`--prune` cannot be passed without `--tree`")}
		}
		if (tree || graph) && len(names) == 0 && !opts.Installed {
			return usageError{errors.New("this command requires a formula argument")}
		}
		e, err := a.getEngine()
		if err != nil {
			return err
		}
		if os.Getenv("GOBREW_NO_ENV_HINTS") == "" {
			snapshot, _ := e.Installed()
			allInstalled := !slices.ContainsFunc(names, func(name string) bool {
				_, ok := snapshot.Records[strings.TrimPrefix(name, "homebrew/core/")]
				return !ok
			})
			if reason := declaredReason(opts, tree, graph, allInstalled); reason != "" {
				a.warn(fmt.Sprintf("`gobrew deps` is not the actual runtime dependencies because %s!\nThis means dependencies may differ from a formula's declared dependencies.\nHide these hints with `GOBREW_NO_ENV_HINTS=1` (see `gobrew help`).", reason))
			}
		}
		switch {
		case tree:
			roots, err := e.DepsTree(ctx, names, opts)
			if roots != nil {
				a.setResult(roots, func(w io.Writer) error { return renderDepsTree(w, roots, opts.Annotate) })
			}
			return err
		case graph:
			edges, err := e.DepsGraph(ctx, names, opts)
			if err != nil {
				return err
			}
			a.setResult(edges, func(w io.Writer) error {
				code := dotCode(edges)
				if dot {
					_, err := fmt.Fprintln(w, cleanText(code))
					return err
				}
				return openURLs(ctx, graphvizURL(code))
			})
			return nil
		case forEach || len(names) == 0:
			each, err := e.DepsEach(ctx, names, opts)
			if err != nil {
				return err
			}
			a.setResult(each, func(w io.Writer) error {
				for _, item := range each {
					if _, err := fmt.Fprintln(w, cleanText(item.Name+": "+strings.Join(item.Dependencies, " "))); err != nil {
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
	cmd.Flags().BoolVar(&graph, "graph", false, "Show dependencies as a directed graph in the browser")
	cmd.Flags().BoolVar(&dot, "dot", false, "Print the graph in DOT format instead")
	cmd.Flags().BoolVar(&opts.IncludeBuild, "include-build", false, "Include build dependencies")
	cmd.Flags().BoolVar(&opts.IncludeTest, "include-test", false, "Include test dependencies (not recursive unless --graph or --tree)")
	cmd.Flags().BoolVar(&opts.IncludeOptional, "include-optional", false, "Include optional dependencies")
	cmd.Flags().BoolVar(&opts.IncludeImplicit, "include-implicit", false, "Include dependencies used to unpack source files")
	cmd.Flags().Bool("include-requirements", false, "Accepted like brew; official formulae list no requirements")
	cmd.Flags().BoolVar(&opts.SkipRecommended, "skip-recommended", false, "Skip recommended dependencies")
	cmd.Flags().BoolVar(&opts.Missing, "missing", false, "Show only missing dependencies")
	cmd.Flags().BoolVar(&opts.Annotate, "annotate", false, "Mark build, test, optional and recommended dependencies")
	cmd.Flags().BoolVar(&forEach, "for-each", false, "List dependencies one formula per line")
	cmd.Flags().BoolVar(&fullName, "full-name", false, "List dependencies by full name (official formulae have no longer one)")
	// brew's other spellings of --direct.
	for _, alias := range []string{"declared", "1"} {
		cmd.Flags().BoolVar(&opts.Direct, alias, false, "Show only direct dependencies")
		_ = cmd.Flags().MarkHidden(alias)
	}
	cmd.MarkFlagsMutuallyExclusive("tree", "graph")
	cmd.MarkFlagsMutuallyExclusive("installed", "missing")
	return cmd
}

// declaredReason is why brew deps reads declared dependencies, if it does; when
// several options apply, brew names the last in this order.
func declaredReason(opts engine.DepsOptions, tree, graph, allInstalled bool) string {
	reason := ""
	if !allInstalled && !opts.Installed {
		reason = "`--installed` was not passed"
	}
	for _, option := range []struct {
		set  bool
		name string
	}{
		{opts.Direct, "direct"}, {tree, "tree"}, {graph, "graph"}, {opts.SkipRecommended, "skip-recommended"}, {opts.Missing, "missing"},
		{opts.IncludeImplicit, "include-implicit"}, {opts.IncludeBuild, "include-build"}, {opts.IncludeTest, "include-test"}, {opts.IncludeOptional, "include-optional"},
	} {
		if option.set {
			reason = "--" + option.name + " was passed"
		}
	}
	return reason
}

// dotCode ports brew deps --graph --dot.
func dotCode(edges []engine.DepEdge) string {
	lines := make([]string, len(edges))
	for i, edge := range edges {
		dep := edge.Dependency
		var attributes, tags []string
		if dep.Build {
			attributes = append(attributes, "style = dotted")
			tags = append(tags, ":build")
		}
		if dep.Test {
			attributes = append(attributes, "arrowhead = empty")
			tags = append(tags, ":test")
		}
		if dep.Recommended {
			tags = append(tags, ":recommended")
		}
		if dep.Optional {
			attributes = append(attributes, "color = red")
			tags = append(tags, ":optional")
		} else if dep.Recommended {
			attributes = append(attributes, "color = green")
		}
		if dep.Implicit {
			tags = append(tags, ":implicit")
		}
		if dep.NoLinkage {
			tags = append(tags, ":no_linkage")
		}
		lines[i] = fmt.Sprintf("  %q -> %q", edge.From, dep.Name)
		if len(attributes) > 0 {
			lines[i] += " [" + strings.Join(attributes, ", ") + "]"
		}
		if len(tags) > 0 {
			lines[i] += " # " + strings.Join(tags, ", ")
		}
	}
	return "digraph {\n" + strings.Join(lines, "\n") + "\n}"
}

// graphvizURL is where brew deps --graph opens a graph. The code stays in the
// URL fragment, which browsers do not send to the site.
func graphvizURL(code string) string {
	return "https://dreampuf.github.io/GraphvizOnline/#" + strings.ReplaceAll(url.QueryEscape(code), "+", "%20")
}

// renderDepsTree follows brew deps --tree: each tree ends with a blank line, and
// --annotate adds a space to every name, tagged or not.
func renderDepsTree(w io.Writer, roots []engine.DepNode, annotate bool) error {
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
			label := node.Name
			if annotate {
				label += " "
			}
			for _, tag := range node.Tags {
				label += " [" + tag + "]"
			}
			if _, err := fmt.Fprintln(w, cleanText(prefix+branch+label+mark)); err != nil {
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
	var opts engine.DepsOptions
	var recursive bool
	cmd := a.commandFor("uses <formula>...", "Show formulae that depend on formulae (their intersection for several)", cobra.MinimumNArgs(1), func(ctx context.Context, names []string) error {
		e, err := a.getEngine()
		if err != nil {
			return err
		}
		opts.Direct = !recursive
		if !opts.Installed {
			if err := a.ensureCatalog(ctx, e); err != nil {
				return err
			}
		}
		uses, err := e.Uses(ctx, names, opts)
		if err != nil {
			return err
		}
		a.names(uses, "")
		return nil
	})
	cmd.Flags().BoolVar(&opts.Installed, "installed", false, "Only list installed formulae, without network access")
	cmd.Flags().BoolVar(&opts.Missing, "missing", false, "Only list formulae that are not installed")
	cmd.Flags().BoolVar(&recursive, "recursive", false, "Resolve more than one level of dependencies")
	cmd.Flags().BoolVar(&opts.IncludeBuild, "include-build", false, "Include formulae that need it to build")
	cmd.Flags().BoolVar(&opts.IncludeTest, "include-test", false, "Include formulae that need it to test")
	cmd.Flags().BoolVar(&opts.IncludeOptional, "include-optional", false, "Include formulae that use it optionally")
	cmd.Flags().BoolVar(&opts.IncludeImplicit, "include-implicit", false, "Include formulae that need it to unpack source files")
	cmd.Flags().BoolVar(&opts.SkipRecommended, "skip-recommended", false, "Skip formulae that only recommend it")
	cmd.MarkFlagsMutuallyExclusive("installed", "missing")
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
		// Like brew's shellenv, which puts brew itself on PATH, include the
		// managed command directory so the evaluated line also finds gobrew.
		if bin := managedBinDir(); bin != "" {
			env.Paths = append([]string{bin}, env.Paths...)
		}
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

// openURLs shows pages in the default browser, as brew does with exec_browser.
// Its failure is reported as such, not as a failure to write the output.
func openURLs(ctx context.Context, urls ...string) error {
	if err := exec.CommandContext(ctx, "/usr/bin/open", urls...).Run(); err != nil {
		return actionError{fmt.Errorf("could not open %s: %w", strings.Join(urls, " "), err)}
	}
	return nil
}
