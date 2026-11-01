package cli

import (
	"fmt"
	"io"
	"slices"
	"strings"
	"text/tabwriter"
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
	if err := table(w, []string{"FORMULA", "VERSION", "BOTTLE", "REASON"}, rows); err != nil {
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
		r := records[name]
		if !r.Requested {
			continue
		}
		var caveats []string
		if text := strings.TrimSpace(r.Formula.Caveats); text != "" {
			caveats = append(caveats, text)
		}
		if text := shadowText(e.Prefix(), r); text != "" {
			caveats = append(caveats, text)
		}
		if len(caveats) > 0 {
			lines = append(append(lines, "==> Caveats"), caveats...)
		}
	}
	for _, name := range result.Historic {
		version := ""
		if r, ok := records[name]; ok {
			version = " " + r.Formula.PkgVersion()
		}
		fmt.Fprintln(a.stderr, cleanText("Warning: "+name+version+" is not covered by Homebrew's signed metadata; its bottle was verified against GHCR and homebrew-core history.\n"+
			"gobrew upgrade will replace it with the current version. To keep it: gobrew pin "+name))
	}
	// Homebrew warns only about the formulae the user named.
	for _, name := range result.Unchanged {
		r, ok := records[name]
		if !ok || !slices.Contains(request.Names, name) {
			continue
		}
		version := r.Formula.PkgVersion()
		fmt.Fprintf(a.stderr, "Warning: %s %s is already installed.\nTo reinstall %s, run:\n  gobrew reinstall %s\n", name, version, version, name)
	}
	for _, group := range []struct {
		label string
		names []string
	}{{"Needs repair", result.NeedsRepair}, {"Not executed", result.NotExecuted}} {
		if len(group.names) > 0 {
			lines = append(lines, group.label+": "+strings.Join(group.names, ", "))
		}
	}
	lines = append(lines, notes...)
	for _, line := range lines {
		if _, err := fmt.Fprintln(w, cleanText(line)); err != nil {
			return err
		}
	}
	return nil
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
