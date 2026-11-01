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
	"time"

	"github.com/shqingda/gobrew/internal/engine"
	"github.com/shqingda/gobrew/internal/model"
	"github.com/spf13/cobra"
)

func (a *application) installCommand(action string) *cobra.Command {
	var dryRun bool
	args, use := cobra.MinimumNArgs(1), action+" <formula>..."
	if action == "upgrade" {
		args, use = cobra.ArbitraryArgs, "upgrade [formula...]"
	}
	cmd := a.commandFor(use, strings.ToUpper(action[:1])+action[1:]+" official core bottles and their dependencies", args, func(ctx context.Context, names []string) error {
		return a.install(ctx, model.Request{Action: action, Names: names}, dryRun)
	})
	cmd.Flags().BoolVarP(&dryRun, "dry-run", "n", false, "Resolve and show the exact plan without installing")
	if action != "reinstall" {
		// gobrew never asks for confirmation; accepted for brew scripts.
		cmd.Flags().BoolP("no-ask", "y", false, "Do not ask for confirmation (gobrew never asks)")
	}
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
	a.setResult(result, func(w io.Writer) error { return a.renderInstall(w, e, request, result, notes) })
	if action == "upgrade" {
		return errors.Join(err, a.pinnedUpgrade(e, result.Held, len(request.Names) > 0))
	}
	return errors.Join(err, heldError(e, result.Held))
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
	fmt.Fprintln(a.stderr, "Warning: "+message)
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
		if _, err := fmt.Fprintf(w, "==> Autoremoved %d unused %s:\n", len(unused), label); err != nil {
			return err
		}
		for _, name := range unused {
			if _, err := fmt.Fprintf(w, "Uninstalled %s\n", cleanText(name)); err != nil {
				return err
			}
		}
		return nil
	})
	return nil
}

func (a *application) linkCommand(linked bool) *cobra.Command {
	var force bool
	name, description := "link", "Link an installed formula into the shared prefix"
	if !linked {
		name, description = "unlink", "Remove only gobrew-owned formula links"
	}
	cmd := a.commandFor(name+" <formula>...", description, cobra.MinimumNArgs(1), func(ctx context.Context, names []string) error {
		e, err := a.getEngine()
		if err != nil {
			return err
		}
		if err = e.Link(ctx, names, linked, force); err != nil {
			return err
		}
		a.names(names, "")
		return nil
	})
	if linked {
		cmd.Flags().BoolVar(&force, "force", false, "Allow linking keg-only formulae; never overwrite foreign files")
	}
	return cmd
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

func (a *application) importHomebrewCommand() *cobra.Command {
	var from string
	var dryRun bool
	// brew migrate moves renamed formulae; this gobrew-only import has its own name.
	cmd := a.commandFor("import-homebrew", "Read requested Homebrew formulae and reinstall them in gobrew", cobra.NoArgs, func(ctx context.Context, _ []string) error {
		e, err := a.getEngine()
		if err != nil {
			return err
		}
		if !filepath.IsAbs(from) {
			return usageError{errors.New("--from-homebrew must be an absolute path")}
		}
		migration, err := e.Migration(from)
		if err != nil {
			return err
		}
		if len(migration.Names) == 0 {
			a.message(migration, "No requested core formulae to migrate.")
			return nil
		}
		request := model.Request{Action: "install", Names: migration.Names}
		if dryRun {
			plan, err := e.Plan(ctx, request)
			if err != nil {
				return err
			}
			a.setResult(struct {
				Plan    model.Plan `json:"plan"`
				Skipped []string   `json:"skipped"`
			}{plan, migration.Skipped}, func(w io.Writer) error {
				if err := renderPlan(w, plan); err != nil {
					return err
				}
				return renderSkipped(w, migration.Skipped)
			})
			return nil
		}
		if err = a.ensureInitialized(ctx, e); err != nil {
			return err
		}
		var result model.Result
		err = a.withEvents(func(events chan<- model.Event) error {
			var err error
			result, err = e.Execute(ctx, request, events)
			return err
		})
		notes := a.afterInstall(ctx, e, result, err)
		a.setResult(struct {
			Result  model.Result `json:"result"`
			Skipped []string     `json:"skipped"`
		}{result, migration.Skipped}, func(w io.Writer) error {
			if err := a.renderInstall(w, e, request, result, notes); err != nil {
				return err
			}
			return renderSkipped(w, migration.Skipped)
		})
		return err
	})
	cmd.Flags().StringVar(&from, "from-homebrew", "/opt/homebrew", "Existing Homebrew prefix to inspect read-only")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show the migration plan without installing")
	return cmd
}

func (a *application) withEvents(run func(chan<- model.Event) error) error {
	if a.json || a.quiet {
		return run(nil)
	}
	events := make(chan model.Event, 64)
	done := make(chan error, 1)
	go func() {
		p := newProgress(a.stderr)
		var writeErr error
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for open := true; open; {
			select {
			case event, ok := <-events:
				if open = ok; ok && writeErr == nil {
					writeErr = p.event(event)
				}
			case <-ticker.C:
				if writeErr == nil {
					writeErr = p.tick()
				}
			}
		}
		done <- errors.Join(writeErr, p.clear())
	}()
	err := run(events)
	close(events)
	return errors.Join(err, <-done)
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
