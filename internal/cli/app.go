// Package cli adapts command-line arguments and streams to the shared engine.
package cli

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/shqingda/gobrew/internal/engine"
	"github.com/shqingda/gobrew/internal/model"
)

type BuildInfo struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Date    string `json:"date"`
}

type errorInfo struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type envelope struct {
	SchemaVersion int        `json:"schema_version"`
	Command       string     `json:"command"`
	Data          any        `json:"data,omitempty"`
	Error         *errorInfo `json:"error,omitempty"`
}

type usageError struct{ error }

// notFound carries Homebrew's exact message while keeping the not_found code.
type notFound string

// heldUpgrade reports installed requests that a pin or
// GOBREW_NO_INSTALL_UPGRADE kept at their version. It exits 1 like brew
// install, with its own JSON code so scripts can tell it from a failure.
type heldUpgrade string

func (e heldUpgrade) Error() string { return string(e) }

// errOutdated is brew outdated's silent exit status 1 when named formulae are
// outdated; the listing itself is the message.
var errOutdated = errors.New("")

// actionError is a renderer's failure at something other than writing, such
// as opening a browser; it is reported without the "write output" prefix.
type actionError struct{ error }

func (e actionError) Unwrap() error { return e.error }

func (e notFound) Error() string { return string(e) }
func (notFound) Unwrap() error   { return model.ErrNotFound }

// brewAliases is Homebrew's HOMEBREW_INTERNAL_COMMAND_ALIASES for the commands
// gobrew provides. "-S" for search is rewritten in run because cobra command
// names cannot start with a dash.
var brewAliases = map[string][]string{
	"list":        {"ls"},
	"update":      {"up"},
	"link":        {"ln"},
	"install":     {"instal"},
	"uninstall":   {"uninstal", "rm", "remove"},
	"postinstall": {"post_install"},
	"info":        {"abv"},
	"doctor":      {"dr"},
}

// brewFormulaFlag lists the commands whose brew counterparts accept --formula.
var brewFormulaFlag = map[string]bool{
	"deps": true, "info": true, "install": true, "link": true, "list": true, "outdated": true, "pin": true,
	"reinstall": true, "search": true, "uninstall": true, "unlink": true, "unpin": true, "upgrade": true, "uses": true,
}

type application struct {
	config    engine.Config
	configErr error
	engine    *engine.Engine
	build     BuildInfo
	json      bool
	quiet     bool
	started   bool
	command   string
	stderr    io.Writer
	hasResult bool
	result    any
	render    func(io.Writer) error
	// times records brew's --display-times for the current install, if asked.
	times *installTimes
	// minimumVersion is upgrade --minimum-version for its one named formula.
	minimumVersion string
	// systemPrefix is the only prefix the privileged helper may create.
	// Tests point it, terminal and elevate at temporary fakes so that no test
	// runs sudo or touches /opt.
	systemPrefix string
	terminal     func() bool
	elevate      func(ctx context.Context, stderr io.Writer, command []string) error
}

// Run executes one invocation. It never exits the process or installs signal
// handlers; the executable owns those process-wide decisions.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer, build BuildInfo) int {
	config, configErr := engine.Defaults()
	if config.Prefix == "" {
		config.Prefix = engine.SystemPrefix
	}
	// shellenv exports GOBREW_PREFIX; honoring it keeps later commands on the
	// same prefix. --prefix still takes precedence.
	if prefix := os.Getenv("GOBREW_PREFIX"); prefix != "" {
		config.Prefix = prefix
	}
	// Homebrew's variable first, so one token serves both tools.
	config.GitHubToken = cmp.Or(os.Getenv("HOMEBREW_GITHUB_API_TOKEN"), os.Getenv("GITHUB_TOKEN"))
	a := &application{config: config, configErr: configErr, stderr: stderr, systemPrefix: engine.SystemPrefix, terminal: hasTerminal, elevate: sudo}
	return a.run(ctx, args, stdout, build)
}

func (a *application) run(ctx context.Context, args []string, stdout io.Writer, build BuildInfo) int {
	stderr := a.stderr
	if build.Version == "" {
		build.Version = "dev"
	}
	if build.Commit == "" {
		build.Commit = "unknown"
	}
	if build.Date == "" {
		build.Date = "unknown"
	}
	a.build = build
	if len(args) > 0 && args[0] == "-S" {
		args = append([]string{"search"}, args[1:]...)
	}
	root := a.root()
	args = pathCommand(root, args)
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
	cmd, err := root.ExecuteContextC(ctx)
	if a.command == "" {
		a.command = "gobrew"
		if cmd != nil {
			a.command = commandName(cmd)
		}
	}
	if err != nil && !a.started && !errors.Is(err, context.Canceled) {
		err = usageError{err}
	}
	if conflict := flagConflict(err); conflict != nil {
		err = conflict
	}
	code, kind := classify(err)
	// Cobra can reject an unknown command before parsing persistent flags.
	jsonOutput := a.json || requestedJSON(args)
	if jsonOutput {
		value := envelope{SchemaVersion: 1, Command: a.command, Data: a.result}
		if err != nil {
			value.Error = &errorInfo{Code: kind, Message: err.Error()}
		}
		encoder := json.NewEncoder(stdout)
		encoder.SetEscapeHTML(false)
		if writeErr := encoder.Encode(value); writeErr != nil {
			fmt.Fprintf(stderr, "Error: write output: %v\n", writeErr)
			return 1
		}
		return code
	}
	if a.hasResult && a.render != nil {
		if writeErr := a.render(stdout); writeErr != nil {
			var action actionError
			if errors.As(writeErr, &action) {
				fmt.Fprintf(stderr, "Error: %s\n", cleanText(action.Error()))
			} else {
				fmt.Fprintf(stderr, "Error: write output: %v\n", writeErr)
			}
			return 1
		}
	}
	if err != nil && err.Error() != "" {
		// Homebrew's error prefix; usage errors keep the --help pointer below.
		fmt.Fprintf(stderr, "Error: %s\n", cleanText(err.Error()))
		if code == 2 {
			path := "gobrew"
			if cmd != nil && cmd.HasParent() {
				path = "gobrew " + commandName(cmd)
				if cmd.Parent() != cmd.Root() {
					path = "gobrew " + strings.TrimPrefix(cmd.CommandPath(), cmd.Root().Name()+" ")
				}
			}
			fmt.Fprintf(stderr, "Run '%s --help' for usage.\n", path)
		}
	}
	return code
}

