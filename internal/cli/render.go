package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"
	"time"
	"unicode"

	"github.com/shqingda/gobrew/internal/engine"
	"github.com/shqingda/gobrew/internal/model"
)

func renderPlan(w io.Writer, plan model.Plan) error {
	rows := make([][]string, 0, len(plan.Packages))
	for _, p := range plan.Packages {
		kind := "dependency"
		if p.Requested {
			kind = "requested"
		}
		rows = append(rows, []string{p.Formula.Name, p.Formula.PkgVersion(), p.Bottle.Tag, kind})
	}
	if len(rows) == 0 && len(plan.Promote) == 0 {
		if _, err := fmt.Fprintln(w, "Nothing to install or upgrade."); err != nil {
			return err
		}
	} else if err := table(w, []string{"FORMULA", "VERSION", "BOTTLE", "REASON"}, rows); err != nil {
		return err
	}
	if len(plan.Unchanged) > 0 {
		if _, err := fmt.Fprintln(w, "Unchanged:", cleanText(strings.Join(plan.Unchanged, ", "))); err != nil {
			return err
		}
	}
	if len(plan.RuntimeChecks) > 0 {
		_, err := fmt.Fprintln(w, "Reuse requires bottle minimum-version verification:", cleanText(strings.Join(plan.RuntimeChecks, ", ")))
		return err
	}
	return nil
}

// renderInstall follows Homebrew: one "<keg>: N files, SIZE" line per poured
// keg, caveats, and an already-installed warning for unchanged requests.
func (a *application) renderInstall(w io.Writer, e *engine.Engine, request model.Request, result model.Result, notes []string) error {
	var lines []string
	records := map[string]model.Record{}
	if len(result.Succeeded)+len(result.Unchanged) > 0 {
		if snapshot, err := e.Installed(); err == nil {
			records = snapshot.Records
		}
	}
	for _, name := range result.Succeeded {
		if r, ok := records[name]; ok && r.Keg != "" {
			lines = append(lines, kegSummary(r.Keg))
		} else {
			lines = append(lines, "Installed "+name)
		}
	}
	for _, name := range result.Succeeded {
		if !slices.Contains(request.Names, name) {
			continue
		}
		if request.SkipPostInstall {
			lines = append(lines, "==> Skipping 'post_install' on request", "You can run it manually using:", "  gobrew postinstall "+name)
		}
		if request.SkipLink {
			lines = append(lines, "==> Skipping 'link' on request", "You can run it manually using:", "  gobrew link "+name)
		}
	}
	for _, name := range result.Succeeded {
		r := records[name]
		if !r.Requested {
			continue
		}
		var caveats []string
		if r.Formula.KegOnly && !r.Linked {
			caveats = append(caveats, a.kegOnlyCaveat(r.Formula, e.Prefix(), r.Keg))
		}
		if text := formulaCaveats(r.Formula, e.Prefix()); text != "" {
			caveats = append(caveats, text)
		}
		if text := shadowText(e.Prefix(), r); text != "" {
			caveats = append(caveats, text)
		}
		if len(caveats) > 0 {
			lines = append(append(lines, "==> Caveats"), caveats...)
		}
	}
	for _, name := range result.Succeeded {
		if r := records[name]; r.Formula.Deprecated || r.Formula.Disabled {
			a.warn(name + " has been " + r.Formula.DeprecationMessage(time.Now()))
		}
	}
	for _, name := range result.Historic {
		version := ""
		if r, ok := records[name]; ok {
			version = " " + r.Formula.PkgVersion()
		}
		a.warn(name + version + " is not covered by Homebrew's signed metadata; its bottle was verified against GHCR and homebrew-core history.\n" +
			"gobrew upgrade will replace it with the current version. To keep it: gobrew pin " + name)
	}
	// Homebrew warns only about the formulae the user named.
	for _, name := range result.Unchanged {
		r, ok := records[name]
		if !ok || !slices.Contains(request.Names, name) {
			continue
		}
		version := r.Formula.PkgVersion()
		a.warn(fmt.Sprintf("%s %s is already installed.\nTo reinstall %s, run:\n  gobrew reinstall %s", name, version, version, name))
	}
	for _, group := range []struct {
		label string
		names []string
	}{{"Needs repair", result.NeedsRepair}, {"Not executed", result.NotExecuted}} {
		if len(group.names) > 0 {
			lines = append(lines, group.label+": "+strings.Join(group.names, ", "))
		}
	}
	lines = append(lines, a.times.lines()...)
	if t := result.Timings; a.verbose && t != nil {
		lines = append(lines, fmt.Sprintf("==> Stages: plan %.2fs, prepare %.2fs, apply %.2fs", t.Plan/1000, t.Prepare/1000, t.Apply/1000))
	}
	lines = append(lines, notes...)
	return writeLines(w, lines)
}

// formulaCaveats expands the placeholders Homebrew's API uses for local paths.
func formulaCaveats(f model.Formula, prefix string) string {
	pairs := []string{"$HOMEBREW_PREFIX", prefix, "$HOMEBREW_CELLAR", filepath.Join(prefix, "Cellar")}
	if home, err := os.UserHomeDir(); err == nil {
		pairs = append(pairs, "/$HOME", home)
	}
	return strings.TrimSpace(strings.NewReplacer(pairs...).Replace(f.Caveats))
}

func table(w io.Writer, headers []string, rows [][]string) error {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, strings.Join(headers, "\t")); err != nil {
		return err
	}
	for _, row := range rows {
		clean := make([]string, len(row))
		for i, value := range row {
			clean[i] = strings.Join(strings.Fields(cleanText(value)), " ")
		}
		if _, err := fmt.Fprintln(tw, strings.Join(clean, "\t")); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// writeLines prints result lines, styling "==> " titles the way progress
// titles are styled on a color terminal.
func writeLines(w io.Writer, lines []string) error {
	color := colorful(w)
	for _, line := range lines {
		line = cleanText(line)
		if text, ok := strings.CutPrefix(line, "==> "); ok && color {
			line = "\033[34;1m==>\033[0m \033[1m" + text + "\033[0m"
		} else if text, ok := strings.CutPrefix(line, "Warning: "); ok && color {
			line = "\033[4;33mWarning\033[0m: " + text
		}
		if _, err := fmt.Fprintln(w, line); err != nil {
			return err
		}
	}
	return nil
}

func cleanText(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, s)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}
