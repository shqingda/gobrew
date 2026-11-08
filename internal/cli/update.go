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
	"strconv"
	"strings"
	"time"

	"github.com/shqingda/gobrew/internal/engine"
	"github.com/shqingda/gobrew/internal/selfupdate"
	"github.com/spf13/cobra"
)

// cliUpdate reports the self-update half of update.
type cliUpdate struct {
	selfupdate.Result
	Skipped string `json:"skipped,omitempty"`
	Error   string `json:"error,omitempty"`
	// Installed and Note report an installation requested by install.sh.
	Installed bool   `json:"installed,omitempty"`
	Note      string `json:"note,omitempty"`
}

type updateReport struct {
	Generation string            `json:"generation"`
	Formulae   int               `json:"formulae"`
	CLI        cliUpdate         `json:"cli"`
	Outdated   []engine.Outdated `json:"outdated"`
}

// runUpdate follows brew update: update the CLI itself, then refresh the
// formula catalog, then compute what upgrade would change. A failed self
// update does not stop the catalog refresh; it is returned separately.
func (a *application) runUpdate(ctx context.Context, e *engine.Engine) (updateReport, error, error) {
	cli, cliErr := a.updateCLI(ctx)
	if cliErr != nil {
		cli.Error = cliErr.Error()
	}
	catalog, err := e.Update(ctx)
	if err != nil {
		return updateReport{CLI: cli}, cliErr, err
	}
	// The catalog is fresh, so this compares local records only.
	outdated, err := e.Outdated(ctx)
	return updateReport{catalog.Generation, len(catalog.Formulae), cli, outdated}, cliErr, err
}

func outdatedLines(outdated []engine.Outdated) []string {
	if len(outdated) == 0 {
		return nil
	}
	lines := []string{"==> Outdated Formulae"}
	for _, item := range outdated {
		line := item.Name + " (" + item.Installed + " -> " + item.Available + ")"
		if item.Pinned {
			line += " [pinned]"
		}
		lines = append(lines, line)
	}
	noun := "formulae"
	if len(outdated) == 1 {
		noun = "formula"
	}
	return append(lines, fmt.Sprintf("You have %d outdated %s installed. Upgrade with: gobrew upgrade", len(outdated), noun))
}

func (a *application) update(ctx context.Context, _ []string) error {
	// install.sh hands over here to install the verified executable it
	// downloaded, which works with every release that install.sh can select.
	if dir := os.Getenv("GOBREW_INSTALL_BIN_DIR"); dir != "" {
		cli, err := a.installCLI(ctx, dir)
		if err != nil {
			return err
		}
		a.setResult(cli, func(w io.Writer) error { return writeInstalled(w, cli) })
		return nil
	}
	e, err := a.getEngine()
	if err != nil {
		return err
	}
	report, cliErr, err := a.runUpdate(ctx, e)
	if err != nil {
		return errors.Join(cliErr, err)
	}
	a.setResult(report, func(w io.Writer) error {
		var lines []string
		switch cli := report.CLI; {
		case cli.Skipped != "":
			lines = append(lines, "Skipped updating gobrew: "+cli.Skipped)
		case cli.Error != "":
			lines = append(lines, "Could not update gobrew: "+cli.Error)
		case cli.Changed:
			lines = append(lines, "==> Updated gobrew to "+cli.Version+"; the new version runs from the next command.")
		default:
			lines = append(lines, "gobrew "+cli.Version+" is already up to date.")
		}
		lines = append(lines, fmt.Sprintf("Updated formula catalog: %d formulae.", report.Formulae))
		lines = append(lines, outdatedLines(report.Outdated)...)
		for _, line := range lines {
			if _, err := fmt.Fprintln(w, cleanText(line)); err != nil {
				return err
			}
		}
		return nil
	})
	return cliErr
}

// autoUpdate mirrors Homebrew's auto-update before install, outdated and
// upgrade: when the catalog is older than GOBREW_AUTO_UPDATE_SECS (default 24
// hours) it runs update first. Failures only warn; the command continues.
// listOutdated is false for zero-argument upgrade and outdated, which list
// outdated formulae themselves.
func (a *application) autoUpdate(ctx context.Context, e *engine.Engine, listOutdated bool) error {
	if os.Getenv("GOBREW_NO_AUTO_UPDATE") != "" {
		return nil
	}
	interval := 24 * time.Hour
	if value := os.Getenv("GOBREW_AUTO_UPDATE_SECS"); value != "" {
		if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds >= 0 {
			interval = time.Duration(seconds) * time.Second
		}
	}
	if fetched, err := e.CatalogFetched(); err == nil && time.Since(fetched) < interval {
		return nil
	}
	text := !a.json && !a.quiet
	if text {
		_ = newProgress(a.stderr).title("Auto-updating gobrew...")
		if os.Getenv("GOBREW_NO_ENV_HINTS") == "" && os.Getenv("GOBREW_AUTO_UPDATE_SECS") == "" {
			fmt.Fprintln(a.stderr, "Adjust how often this is run with `$GOBREW_AUTO_UPDATE_SECS` or disable with")
			fmt.Fprintln(a.stderr, "`$GOBREW_NO_AUTO_UPDATE=1`. Hide these hints with `$GOBREW_NO_ENV_HINTS=1` (see `gobrew help`).")
		}
	}
	report, cliErr, err := a.runUpdate(ctx, e)
	if cause := ctx.Err(); cause != nil {
		return cause
	}
	if !text {
		return nil
	}
	if err = errors.Join(cliErr, err); err != nil {
		fmt.Fprintln(a.stderr, "Warning: auto-update failed: "+cleanText(err.Error()))
		return nil
	}
	if report.CLI.Changed {
		_ = newProgress(a.stderr).title("Updated gobrew to " + report.CLI.Version + "; the new version runs from the next command.")
	}
	if listOutdated {
		for _, line := range outdatedLines(report.Outdated) {
			fmt.Fprintln(a.stderr, cleanText(line))
		}
	}
	return nil
}

