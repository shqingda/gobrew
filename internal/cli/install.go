package cli

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/shqingda/gobrew/internal/engine"
	"github.com/shqingda/gobrew/internal/model"
	"github.com/spf13/cobra"
)

func (a *application) installCommand(action string) *cobra.Command {
	var dryRun, displayTimes, asDependency, onlyDependencies, skipLink, skipPostInstall, force, includeTest bool
	var minimum, minimumAlias string
	args, use := cobra.MinimumNArgs(1), action+" <formula>..."
	if action == "upgrade" {
		args, use = cobra.ArbitraryArgs, "upgrade [formula...]"
	}
	cmd := a.commandFor(use, strings.ToUpper(action[:1])+action[1:]+" official core bottles and their dependencies", args, func(ctx context.Context, names []string) error {
		if displayTimes || a.verbose {
			a.times = &installTimes{}
		}
		if a.minimumVersion = cmp.Or(minimum, minimumAlias); a.minimumVersion != "" && len(names) != 1 {
			return usageError{errors.New("`--minimum-version` requires exactly one formula argument")}
		}
		return a.install(ctx, model.Request{Action: action, Names: names, AsDependency: asDependency, OnlyDependencies: onlyDependencies, SkipLink: skipLink, SkipPostInstall: skipPostInstall, Force: force, IncludeTest: includeTest}, dryRun)
	})
	cmd.Flags().BoolVarP(&dryRun, "dry-run", "n", false, "Resolve and show the exact plan without installing")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "Install disabled formulae with a warning instead of refusing them")
	cmd.Flags().BoolVar(&displayTimes, "display-times", os.Getenv("GOBREW_DISPLAY_INSTALL_TIMES") != "", "Print install times for each package at the end of the run")
	if action == "install" {
		cmd.Flags().BoolVar(&asDependency, "as-dependency", false, "Install but mark as a dependency, not installed on request")
		cmd.Flags().BoolVar(&onlyDependencies, "only-dependencies", false, "Install the dependencies but not the formula itself")
		cmd.Flags().BoolVar(&skipLink, "skip-link", false, "Install but skip linking the keg into the prefix")
		cmd.Flags().BoolVar(&skipPostInstall, "skip-post-install", false, "Install but skip any post-install steps")
		cmd.Flags().BoolVar(&includeTest, "include-test", false, "Also install the test dependencies of the named formulae")
	}
	if action == "upgrade" {
		cmd.Flags().StringVar(&minimum, "minimum-version", "", "Only upgrade a named formula if its installed version is below the given minimum version")
		cmd.Flags().StringVar(&minimumAlias, "min-version", "", "Alias of --minimum-version")
		cmd.MarkFlagsMutuallyExclusive("minimum-version", "min-version")
	}
	// gobrew never asks for confirmation and only pours bottles; these are
	// accepted so brew scripts keep working.
	cmd.Flags().BoolP("no-ask", "y", false, "Do not ask for confirmation (gobrew never asks)")
	cmd.Flags().Bool("yes", false, "Alias of --no-ask")
	cmd.Flags().Bool("force-bottle", false, "Install from a bottle (gobrew always does)")
	cmd.Example = map[string]string{
		"install":   "  gobrew install jq\n  gobrew install --dry-run node",
		"reinstall": "  gobrew reinstall jq",
		"upgrade":   "  gobrew upgrade\n  gobrew upgrade jq --dry-run",
	}[action]
	return cmd
}

// versionInstallCommand mirrors brew version-install formula[@version] [version].
// Homebrew extracts the version into a personal tap; gobrew verifies the bottle
// against GHCR and homebrew-core instead. Only this command takes versions:
// like brew install, plain install never splits names at "@".
func (a *application) versionInstallCommand() *cobra.Command {
	var dryRun bool
	cmd := a.commandFor("version-install <formula[@version]> [version]", "Install a specific version of a formula", cobra.RangeArgs(1, 2), func(ctx context.Context, args []string) error {
		name, version := args[0], ""
		if len(args) == 2 {
			version = args[1]
		} else if i := strings.LastIndex(name, "@"); i > 0 {
			name, version = name[:i], name[i+1:]
		}
		name = strings.TrimPrefix(name, "homebrew/core/")
		if version == "" {
			return usageError{errors.New("specify a version: gobrew version-install " + name + " <version>")}
		}
		return a.install(ctx, model.Request{Action: "install", Names: []string{name}, Versions: map[string]string{name: version}}, dryRun)
	})
	cmd.Flags().BoolVarP(&dryRun, "dry-run", "n", false, "Resolve and show the exact plan without installing")
	cmd.Example = "  gobrew version-install uv 0.13.5\n  gobrew version-install uv@0.13.5"
	return cmd
}

