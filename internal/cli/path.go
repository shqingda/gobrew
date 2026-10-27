package cli

import (
	"cmp"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/shqingda/gobrew/internal/engine"
	"github.com/shqingda/gobrew/internal/model"
	"github.com/shqingda/gobrew/internal/selfupdate"
	"github.com/shqingda/gobrew/internal/shellconfig"
)

// pathEnv lists what new shells need: the managed command directory, then the
// default Formula prefix. Custom prefixes are never written to startup files,
// so development and test prefixes stay out of the user's shell.
func (a *application) pathEnv(binDir, prefix string) shellconfig.Env {
	var env shellconfig.Env
	if binDir != "" {
		env.Paths = append(env.Paths, binDir)
	}
	if prefix == a.systemPrefix {
		env.Paths = append(env.Paths, filepath.Join(prefix, "bin"), filepath.Join(prefix, "sbin"))
		env.Prefix = prefix
	}
	return env
}

// setupPath adds gobrew to new shells and returns what the user should know.
// It does nothing when the running shell already has every directory, so the
// installed no-op path performs no file access. GOBREW_NO_MODIFY_PATH opts out.
func (a *application) setupPath(env shellconfig.Env) string {
	if len(env.Paths) == 0 || onPath(env.Paths...) {
		return ""
	}
	manual := "Add it to your shell startup file:\n  eval \"$(gobrew shellenv)\""
	if os.Getenv("GOBREW_NO_MODIFY_PATH") != "" {
		return "gobrew is not on PATH. " + manual
	}
	profile, err := a.profile()
	if err != nil {
		return "Could not set up PATH automatically: " + err.Error() + ". " + manual
	}
	changed, err := profile.Ensure(env)
	if err != nil {
		return "Could not update " + tildePath(profile.Path) + ": " + err.Error() + ". " + manual
	}
	note := ""
	if changed {
		note = "Added gobrew to PATH in " + tildePath(profile.Path) + ".\n"
	}
	return note + "Open a new terminal, or run: exec " + profile.Shell
}

func (a *application) removePath() string {
	profile, err := a.profile()
	if err != nil {
		return ""
	}
	removed, err := profile.Remove()
	if err != nil {
		return "Could not remove the gobrew PATH block from " + tildePath(profile.Path) + ": " + err.Error()
	}
	if removed {
		return "Removed the gobrew PATH block from " + tildePath(profile.Path) + "."
	}
	return ""
}

func (a *application) profile() (shellconfig.Profile, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return shellconfig.Profile{}, err
	}
	return shellconfig.Detect(home, os.Getenv("SHELL"), os.Getenv)
}

// managedBinDir is the self-installed command directory, or "" for source
// builds and other unmanaged executables.
func managedBinDir() string {
	m, err := selfupdate.Running()
	if err != nil {
		return ""
	}
	return m.BinDir
}

func onPath(dirs ...string) bool {
	entries := filepath.SplitList(os.Getenv("PATH"))
	for _, dir := range dirs {
		if !slices.Contains(entries, dir) {
			return false
		}
	}
	return true
}

func tildePath(path string) string {
	if home, err := os.UserHomeDir(); err == nil {
		if rel, ok := strings.CutPrefix(path, home+string(filepath.Separator)); ok {
			return "~/" + rel
		}
	}
	return path
}

func homebrewPrefix() string { return cmp.Or(os.Getenv("HOMEBREW_PREFIX"), "/opt/homebrew") }

// duplicate is a gobrew command that Homebrew also provides.
type duplicate struct {
	Package  string // gobrew formula
	Command  string
	Homebrew string // Homebrew formula owning the command, if its link says so
}

// homebrewDuplicates only reads Homebrew's bin links; gobrew never changes them.
func homebrewDuplicates(records map[string]model.Record, names []string) []duplicate {
	brew := homebrewPrefix()
	var found []duplicate
	for _, name := range names {
		for rel := range records[name].Links {
			command, ok := strings.CutPrefix(rel, "bin/")
			if !ok || strings.Contains(command, "/") {
				continue
			}
			path := filepath.Join(brew, "bin", command)
			if _, err := os.Lstat(path); err != nil {
				continue
			}
			formula := ""
			if target, err := os.Readlink(path); err == nil {
				if _, after, ok := strings.Cut(target, "Cellar/"); ok {
					formula, _, _ = strings.Cut(after, "/")
				}
			}
			found = append(found, duplicate{name, command, formula})
		}
	}
	slices.SortFunc(found, func(a, b duplicate) int { return strings.Compare(a.Command, b.Command) })
	return found
}

func duplicateWarnings(prefix string, dups []duplicate) []string {
	var lines []string
	formulae := map[string]bool{}
	for _, d := range dups {
		lines = append(lines, fmt.Sprintf("Warning: Homebrew also provides %s (%s). New shells run %s first.", d.Command, filepath.Join(homebrewPrefix(), "bin", d.Command), filepath.Join(prefix, "bin", d.Command)))
		if d.Homebrew != "" {
			formulae[d.Homebrew] = true
		}
	}
	if len(formulae) > 0 {
		names := slices.Sorted(func(yield func(string) bool) {
			for name := range formulae {
				if !yield(name) {
					return
				}
			}
		})
		lines = append(lines, "To keep only gobrew's copy: brew uninstall "+strings.Join(names, " "))
	}
	return lines
}

// pathFindings are environment warnings for doctor. They do not make the
// installation unhealthy, so they never change doctor's exit status.
func (a *application) pathFindings(e *engine.Engine, records map[string]model.Record) []engine.Finding {
	var findings []engine.Finding
	warn := func(pkg, problem string) {
		findings = append(findings, engine.Finding{Package: pkg, Problem: problem, Severity: "warning"})
	}
	bin := filepath.Join(e.Prefix(), "bin")
	entries := filepath.SplitList(os.Getenv("PATH"))
	ours := slices.Index(entries, bin)
	switch brew := slices.Index(entries, filepath.Join(homebrewPrefix(), "bin")); {
	case ours < 0:
		warn("", bin+" is not on PATH; open a new terminal or run gobrew install to set it up")
	case brew >= 0 && brew < ours:
		warn("", "Homebrew's bin precedes "+bin+" on PATH; same-named commands run Homebrew's copy")
	}
	names := make([]string, 0, len(records))
	for name := range records {
		names = append(names, name)
	}
	for _, d := range homebrewDuplicates(records, names) {
		problem := "command " + d.Command + " is also installed by Homebrew"
		if d.Homebrew != "" {
			problem += " (" + d.Homebrew + ")"
		}
		if resolved, err := exec.LookPath(d.Command); err == nil {
			problem += "; this shell runs " + resolved
		}
		warn(d.Package, problem)
	}
	return findings
}

// kegSummary matches Homebrew's "<keg>: N files, SIZE" line.
func kegSummary(keg string) string {
	files, size := 0, int64(0)
	_ = filepath.WalkDir(keg, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil || !entry.Type().IsRegular() {
			return nil
		}
		if info, err := entry.Info(); err == nil {
			files, size = files+1, size+info.Size()
		}
		return nil
	})
	return fmt.Sprintf("%s: %d files, %s", keg, files, humanBytes(size))
}
