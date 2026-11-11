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
	"github.com/spf13/cobra"
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
	"home":        {"homepage"},
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
	"desc": true, "home": true,
}

type application struct {
	config    engine.Config
	configErr error
	engine    *engine.Engine
	build     BuildInfo
	json      bool
	quiet     bool
	// verbose and debug are brew's global -v and -d; debug implies verbose.
	verbose   bool
	debug     bool
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
	args = versionShorthand(root, args)
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
	if missing := brewOnlyCommand(err); missing != nil {
		err = missing
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
		fmt.Fprintf(stderr, "%s: %s\n", label(stderr, "Error", "31"), cleanText(userMessage(err)))
		if hint := errorHint(err, a.command); hint != "" {
			fmt.Fprintln(stderr, hint)
		}
		if a.debug && userMessage(err) != err.Error() {
			fmt.Fprintf(stderr, "Debug: %s (%s)\n", cleanText(err.Error()), kind)
		}
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

// categories are model's error kinds. Messages built as "%w: detail" start
// with the kind's own text ("not found: jq"), which suits the JSON code but
// not a person; text output drops it. JSON keeps the full message.
var categories = []error{model.ErrUnsupported, model.ErrNotFound, model.ErrConflict, model.ErrBusy}

// userMessage is the text shown after "Error: ".
func userMessage(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "Interrupted. gobrew stops only between safe steps, and the next command finishes any interrupted change."
	case errors.Is(err, model.ErrStalePlan):
		return "Another gobrew command changed the installation while this one was running; run it again."
	}
	message := err.Error()
	for _, category := range categories {
		if errors.Is(err, category) {
			message = strings.Replace(message, category.Error()+": ", "", 1)
		}
	}
	return message
}

// errorHint suggests the next command for failures a user can act on.
func errorHint(err error, command string) string {
	switch {
	case errors.Is(err, model.ErrRecoveryRequired):
		return "Run `gobrew doctor --repair` to recover the interrupted operation."
	case errors.Is(err, model.ErrNeedsRepair) && command != "doctor":
		return "Run `gobrew doctor` to see what needs repair."
	}
	return ""
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

// versionShorthand keeps brew's "gobrew -v" for the version: with no command,
// -v means --version; after a command it is the global --verbose.
func versionShorthand(root *cobra.Command, args []string) []string {
	flags := root.Flags()
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" || !strings.HasPrefix(arg, "-") {
			return args
		}
		// Skip the separate value of a root flag such as --prefix PATH.
		if name, ok := strings.CutPrefix(arg, "--"); ok && !strings.Contains(name, "=") {
			f := flags.Lookup(name)
			if f == nil {
				f = root.PersistentFlags().Lookup(name)
			}
			if f != nil && f.NoOptDefVal == "" {
				i++
			}
		}
	}
	out := slices.Clone(args)
	for i, arg := range out {
		if arg == "-v" {
			out[i] = "--version"
		}
	}
	return out
}

// brewOnly are Homebrew commands gobrew deliberately does not provide, with
// the reason. Other unknown commands keep cobra's message and suggestions.
var brewOnly = map[string]string{
	"tap": "gobrew installs only official homebrew/core formulae", "untap": "gobrew installs only official homebrew/core formulae",
	"tap-info": "gobrew installs only official homebrew/core formulae", "services": "gobrew does not manage background services",
	"bundle": "gobrew does not read Brewfiles", "fetch": "gobrew downloads bottles as part of install",
	"cask": "gobrew installs only formulae", "update-reset": "gobrew does not use Git for its catalog",
	"--repository": "gobrew does not use Git for its catalog", "--repo": "gobrew does not use Git for its catalog",
	"create": "gobrew has no formula development tools", "edit": "gobrew has no formula development tools",
	"audit": "gobrew has no formula development tools", "bottle": "gobrew has no formula development tools",
	"test": "gobrew has no formula development tools", "style": "gobrew has no formula development tools",
	"extract": "use gobrew version-install for an older version", "migrate": "gobrew installs only official homebrew/core formulae",
}

var unknownCommand = regexp.MustCompile(`^unknown command "([^"]+)" for "gobrew"`)

func brewOnlyCommand(err error) error {
	if err == nil {
		return nil
	}
	m := unknownCommand.FindStringSubmatch(err.Error())
	if m == nil {
		return nil
	}
	reason, ok := brewOnly[m[1]]
	if !ok {
		return nil
	}
	return fmt.Errorf("%w: Homebrew's `%s` command is not available in gobrew: %s.", model.ErrUnsupported, m[1], reason)
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

// warn is brew's opoo: a "Warning:" line on stderr, never on stdout.
func (a *application) warn(text string) {
	fmt.Fprintf(a.stderr, "%s: %s\n", label(a.stderr, "Warning", "33"), cleanText(text))
}

// label underlines and colors "Error" (red, 31) or "Warning" (yellow, 33) on a
// color terminal, as Homebrew's Formatter does.
func label(w io.Writer, text, color string) string {
	if colorful(w) {
		return "\033[4;" + color + "m" + text + "\033[0m"
	}
	return text
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