func (a *application) install(ctx context.Context, request model.Request, dryRun bool) error {
	action := request.Action
	e, err := a.getEngine()
	if err != nil {
		return err
	}
	// Planning may fetch a missing catalog, but must not run automatic updates
	// (which also replace the managed CLI). Reinstall and version-install do not
	// automatically update either.
	if !dryRun && len(request.Versions) == 0 && (action == "install" || action == "upgrade") {
		if err = a.autoUpdate(ctx, e, action == "install" || len(request.Names) > 0); err != nil {
			return err
		}
	}
	if action == "install" {
		request.NoInstallUpgrade = os.Getenv("GOBREW_NO_INSTALL_UPGRADE") != ""
	}
	if action == "upgrade" && a.minimumVersion != "" {
		skip, err := a.notBelowMinimum(ctx, e, request.Names[0])
		if err != nil {
			return err
		}
		if skip {
			// Like brew, the warning is all a skipped run prints.
			var data any = model.Result{}
			if dryRun {
				data = model.Plan{Request: request}
			}
			a.setResult(data, func(io.Writer) error { return nil })
			return nil
		}
	}
	if dryRun {
		plan, err := e.Plan(ctx, request)
		if err != nil {
			return err
		}
		a.setResult(plan, func(w io.Writer) error { return renderPlan(w, plan) })
		return nil
	}
	if action == "install" {
		if err = a.ensureInitialized(ctx, e); err != nil {
			return err
		}
	}
	var result model.Result
	err = a.withEvents(func(events chan<- model.Event) error {
		var err error
		result, err = e.Execute(ctx, request, events)
		return err
	})
	notes := a.afterInstall(ctx, e, result, err)
	// The embedded Result keeps its JSON fields where they were; notes adds
	// the PATH setup and cleanup messages text output shows.
	data := struct {
		model.Result
		Notes []string `json:"notes,omitempty"`
	}{result, notes}
	a.setResult(data, func(w io.Writer) error { return a.renderInstall(w, e, request, result, notes) })
	if action == "upgrade" {
		return errors.Join(err, a.pinnedUpgrade(e, result.Held, len(request.Names) > 0))
	}
	return errors.Join(err, heldError(e, result.Held))
}

// notBelowMinimum is brew upgrade --minimum-version: an outdated formula whose
// installed version is not below the minimum stays as it is, with a warning.
// Every other case upgrades as usual.
func (a *application) notBelowMinimum(ctx context.Context, e *engine.Engine, name string) (bool, error) {
	name = strings.TrimPrefix(name, "homebrew/core/")
	outdated, err := e.Outdated(ctx)
	if err != nil || !slices.ContainsFunc(outdated, func(o engine.Outdated) bool { return o.Name == name }) {
		return false, err
	}
	below, err := e.OutdatedBelow(name, a.minimumVersion)
	if err != nil || len(below) > 0 {
		return false, err
	}
	if !a.quiet {
		a.warn("Not upgrading " + name + ", the installed version is not below the minimum version " + a.minimumVersion)
	}
	return true, nil
}

// pinnedUpgrade reports, like brew upgrade, outdated pinned formulae that were
// skipped. Only formulae named explicitly make the command fail.
func (a *application) pinnedUpgrade(e *engine.Engine, held []string, named bool) error {
	if len(held) == 0 {
		return nil
	}
	snapshot, err := e.Installed()
	if err != nil {
		return err
	}
	versions := make([]string, 0, len(held))
	for _, name := range held {
		versions = append(versions, name+" "+snapshot.Records[name].Formula.PkgVersion())
	}
	noun := "package"
	if len(held) > 1 {
		noun = "packages"
	}
	message := fmt.Sprintf("Not upgrading %d pinned %s:\n%s", len(held), noun, strings.Join(versions, ", "))
	if named {
		return heldUpgrade(message)
	}
	a.warn(message)
	return nil
}

