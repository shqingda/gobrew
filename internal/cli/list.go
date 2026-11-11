package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/shqingda/gobrew/internal/model"
	"github.com/spf13/cobra"
)

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
