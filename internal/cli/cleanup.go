package cli

import (
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/shqingda/gobrew/internal/engine"
	"github.com/shqingda/gobrew/internal/model"
	"github.com/spf13/cobra"
)

func (a *application) autoremoveCommand() *cobra.Command {
	var dryRun bool
	cmd := a.commandFor("autoremove", "Remove dependencies no longer needed by requested or pinned formulae", cobra.NoArgs, func(ctx context.Context, _ []string) error {
		e, err := a.getEngine()
		if err != nil {
			return err
		}
		names, err := e.Autoremove(ctx, dryRun)
		if err != nil {
			return err
		}
		if names == nil {
			names = []string{}
		}
		// brew's headings: "==> Autoremoving 2 unneeded formulae:", or
		// "Would autoremove" for a dry run, then one name per line.
		a.setResult(names, func(w io.Writer) error {
			if len(names) == 0 {
				return writeLines(w, []string{"No unused dependencies."})
			}
			noun := "formulae"
			if len(names) == 1 {
				noun = "formula"
			}
			verb := "Autoremoving"
			if dryRun {
				verb = "Would autoremove"
			}
			return writeLines(w, append([]string{fmt.Sprintf("==> %s %d unneeded %s:", verb, len(names), noun)}, names...))
		})
		return nil
	})
	cmd.Flags().BoolVarP(&dryRun, "dry-run", "n", false, "List unused dependencies without removing them")
	return cmd
}

func (a *application) doctorCommand() *cobra.Command {
	var repair bool
	cmd := a.commandFor("doctor", "Check installed state, links and dependencies", cobra.NoArgs, func(ctx context.Context, _ []string) error {
		e, err := a.getEngine()
		if err != nil {
			return err
		}
		findings, err := e.Doctor(ctx, repair)
		if err != nil {
			return err
		}
		unhealthy := slices.ContainsFunc(findings, func(f engine.Finding) bool { return f.Severity == "" })
		if snapshot, err := e.Installed(); err == nil {
			findings = append(findings, a.pathFindings(e, snapshot.Records)...)
		}
		findings = append(findings, a.environmentFindings(e)...)
		a.setResult(findings, func(w io.Writer) error {
			if len(findings) == 0 {
				_, err := fmt.Fprintln(w, "Your system is ready to brew.")
				return err
			}
			rows := make([][]string, 0, len(findings))
			for _, f := range findings {
				problem := f.Problem
				if f.Severity != "" {
					problem = f.Severity + ": " + problem
				}
				rows = append(rows, []string{f.Package, problem})
			}
			return table(w, []string{"FORMULA", "PROBLEM"}, rows)
		})
		if unhealthy {
			return model.ErrNeedsRepair
		}
		return nil
	})
	cmd.Flags().BoolVar(&repair, "repair", false, "Recover interrupted filesystem transactions and restore missing links before checking; does not replay post-install")
	return cmd
}

// cleanupCommand mirrors brew cleanup [--prune=days|all] [-n] [-s] [formula...].
func (a *application) cleanupCommand() *cobra.Command {
	var prune string
	var dryRun, scrub, prunePrefix bool
	cmd := a.commandFor("cleanup [formula...]", "Remove old versions of installed formulae and stale cache entries", cobra.ArbitraryArgs, func(ctx context.Context, names []string) error {
		e, err := a.getEngine()
		if err != nil {
			return err
		}
		opts := engine.CleanupOptions{Formulae: names, DryRun: dryRun, All: scrub, MaxAge: cleanupMaxAge()}
		switch prune {
		case "":
		case "all":
			opts.All = true
		default:
			days, err := strconv.Atoi(prune)
			if err != nil || days < 0 {
				return usageError{fmt.Errorf("--prune must be a number of days or all")}
			}
			opts.MaxAge = saturate(int64(days), 24*time.Hour)
		}
		var removed []model.Removed
		if prunePrefix {
			removed, err = e.PrunePrefix(ctx, dryRun)
		} else if removed, err = e.Cleanup(ctx, opts); err == nil && !dryRun && len(names) == 0 {
			err = e.MarkCleaned()
		}
		if removed == nil {
			removed = []model.Removed{}
		}
		a.setResult(removed, func(w io.Writer) error {
			lines := removedLines(removed, e.Prefix(), dryRun)
			if total := freed(removed); total > 0 {
				verb := "has freed"
				if dryRun {
					verb = "would free"
				}
				lines = append(lines, "==> This operation "+verb+" approximately "+humanBytes(total)+" of disk space.")
			}
			return writeLines(w, lines)
		})
		return err
	})
	cmd.Flags().StringVar(&prune, "prune", "", "Remove cache files older than this many days, or all")
	cmd.Flags().BoolVarP(&dryRun, "dry-run", "n", false, "Show what would be removed without removing anything")
	cmd.Flags().BoolVarP(&scrub, "scrub", "s", false, "Scrub the cache, including downloads for the latest versions")
	cmd.Flags().BoolVar(&prunePrefix, "prune-prefix", false, "Only prune gobrew's broken links and empty directories from the prefix")
	return cmd
}