func (a *application) uninstall(ctx context.Context, names []string) error {
	e, err := a.getEngine()
	if err != nil {
		return err
	}
	removed, err := e.Uninstall(ctx, names)
	if err != nil {
		return err
	}
	a.setResult(removed, func(w io.Writer) error {
		var unused []string
		for _, name := range removed {
			if !slices.Contains(names, name) {
				unused = append(unused, name)
				continue
			}
			if _, err := fmt.Fprintf(w, "Uninstalled %s\n", cleanText(name)); err != nil {
				return err
			}
		}
		if len(unused) == 0 {
			return nil
		}
		label := "dependencies"
		if len(unused) == 1 {
			label = "dependency"
		}
		lines := []string{fmt.Sprintf("==> Autoremoved %d unused %s:", len(unused), label)}
		for _, name := range unused {
			lines = append(lines, "Uninstalled "+name)
		}
		if err := writeLines(w, lines); err != nil {
			return err
		}
		return nil
	})
	return nil
}

func (a *application) linkCommand(linked bool) *cobra.Command {
	var force, dryRun bool
	name, description := "link", "Link an installed formula into the shared prefix"
	if !linked {
		name, description = "unlink", "Remove only gobrew-owned formula links"
	}
	cmd := a.commandFor(name+" <formula>...", description, cobra.MinimumNArgs(1), func(ctx context.Context, names []string) error {
		e, err := a.getEngine()
		if err != nil {
			return err
		}
		if dryRun {
			results, err := e.PreviewLink(names, linked)
			if err != nil {
				return err
			}
			a.setResult(results, func(w io.Writer) error { return a.renderLinks(w, e.Prefix(), results, linked, true) })
			return nil
		}
		results, err := e.Link(ctx, names, linked, force)
		if err != nil {
			return err
		}
		done := make([]string, 0, len(results))
		for _, r := range results {
			if !r.NeedsForce {
				done = append(done, r.Name)
			}
		}
		a.setResult(done, func(w io.Writer) error { return a.renderLinks(w, e.Prefix(), results, linked, false) })
		return nil
	})
	if linked {
		cmd.Flags().BoolVarP(&force, "force", "f", false, "Allow linking keg-only formulae; never overwrite foreign files")
	}
	help := "List the links that would be removed without changing anything"
	if linked {
		help = "List the links that would be created without changing anything"
	}
	cmd.Flags().BoolVarP(&dryRun, "dry-run", "n", false, help)
	return cmd
}

// renderLinks follows brew's link and unlink output. An already linked keg, or
// a keg-only one without --force, is a warning with exit status 0.
func (a *application) renderLinks(w io.Writer, prefix string, results []model.LinkResult, linked, dryRun bool) error {
	for _, r := range results {
		var lines []string
		switch {
		case r.AlreadyLinked:
			force := ""
			if r.KegOnly {
				force = "--force "
			}
			a.warn("Already linked: " + r.Keg + "\nTo relink, run:\n  gobrew unlink " + r.Name + " && gobrew link " + force + r.Name)
		case r.NeedsForce:
			a.warn(r.Name + " is keg-only and must be linked with `--force`.")
		case dryRun && linked:
			lines = append([]string{"Would link:"}, r.Paths...)
		case dryRun:
			lines = append([]string{"Would remove:"}, r.Paths...)
		case linked:
			lines = []string{fmt.Sprintf("Linking %s... %d symlinks created.", r.Keg, len(r.Paths))}
		default:
			lines = []string{fmt.Sprintf("Unlinking %s... %d symlinks removed.", r.Keg, len(r.Paths))}
		}
		if r.KegOnly && !r.AlreadyLinked {
			lines = append(lines, a.pathHint("If you need to have this software first in your PATH instead consider running:", filepath.Join(prefix, "opt", r.Name), r.Keg)...)
		}
		for _, line := range lines {
			if _, err := fmt.Fprintln(w, cleanText(line)); err != nil {
				return err
			}
		}
	}
	return nil
}

