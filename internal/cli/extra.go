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
	"runtime"
	"slices"
	"strings"

	"github.com/shqingda/gobrew/internal/engine"
	"github.com/shqingda/gobrew/internal/model"
	"github.com/shqingda/gobrew/internal/platform"
	"github.com/spf13/cobra"
)

// gobrewHome is what `gobrew home` opens without arguments, as brew opens brew.sh.
const gobrewHome = "https://github.com/shqingda/gobrew"

// configReport is `gobrew config`: what a bug report needs, read locally.
type configReport struct {
	Version     string            `json:"version"`
	Commit      string            `json:"commit"`
	Built       string            `json:"built"`
	Prefix      string            `json:"prefix"`
	Cellar      string            `json:"cellar"`
	Cache       string            `json:"cache"`
	Catalog     string            `json:"catalog,omitempty"`
	CatalogTime string            `json:"catalog_refreshed,omitempty"`
	Installed   int               `json:"installed"`
	CLI         string            `json:"cli"`
	MacOS       string            `json:"macos"`
	CPU         string            `json:"cpu"`
	Environment map[string]string `json:"environment"`
}

func (a *application) configCommand() *cobra.Command {
	return a.commandFor("config", "Show gobrew and system configuration for bug reports", cobra.NoArgs, func(context.Context, []string) error {
		e, err := a.getEngine()
		if err != nil {
			return err
		}
		r := configReport{
			Version: a.build.Version, Commit: a.build.Commit, Built: a.build.Date,
			Prefix: e.Prefix(), Cellar: filepath.Join(e.Prefix(), "Cellar"), Cache: e.CacheDir(),
			CLI: "not managed (a source build or a copied executable)", CPU: runtime.GOARCH, Environment: map[string]string{},
		}
		if generation, err := e.CatalogGeneration(); err == nil {
			r.Catalog = generation
		}
		if fetched, err := e.CatalogFetched(); err == nil {
			r.CatalogTime = fetched.Format("2006-01-02 15:04:05 -0700")
		}
		if snapshot, err := e.Installed(); err == nil {
			r.Installed = len(snapshot.Records)
		}
		if dir := managedBinDir(); dir != "" {
			r.CLI = filepath.Join(dir, "gobrew")
		}
		r.MacOS = "unknown"
		if version, err := platform.SystemVersion(); err == nil {
			r.MacOS = version
		}
		for _, entry := range os.Environ() {
			name, value, _ := strings.Cut(entry, "=")
			if strings.HasPrefix(name, "GOBREW_") || slices.Contains([]string{"HOMEBREW_PREFIX", "HOMEBREW_NO_COLOR", "NO_COLOR", "SHELL"}, name) {
				r.Environment[name] = value
			}
		}
		// Never print tokens, even in a bug report.
		for _, name := range []string{"HOMEBREW_GITHUB_API_TOKEN", "GITHUB_TOKEN"} {
			if os.Getenv(name) != "" {
				r.Environment[name] = "set"
			}
		}
		a.setResult(r, func(w io.Writer) error { return renderConfig(w, r) })
		return nil
	})
}

func renderConfig(w io.Writer, r configReport) error {
	lines := []string{
		"GOBREW_VERSION: " + r.Version,
		"Commit: " + r.Commit,
		"Built: " + r.Built,
		"GOBREW_PREFIX: " + r.Prefix,
		"Cellar: " + r.Cellar,
		"Cache: " + r.Cache,
		"Catalog: " + cmp.Or(r.Catalog, "none (run `gobrew update`)"),
	}
	if r.CatalogTime != "" {
		lines = append(lines, "Catalog refreshed: "+r.CatalogTime)
	}
	lines = append(lines,
		fmt.Sprintf("Installed formulae: %d", r.Installed),
		"CLI: "+r.CLI,
		"macOS: "+r.MacOS+"-"+r.CPU,
	)
	for _, name := range slices.Sorted(maps.Keys(r.Environment)) {
		lines = append(lines, name+": "+r.Environment[name])
	}
	return writeLines(w, lines)
}

// leavesCommand mirrors brew leaves: installed formulae that no other
// installed formula depends on.
func (a *application) leavesCommand() *cobra.Command {
	var onRequest, asDependency bool
	cmd := a.commandFor("leaves", "List installed formulae that are not dependencies of another installed formula", cobra.NoArgs, func(context.Context, []string) error {
		e, err := a.getEngine()
		if err != nil {
			return err
		}
		snapshot, err := e.Installed()
		if err != nil {
			return err
		}
		a.names(leaves(snapshot.Records, onRequest, asDependency), "")
		return nil
	})
	cmd.Flags().BoolVarP(&onRequest, "installed-on-request", "r", false, "Only list leaves that were manually installed")
	cmd.Flags().BoolVarP(&asDependency, "installed-as-dependency", "p", false, "Only list leaves that were installed as dependencies")
	cmd.MarkFlagsMutuallyExclusive("installed-on-request", "installed-as-dependency")
	return cmd
}