// saturate is n units without wrapping around: a period too long to represent
// means "never", as it does for brew, rather than a short or negative one.
func saturate(n int64, unit time.Duration) time.Duration {
	if n >= int64(math.MaxInt64/unit) {
		return math.MaxInt64
	}
	return time.Duration(n) * unit
}

// cleanupMaxAge is Homebrew's HOMEBREW_CLEANUP_MAX_AGE_DAYS, default 120.
func cleanupMaxAge() time.Duration {
	days := 120
	if value, err := strconv.Atoi(os.Getenv("GOBREW_CLEANUP_MAX_AGE_DAYS")); err == nil && value >= 0 {
		days = value
	}
	return saturate(int64(days), 24*time.Hour)
}

// removedLines follows brew: pruned paths are listed only in a dry run, and a
// real run sums them up in one line.
func removedLines(removed []model.Removed, prefix string, dryRun bool) []string {
	lines := make([]string, 0, len(removed))
	links, dirs := 0, 0
	for _, r := range removed {
		switch {
		case r.Kind == model.BrokenLink:
			links++
			if dryRun {
				lines = append(lines, "Would remove (broken link): "+r.Path)
			}
		case r.Kind == model.EmptyDirectory:
			dirs++
			if dryRun {
				lines = append(lines, "Would remove (empty directory): "+r.Path)
			}
		case dryRun:
			lines = append(lines, "Would remove: "+r.Path+" ("+abv(r.Files, r.Bytes)+")")
		default:
			lines = append(lines, "Removing: "+r.Path+"... ("+abv(r.Files, r.Bytes)+")")
		}
	}
	if !dryRun && links+dirs > 0 {
		summary := fmt.Sprintf("Pruned %d symbolic links ", links)
		if dirs > 0 {
			summary += fmt.Sprintf("and %d directories ", dirs)
		}
		lines = append(lines, summary+"from "+prefix)
	}
	return lines
}

func freed(removed []model.Removed) int64 {
	var total int64
	for _, r := range removed {
		total += r.Bytes
	}
	return total
}

// installCleanup is brew's install-time cleanup: old versions of the formulae
// just installed, and every GOBREW_CLEANUP_PERIODIC_FULL_DAYS (default 30) a
// full cleanup. Like Homebrew, a missing marker starts the period instead.
func (a *application) installCleanup(ctx context.Context, e *engine.Engine, result model.Result) []string {
	if os.Getenv("GOBREW_NO_INSTALL_CLEANUP") != "" || len(result.Succeeded) == 0 {
		return nil
	}
	var skip []string
	for _, name := range strings.Split(os.Getenv("GOBREW_NO_CLEANUP_FORMULAE"), ",") {
		if name = strings.TrimSpace(name); name != "" {
			skip = append(skip, name)
		}
	}
	// An explicit older version is not the latest, so brew leaves it alone.
	formulae := slices.DeleteFunc(slices.Clone(result.Succeeded), func(name string) bool { return slices.Contains(result.Historic, name) })
	var lines []string
	if len(formulae) > 0 {
		removed, err := e.Cleanup(ctx, engine.CleanupOptions{Formulae: formulae, Skip: skip})
		if err != nil {
			return []string{"Warning: cleanup failed: " + err.Error()}
		}
		if len(removed) > 0 {
			lines = append(append(lines, "==> Cleanup"), removedLines(removed, e.Prefix(), false)...)
		}
	}
	days := 30
	if value, err := strconv.Atoi(os.Getenv("GOBREW_CLEANUP_PERIODIC_FULL_DAYS")); err == nil && value >= 0 {
		days = value
	}
	if due, err := e.PeriodicCleanupDue(saturate(int64(days), 24*time.Hour)); err == nil && due {
		lines = append(lines, fmt.Sprintf("==> `gobrew cleanup` has not been run in the last %d days, running now...", days))
		removed, err := e.Cleanup(ctx, engine.CleanupOptions{Skip: skip, MaxAge: cleanupMaxAge()})
		if err == nil {
			err = e.MarkCleaned()
		}
		if err != nil {
			return append(lines, "Warning: cleanup failed: "+err.Error())
		}
		lines = append(lines, removedLines(removed, e.Prefix(), false)...)
	}
	if len(lines) > 0 && os.Getenv("GOBREW_NO_ENV_HINTS") == "" {
		lines = append(lines, "Disable this behaviour by setting `GOBREW_NO_INSTALL_CLEANUP=1`.", "Hide these hints with `GOBREW_NO_ENV_HINTS=1` (see `gobrew help environment`).")
	}
	return lines
}