func (a *application) postinstall(ctx context.Context, names []string) error {
	e, err := a.getEngine()
	if err != nil {
		return err
	}
	err = a.withEvents(func(events chan<- model.Event) error { return e.PostInstall(ctx, names, events) })
	if err != nil {
		return err
	}
	a.names(names, "")
	return nil
}

// ensureCatalog downloads a missing catalog with visible progress, so the
// first command that needs one does not sit silent for the download.
func (a *application) ensureCatalog(ctx context.Context, e *engine.Engine) error {
	return a.withEvents(func(events chan<- model.Event) error { return e.EnsureCatalog(ctx, events) })
}

func (a *application) withEvents(run func(chan<- model.Event) error) error {
	show := !a.json && !a.quiet
	if !show && a.times == nil {
		return run(nil)
	}
	events := make(chan model.Event, 64)
	done := make(chan error, 1)
	go func() {
		var p *progress
		if show {
			p = newProgress(a.stderr)
		}
		var writeErr error
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for open := true; open; {
			select {
			case event, ok := <-events:
				if open = ok; ok {
					a.times.observe(event, time.Now())
				}
				if ok && p != nil && writeErr == nil {
					writeErr = p.event(event)
				}
			case <-ticker.C:
				if p != nil && writeErr == nil {
					writeErr = p.tick()
				}
			}
		}
		if p != nil {
			writeErr = errors.Join(writeErr, p.clear())
		}
		done <- writeErr
	}()
	err := run(events)
	close(events)
	return errors.Join(err, <-done)
}

// installTimes follows brew's --display-times. Packages prepare in parallel, so
// each time runs from the package's first progress event until it is installed
// and can overlap the others.
type installTimes struct {
	started map[string]time.Time
	order   []string
	took    map[string]time.Duration
}

func (t *installTimes) observe(event model.Event, now time.Time) {
	if t == nil || event.Package == "" {
		return
	}
	if t.started == nil {
		t.started, t.took = map[string]time.Time{}, map[string]time.Duration{}
	}
	if _, ok := t.started[event.Package]; !ok {
		t.started[event.Package] = now
	}
	if _, done := t.took[event.Package]; event.Phase == "installed" && !done {
		t.took[event.Package] = now.Sub(t.started[event.Package])
		t.order = append(t.order, event.Package)
	}
}

func (t *installTimes) lines() []string {
	if t == nil || len(t.order) == 0 {
		return nil
	}
	lines := []string{"==> Installation times"}
	for _, name := range t.order {
		lines = append(lines, fmt.Sprintf("%-20s %10.3f s", name, t.took[name].Seconds()))
	}
	return lines
}

// afterInstall gathers what to tell the user once packages are in place:
// Homebrew duplicates and, for the default prefix, PATH setup for new shells.
// It runs in JSON mode too, so a GUI caller gets the same PATH setup.
func (a *application) afterInstall(ctx context.Context, e *engine.Engine, result model.Result, err error) []string {
	if err != nil && len(result.Succeeded) == 0 {
		return nil
	}
	notes := a.installCleanup(ctx, e, result)
	if e.Prefix() == a.systemPrefix {
		if note := a.setupPath(a.pathEnv(managedBinDir(), e.Prefix())); note != "" {
			notes = append(notes, note)
		}
	}
	return notes
}

// heldError reports, like brew install, requested formulae that are installed
// and outdated but were not upgraded. The command then exits non-zero.
func heldError(e *engine.Engine, held []string) error {
	if len(held) == 0 {
		return nil
	}
	snapshot, err := e.Installed()
	if err != nil {
		return err
	}
	available, err := e.Available(held)
	if err != nil {
		return err
	}
	var messages []string
	for _, name := range held {
		r := snapshot.Records[name]
		unpin := ""
		if r.Pinned {
			unpin = "gobrew unpin " + name + " && "
		}
		messages = append(messages, fmt.Sprintf("%s %s is already installed.\nTo upgrade to %s, run:\n  %sgobrew upgrade %s", name, r.Formula.PkgVersion(), available[name].PkgVersion(), unpin, name))
	}
	return heldUpgrade(strings.Join(messages, "\n"))
}
