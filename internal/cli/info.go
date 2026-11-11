package cli

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shqingda/gobrew/internal/engine"
	"github.com/shqingda/gobrew/internal/model"
	"github.com/spf13/cobra"
)

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
			catalog, err := e.CatalogFor(ctx, names)
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
		cat, err := e.CatalogFor(ctx, names)
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
	cat, err := e.CatalogFor(ctx, names)
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
		cat, err := e.CatalogFor(ctx, names)
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