// updateCLI skips builds that cannot be updated in place instead of failing:
// source builds have no release version and unmanaged binaries no receipt.
func (a *application) updateCLI(ctx context.Context) (cliUpdate, error) {
	if !versionPattern.MatchString(a.build.Version) {
		return cliUpdate{Skipped: "development build " + a.build.Version + " has no release version"}, nil
	}
	manager, err := selfupdate.Running()
	if err != nil {
		return cliUpdate{Skipped: "this executable is not a managed installation; run 'gobrew self install' first"}, nil
	}
	result, err := manager.Update(ctx)
	return cliUpdate{Result: result}, err
}

// installCLI installs the running executable and the gob alias into binDir
// and adds it to new shells.
func (a *application) installCLI(ctx context.Context, binDir string) (cliUpdate, error) {
	if !filepath.IsAbs(binDir) {
		return cliUpdate{}, usageError{errors.New("the command directory must be an absolute path")}
	}
	binDir = filepath.Clean(binDir)
	source, err := os.Executable()
	if err == nil {
		source, err = filepath.EvalSymlinks(source)
	}
	if err != nil {
		return cliUpdate{}, err
	}
	result, err := (selfupdate.Manager{BinDir: binDir}).Install(ctx, source, a.build.Version)
	if err != nil {
		return cliUpdate{Result: result}, err
	}
	return cliUpdate{Result: result, Installed: true, Note: a.setupPath(a.pathEnv(binDir, filepath.Clean(a.config.Prefix)))}, nil
}

// outdatedCommand mirrors brew outdated [--verbose] [formula...].
func (a *application) outdatedCommand() *cobra.Command {
	var verbose bool
	var minimum, minimumAlias string
	cmd := a.commandFor("outdated [formula...]", "List installed formulae with available updates", cobra.ArbitraryArgs, func(ctx context.Context, names []string) error {
		minimum = cmp.Or(minimum, minimumAlias)
		if minimum != "" && len(names) != 1 {
			return usageError{errors.New("`--minimum-version` requires exactly one formula argument")}
		}
		return a.outdated(ctx, names, verbose, minimum)
	})
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "Include detailed version information")
	cmd.Flags().StringVar(&minimum, "minimum-version", "", "Only list a named formula with an installed version below the given minimum version")
	cmd.Flags().StringVar(&minimumAlias, "min-version", "", "Alias of --minimum-version")
	return cmd
}

func (a *application) outdated(ctx context.Context, names []string, verbose bool, minimum string) error {
	e, err := a.getEngine()
	if err != nil {
		return err
	}
	if err = a.autoUpdate(ctx, e, false); err != nil {
		return err
	}
	var items []engine.Outdated
	if minimum == "" {
		items, err = e.Outdated(ctx)
		if err != nil {
			return err
		}
	}
	if len(names) > 0 {
		snapshot, err := e.Installed()
		if err != nil {
			return err
		}
		for i, name := range names {
			names[i] = strings.TrimPrefix(name, "homebrew/core/")
			if _, ok := snapshot.Records[names[i]]; !ok {
				return notFound("No such keg: " + filepath.Join(e.Prefix(), "Cellar", names[i]))
			}
		}
		items = slices.DeleteFunc(items, func(item engine.Outdated) bool { return !slices.Contains(names, item.Name) })
		if minimum != "" {
			if items, err = e.OutdatedBelow(names[0], minimum); err != nil {
				return err
			}
		}
	}
	// Like brew: versions on a terminal or with --verbose, names otherwise
	// (and always with --quiet).
	a.setResult(items, func(w io.Writer) error {
		verbose := (verbose || newProgress(w).tty) && !a.quiet
		for _, item := range items {
			line := item.Name
			if verbose {
				line += " (" + item.Installed + ") < " + item.Available
				if item.Pinned {
					line += " [pinned at " + item.Installed + "]"
				}
			}
			if _, err := fmt.Fprintln(w, cleanText(line)); err != nil {
				return err
			}
		}
		return nil
	})
	if len(names) > 0 && len(items) > 0 && !a.json {
		return errOutdated
	}
	return nil
}