func leaves(records map[string]model.Record, onRequest, asDependency bool) []string {
	needed := map[string]bool{}
	for _, r := range records {
		for _, d := range r.Dependencies {
			needed[d.Name] = true
		}
	}
	result := []string{}
	for name, r := range records {
		if needed[name] || onRequest && !r.Requested || asDependency && r.Requested {
			continue
		}
		result = append(result, name)
	}
	slices.Sort(result)
	return result
}

// descCommand mirrors brew desc: formula descriptions by name, or matches of
// a search in names, descriptions or both.
func (a *application) descCommand() *cobra.Command {
	var search, name, description bool
	cmd := a.commandFor("desc <formula|text|/regex/>...", "Display a formula's name and one-line description", cobra.MinimumNArgs(1), func(ctx context.Context, args []string) error {
		e, err := a.getEngine()
		if err != nil {
			return err
		}
		var formulae []model.Formula
		if search || name || description {
			formulae, err = a.searchFormulae(ctx, e, strings.Join(args, " "), !description, !name)
		} else {
			formulae, err = a.namedFormulae(ctx, e, args)
		}
		if err != nil {
			return err
		}
		type entry struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		}
		entries := make([]entry, 0, len(formulae))
		lines := make([]string, 0, len(formulae))
		for _, f := range formulae {
			entries = append(entries, entry{f.Name, f.Description})
			lines = append(lines, f.Name+": "+cmp.Or(f.Description, "(no description)"))
		}
		a.setResult(entries, func(w io.Writer) error { return writeLines(w, lines) })
		if len(formulae) == 0 {
			return notFound(fmt.Sprintf("No formulae found for %q.", strings.Join(args, " ")))
		}
		return nil
	})
	cmd.Flags().BoolVarP(&search, "search", "s", false, "Search both names and descriptions for the text")
	cmd.Flags().BoolVarP(&name, "name", "n", false, "Search only names for the text")
	// brew's -d is gobrew's global --debug, so only the long form exists.
	cmd.Flags().BoolVar(&description, "description", false, "Search only descriptions for the text")
	cmd.MarkFlagsMutuallyExclusive("search", "name", "description")
	return cmd
}

// searchFormulae returns the catalog matches in names, descriptions or both.
func (a *application) searchFormulae(ctx context.Context, e *engine.Engine, query string, names, descriptions bool) ([]model.Formula, error) {
	if err := a.ensureCatalog(ctx, e); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var result []model.Formula
	for _, inDescriptions := range []bool{false, true} {
		if inDescriptions && !descriptions || !inDescriptions && !names {
			continue
		}
		matches, err := e.Search(ctx, query, inDescriptions)
		if err != nil {
			return nil, err
		}
		for _, f := range matches {
			if !seen[f.Name] {
				seen[f.Name] = true
				result = append(result, f)
			}
		}
	}
	slices.SortStableFunc(result, func(a, b model.Formula) int { return strings.Compare(a.Name, b.Name) })
	return result, nil
}

// homeCommand mirrors brew home: open formula homepages, or gobrew's own.
func (a *application) homeCommand() *cobra.Command {
	return a.commandFor("home [formula...]", "Open a formula's homepage in a browser, or gobrew's without arguments", cobra.ArbitraryArgs, func(ctx context.Context, names []string) error {
		urls := []string{gobrewHome}
		if len(names) > 0 {
			e, err := a.getEngine()
			if err != nil {
				return err
			}
			formulae, err := a.namedFormulae(ctx, e, names)
			if err != nil {
				return err
			}
			urls = urls[:0]
			for _, f := range formulae {
				if f.Homepage == "" {
					return fmt.Errorf("%w: %s has no homepage", model.ErrNotFound, f.Name)
				}
				urls = append(urls, f.Homepage)
			}
		}
		if slices.ContainsFunc(urls, func(u string) bool { return !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") }) {
			return errors.New("refusing to open a homepage that is not a web address")
		}
		a.setResult(urls, func(io.Writer) error { return openURLs(ctx, urls...) })
		return nil
	})
}