func classify(err error) (int, string) {
	if err == nil {
		return 0, ""
	}
	var usage usageError
	var held heldUpgrade
	switch {
	case errors.Is(err, context.Canceled):
		return 130, "canceled"
	case errors.As(err, &usage):
		return 2, "usage"
	case errors.Is(err, model.ErrUnsupported):
		return 3, "unsupported"
	case errors.Is(err, model.ErrConflict), errors.Is(err, model.ErrStalePlan), errors.Is(err, model.ErrBusy):
		return 4, "conflict"
	case errors.Is(err, model.ErrNeedsRepair):
		return 5, "needs_repair"
	// Only when nothing else failed: a joined real failure keeps its own code.
	case errors.As(err, &held) && err.Error() == held.Error():
		return 1, "held"
	case err == errOutdated:
		return 1, "outdated"
	case errors.Is(err, model.ErrPrivilegeRequired):
		return 1, "privilege_required"
	case errors.Is(err, model.ErrNotFound), errors.Is(err, os.ErrNotExist):
		return 1, "not_found"
	case errors.Is(err, context.DeadlineExceeded):
		return 1, "timeout"
	default:
		return 1, "failed"
	}
}

// cobraConflict is cobra's message for flags marked mutually exclusive.
var cobraConflict = regexp.MustCompile(`^if any flags in the group \[([^\]]+)\] are set none of the others can be; \[([^\]]+)\] were all set$`)

// flagConflict rewrites cobra's mutually exclusive flags error as brew's
// OptionConflictError: the options passed, in the order the group names them.
func flagConflict(err error) error {
	if err == nil {
		return nil
	}
	m := cobraConflict.FindStringSubmatch(err.Error())
	if m == nil {
		return nil
	}
	set := strings.Fields(m[2])
	var passed []string
	for _, name := range strings.Fields(m[1]) {
		if slices.Contains(set, name) {
			passed = append(passed, "--"+name)
		}
	}
	return usageError{fmt.Errorf("Options %s are mutually exclusive.", strings.Join(passed, " and "))}
}

var versionPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)

func requestedJSON(args []string) bool {
	requested := false
	for _, arg := range args {
		if arg == "--" {
			break
		}
		if arg == "--json" {
			requested = true
		} else if value, ok := strings.CutPrefix(arg, "--json="); ok {
			if parsed, err := strconv.ParseBool(value); err == nil {
				requested = parsed
			}
		}
	}
	return requested
}

func (a *application) getEngine() (*engine.Engine, error) {
	if a.engine != nil {
		return a.engine, nil
	}
	if a.configErr != nil {
		return nil, a.configErr
	}
	if !filepath.IsAbs(a.config.Prefix) || !filepath.IsAbs(a.config.CacheDir) {
		return nil, usageError{errors.New("--prefix and --cache-dir must be absolute paths")}
	}
	if a.config.Downloads < 1 || a.config.Jobs < 1 {
		return nil, usageError{errors.New("--downloads and --jobs must be positive")}
	}
	if a.config.BlobBudget < 1 || a.config.TreeBudget < 1 {
		return nil, usageError{errors.New("--download-cache-bytes and --prepared-cache-bytes must be positive")}
	}
	a.config.Prefix = filepath.Clean(a.config.Prefix)
	a.config.CacheDir = filepath.Clean(a.config.CacheDir)
	a.engine = engine.New(a.config)
	return a.engine, nil
}

func (a *application) setResult(data any, render func(io.Writer) error) {
	a.hasResult, a.result, a.render = true, data, render
}

func (a *application) message(data any, message string) {
	a.setResult(data, func(w io.Writer) error {
		_, err := fmt.Fprintln(w, cleanText(message))
		return err
	})
}

func (a *application) names(names []string, empty string) {
	if names == nil {
		names = []string{}
	}
	a.setResult(names, func(w io.Writer) error {
		if len(names) == 0 && empty != "" {
			_, err := fmt.Fprintln(w, empty)
			return err
		}
		for _, name := range names {
			if _, err := fmt.Fprintln(w, cleanText(name)); err != nil {
				return err
			}
		}
		return nil
	})
}
