// Package cli adapts command-line arguments and streams to the shared engine.
package cli

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"
	"unicode"

	"github.com/shqingda/gobrew/internal/engine"
	"github.com/shqingda/gobrew/internal/model"
	"github.com/shqingda/gobrew/internal/selfupdate"
	"github.com/shqingda/gobrew/internal/shellconfig"
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
	// systemPrefix is the only prefix the privileged helper may create.
	// Tests point it, terminal and elevate at temporary fakes so that no test
	// runs sudo or touches /opt.
	systemPrefix string
	terminal     func() bool
	elevate      func(ctx context.Context, executable string, args []string) error
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
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
	cmd, err := root.ExecuteContextC(ctx)
	if a.command == "" {
		a.command = "gobrew"
		if cmd != nil {
			a.command = cmd.Name()
		}
	}
	if err != nil && !a.started && !errors.Is(err, context.Canceled) {
		err = usageError{err}
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
			fmt.Fprintf(stderr, "gobrew: write output: %v\n", writeErr)
			return 1
		}
		return code
	}
	if a.hasResult && a.render != nil {
		if writeErr := a.render(stdout); writeErr != nil {
			fmt.Fprintf(stderr, "gobrew: write output: %v\n", writeErr)
			return 1
		}
	}
	if err != nil {
		// Homebrew's error prefix; usage errors keep the --help pointer below.
		fmt.Fprintf(stderr, "Error: %s\n", cleanText(err.Error()))
		if code == 2 {
			fmt.Fprintln(stderr, "Run 'gobrew --help' for usage.")
		}
	}
	return code
}

func classify(err error) (int, string) {
	if err == nil {
		return 0, ""
	}
	var usage usageError
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
		return nil, usageError{errors.New("cache byte limits must be positive")}
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
		if len(names) == 0 {
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

func (a *application) root() *cobra.Command {
	var version bool
	root := &cobra.Command{
		Use: "gobrew", Short: "A Go package manager for official Homebrew core bottles",
		Long:          "Install official Homebrew core bottles in an independent prefix.\nSupports macOS ARM64 15+; unsupported formula semantics fail before activation.",
		SilenceErrors: true, SilenceUsage: true,
		Args: cobra.NoArgs,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			a.started, a.command = true, cmd.Name()
			return cmd.Context().Err()
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			if version {
				a.version()
				return nil
			}
			return cmd.Help()
		},
	}
	root.Flags().BoolVarP(&version, "version", "v", false, "Print build information")
	root.PersistentFlags().StringVar(&a.config.Prefix, "prefix", a.config.Prefix, "Installation prefix")
	root.PersistentFlags().StringVar(&a.config.CacheDir, "cache-dir", a.config.CacheDir, "Catalog and artifact cache directory")
	root.PersistentFlags().IntVar(&a.config.Downloads, "downloads", a.config.Downloads, "Maximum concurrent downloads")
	root.PersistentFlags().IntVar(&a.config.Jobs, "jobs", a.config.Jobs, "Maximum concurrent preparation jobs")
	root.PersistentFlags().Int64Var(&a.config.BlobBudget, "download-cache-bytes", a.config.BlobBudget, "Compressed bottle cache budget in logical bytes")
	root.PersistentFlags().Int64Var(&a.config.TreeBudget, "prepared-cache-bytes", a.config.TreeBudget, "Prepared tree cache budget in logical bytes")
	root.PersistentFlags().BoolVar(&a.json, "json", false, "Write one versioned JSON result to stdout")
	root.PersistentFlags().BoolVarP(&a.quiet, "quiet", "q", false, "Suppress progress messages")
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return usageError{err} })
	root.SetHelpFunc(func(cmd *cobra.Command, _ []string) {
		description := cmd.Long
		if description == "" {
			description = cmd.Short
		}
		usage := cmd.UsageString()
		a.command = cmd.Name()
		a.setResult(map[string]string{"description": description, "usage": usage}, func(w io.Writer) error {
			_, err := fmt.Fprintf(w, "%s\n\n%s", description, usage)
			return err
		})
	})
	root.SetHelpCommand(&cobra.Command{
		Use: "help [command]", Short: "Show help for a command", Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			target, remaining, err := root.Find(args)
			if err != nil {
				return usageError{err}
			}
			if len(remaining) != 0 {
				return usageError{fmt.Errorf("unknown help topic %q", strings.Join(args, " "))}
			}
			target.InitDefaultHelpFlag()
			return target.Help()
		},
		ValidArgsFunction: func(_ *cobra.Command, _ []string, prefix string) ([]string, cobra.ShellCompDirective) {
			var names []string
			for _, command := range root.Commands() {
				if !command.Hidden && strings.HasPrefix(command.Name(), prefix) {
					names = append(names, command.Name())
				}
			}
			return names, cobra.ShellCompDirectiveNoFileComp
		},
	})
	root.CompletionOptions.DisableDefaultCmd = true
	root.AddCommand(a.commandFor("init", "Initialize an empty, user-owned installation prefix", cobra.NoArgs, a.init))
	root.AddCommand(a.searchCommand())
	root.AddCommand(a.infoCommand(), a.listCommand())
	root.AddCommand(a.commandFor("update", "Update gobrew itself and refresh the verified formula catalog", cobra.NoArgs, a.update))
	for _, action := range []string{"install", "reinstall", "upgrade"} {
		root.AddCommand(a.installCommand(action))
	}
	root.AddCommand(a.versionInstallCommand())
	root.AddCommand(a.commandFor("uninstall <formula>...", "Remove packages while retaining configuration and data", cobra.MinimumNArgs(1), a.uninstall))
	root.AddCommand(a.commandFor("outdated", "List installed formulae with available updates", cobra.NoArgs, a.outdated))
	for _, pin := range []bool{true, false} {
		name, description := "pin", "Prevent upgrades of installed formulae"
		if !pin {
			name, description = "unpin", "Allow upgrades of installed formulae"
		}
		root.AddCommand(a.commandFor(name+" <formula>...", description, cobra.MinimumNArgs(1), func(ctx context.Context, names []string) error {
			e, err := a.getEngine()
			if err != nil {
				return err
			}
			if err := e.Pin(ctx, names, pin); err != nil {
				return err
			}
			a.names(names, "")
			return nil
		}))
	}
	root.AddCommand(a.linkCommand(true), a.linkCommand(false), a.autoremoveCommand(), a.doctorCommand())
	root.AddCommand(a.cleanupCommand())
	root.AddCommand(a.commandFor("postinstall <formula>...", "Explicitly retry supported post-install initialization", cobra.MinimumNArgs(1), a.postinstall))
	root.AddCommand(a.commandFor("deps <formula>", "List transitive dependencies", cobra.ExactArgs(1), a.deps), a.usesCommand())
	root.AddCommand(a.importHomebrewCommand(), a.shellenvCommand(), a.completionCommand(), a.selfCommand())
	for _, command := range root.Commands() {
		command.Aliases = brewAliases[command.Name()]
	}
	return root
}

func (a *application) commandFor(use, description string, args cobra.PositionalArgs, run func(context.Context, []string) error) *cobra.Command {
	return &cobra.Command{
		Use: use, Short: description, Args: args,
		RunE: func(cmd *cobra.Command, names []string) error { return run(cmd.Context(), names) },
		ValidArgsFunction: func(_ *cobra.Command, _ []string, prefix string) ([]string, cobra.ShellCompDirective) {
			e, err := a.getEngine()
			if err != nil {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			snapshot, err := e.Installed()
			if err != nil {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			var names []string
			for name := range snapshot.Records {
				if strings.HasPrefix(name, prefix) {
					names = append(names, name)
				}
			}
			slices.Sort(names)
			return names, cobra.ShellCompDirectiveNoFileComp
		},
	}
}

func (a *application) version() {
	a.command = "version"
	a.message(a.build, fmt.Sprintf("gobrew %s (commit %s, built %s)", a.build.Version, a.build.Commit, a.build.Date))
}

func (a *application) init(ctx context.Context, _ []string) error {
	e, err := a.getEngine()
	if err != nil {
		return err
	}
	if os.Geteuid() == 0 {
		if err := e.Init(); err != nil {
			return err
		}
		a.message(map[string]string{"prefix": e.Prefix()}, "Created "+e.Prefix()+" for your user. Your next gobrew command finishes setup without sudo.")
		return nil
	}
	if err := a.initialize(ctx, e); err != nil {
		return err
	}
	message := "Initialized " + e.Prefix() + "."
	if e.Prefix() != a.systemPrefix {
		message += " Custom prefixes are not added to PATH automatically; print the setup with: gobrew --prefix " + shellQuote(e.Prefix()) + " shellenv"
	}
	a.message(map[string]string{"prefix": e.Prefix()}, message)
	return nil
}

// ensureInitialized makes the first install work without a separate init step.
// Already initialized prefixes cost one stat.
func (a *application) ensureInitialized(ctx context.Context, e *engine.Engine) error {
	ok, err := e.Initialized()
	if err != nil || ok {
		return err
	}
	return a.initialize(ctx, e)
}

// initialize runs Init and, when only administrator access is missing for the
// default prefix, runs this executable's root init once through sudo. sudo
// reads the password from the controlling terminal, never from our stdin, so
// this also works under curl | sh. Everything else stays unprivileged.
func (a *application) initialize(ctx context.Context, e *engine.Engine) error {
	err := e.Init()
	if !errors.Is(err, model.ErrPrivilegeRequired) || e.Prefix() != a.systemPrefix {
		return err
	}
	executable, exeErr := os.Executable()
	if exeErr != nil {
		return errors.Join(err, exeErr)
	}
	manual := "sudo " + shellQuote(executable) + " init"
	if a.json || !a.terminal() {
		return fmt.Errorf("%w; run once in a terminal: %s", err, manual)
	}
	fmt.Fprintf(a.stderr, "gobrew needs administrator access once to create %s and hand it to you.\nNo downloads or package code run as root.\n", e.Prefix())
	elevateErr := a.elevate(ctx, executable, []string{"--prefix", e.Prefix(), "--quiet", "init"})
	if cause := ctx.Err(); cause != nil {
		return cause
	}
	// Retry even after a failed helper: a concurrent installer may have
	// created the prefix for this user in the meantime.
	if err = e.Init(); err != nil && elevateErr != nil {
		return fmt.Errorf("%w: administrator authorization did not complete (%v); to retry manually run: %s", model.ErrPrivilegeRequired, elevateErr, manual)
	}
	return err
}

func hasTerminal() bool {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return false
	}
	_ = tty.Close()
	return true
}

func sudo(ctx context.Context, executable string, args []string) error {
	// An absolute sudo avoids PATH lookups; stdin stays closed so a piped
	// installer script is never consumed as a password.
	cmd := exec.CommandContext(ctx, "/usr/bin/sudo", append([]string{"-p", "Password for %u: ", "--", executable}, args...)...)
	cmd.Stdout = io.Discard
	cmd.Stderr = os.Stderr
	// SIGTERM lets sudo forward cancellation to the root helper; SIGKILL would
	// orphan it. The helper only performs two short directory operations.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 5 * time.Second
	return cmd.Run()
}

// searchCommand mirrors brew search [--formula] [--desc] text|/regex/.
func (a *application) searchCommand() *cobra.Command {
	var descriptions, formulaOnly bool
	cmd := a.commandFor("search <text|/regex/>...", "Search formula names, or descriptions with --desc", cobra.MinimumNArgs(1), func(ctx context.Context, args []string) error {
		e, err := a.getEngine()
		if err != nil {
			return err
		}
		query := strings.Join(args, " ")
		formulae, err := e.Search(ctx, query, descriptions)
		if err != nil {
			return err
		}
		installed := map[string]bool{}
		if snapshot, err := e.Installed(); err == nil {
			for name := range snapshot.Records {
				installed[name] = true
			}
		}
		a.setResult(formulae, func(w io.Writer) error {
			p := newProgress(w)
			if descriptions {
				if err := p.title("Formulae"); err != nil {
					return err
				}
				for _, f := range formulae {
					if _, err := fmt.Fprintln(w, cleanText(f.Name+": "+strings.Join(strings.Fields(f.Description), " "))); err != nil {
						return err
					}
				}
				return nil
			}
			names := make([]string, len(formulae))
			widths := make([]int, len(formulae))
			for i, f := range formulae {
				names[i], widths[i] = cleanText(f.Name), len(f.Name)
				if p.tty && installed[f.Name] {
					mark := " \u2714"
					if os.Getenv("GOBREW_NO_EMOJI") != "" {
						mark = " (installed)"
					}
					names[i] = "\033[1m" + names[i] + "\033[32m" + mark + "\033[0m"
					widths[i] += len([]rune(mark))
				}
			}
			if !p.tty {
				for _, name := range names {
					if _, err := fmt.Fprintln(w, name); err != nil {
						return err
					}
				}
				return nil
			}
			if err := p.title("Formulae"); err != nil {
				return err
			}
			_, err := io.WriteString(w, columns(names, widths, p.width))
			return err
		})
		if len(formulae) == 0 {
			return notFound(fmt.Sprintf("No formulae found for %q.", query))
		}
		if newProgress(a.stderr).tty && !strings.HasPrefix(query, "/") && strings.ContainsAny(query, `\|()[]{}^$*+?`) {
			fmt.Fprintln(a.stderr, "Warning: Did you mean to perform a regular expression search?\nSurround your query with /slashes/ to search locally by regex.")
		}
		return nil
	})
	cmd.Flags().BoolVar(&descriptions, "desc", false, "Search descriptions instead of names")
	cmd.Flags().BoolVar(&formulaOnly, "formula", false, "Search only formulae (gobrew has no casks)")
	return cmd
}

// columns ports Homebrew's Formatter.columns: fill columns top to bottom,
// two spaces apart, without trailing whitespace; one per line if too narrow.
func columns(items []string, widths []int, width int) string {
	if len(items) == 0 {
		return ""
	}
	const gap = 2
	longest := slices.Max(widths)
	cols := (width + gap) / (longest + gap)
	if cols < 2 {
		return strings.Join(items, "\n") + "\n"
	}
	rows := (len(items) + cols - 1) / cols
	cols = (len(items) + rows - 1) / rows
	colWidth := (width+gap)/cols - gap
	var b strings.Builder
	for row := 0; row < rows; row++ {
		for i := row; i < len(items); i += rows {
			b.WriteString(items[i])
			if i+rows < len(items) {
				b.WriteString(strings.Repeat(" ", colWidth-widths[i]+gap))
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

func (a *application) infoCommand() *cobra.Command {
	var installed bool
	cmd := a.commandFor("info <formula>...", "Show formula details and compatibility information", cobra.MinimumNArgs(1), func(ctx context.Context, names []string) error {
		e, err := a.getEngine()
		if err != nil {
			return err
		}
		formulae := make([]model.Formula, 0, len(names))
		if installed {
			snapshot, err := e.Installed()
			if err != nil {
				return err
			}
			for _, name := range names {
				r, ok := snapshot.Records[strings.TrimPrefix(name, "homebrew/core/")]
				if !ok {
					return fmt.Errorf("%w: %s is not installed", model.ErrNotFound, name)
				}
				formulae = append(formulae, r.Formula)
			}
		} else {
			catalog, err := e.Catalog(ctx)
			if err != nil {
				return err
			}
			for _, name := range names {
				formula, err := catalog.Lookup(name)
				if err != nil {
					return err
				}
				formulae = append(formulae, formula)
			}
		}
		records := map[string]model.Record{}
		if snapshot, err := e.Installed(); err == nil {
			records = snapshot.Records
		}
		a.setResult(formulae, func(w io.Writer) error {
			for i, f := range formulae {
				if i > 0 {
					if _, err := fmt.Fprintln(w); err != nil {
						return err
					}
				}
				lines := []string{f.Name + " " + f.PkgVersion(), f.Description, f.Homepage}
				if len(f.Dependencies) > 0 {
					lines = append(lines, "Dependencies: "+strings.Join(f.Dependencies, ", "))
				}
				if f.KegOnly {
					lines = append(lines, "Keg-only: not linked into the shared prefix by default.")
				}
				if f.Disabled {
					lines = append(lines, "Disabled upstream.")
				}
				if f.Deprecated {
					lines = append(lines, "Deprecated upstream.")
				}
				if f.UnsupportedReason != "" {
					lines = append(lines, "Unsupported: "+f.UnsupportedReason)
				}
				if f.Caveats != "" {
					lines = append(lines, f.Caveats)
				}
				if r, ok := records[f.Name]; ok {
					lines = append(lines, shadowText(e.Prefix(), r))
				}
				for _, line := range lines {
					if line != "" {
						if _, err := fmt.Fprintln(w, cleanText(line)); err != nil {
							return err
						}
					}
				}
			}
			return nil
		})
		return nil
	})
	cmd.Flags().BoolVar(&installed, "installed", false, "Read installed metadata without opening the catalog")
	return cmd
}

func (a *application) listCommand() *cobra.Command {
	var pinned, onRequest, notOnRequest bool
	cmd := a.commandFor("list", "List installed formulae without network access", cobra.NoArgs, func(_ context.Context, _ []string) error {
		e, err := a.getEngine()
		if err != nil {
			return err
		}
		snapshot, err := e.Installed()
		if err != nil {
			return err
		}
		records := make([]model.Record, 0, len(snapshot.Records))
		for _, record := range snapshot.Records {
			if pinned && !record.Pinned || onRequest && !record.Requested || notOnRequest && record.Requested {
				continue
			}
			records = append(records, record)
		}
		slices.SortFunc(records, func(a, b model.Record) int { return strings.Compare(a.Formula.Name, b.Formula.Name) })
		a.setResult(records, func(w io.Writer) error {
			rows := make([][]string, 0, len(records))
			for _, r := range records {
				flags := []string{}
				if r.Requested {
					flags = append(flags, "requested")
				}
				if r.Pinned {
					flags = append(flags, "pinned")
				}
				if !r.Linked {
					flags = append(flags, "unlinked")
				}
				rows = append(rows, []string{r.Formula.Name, r.Formula.PkgVersion(), r.Status, strings.Join(flags, ",")})
			}
			return table(w, []string{"FORMULA", "VERSION", "STATUS", "FLAGS"}, rows)
		})
		return nil
	})
	cmd.Flags().BoolVar(&pinned, "pinned", false, "Only show pinned formulae")
	cmd.Flags().BoolVar(&onRequest, "installed-on-request", false, "Only show formulae installed on request")
	cmd.Flags().BoolVar(&notOnRequest, "no-installed-on-request", false, "Only show formulae installed as dependencies")
	cmd.MarkFlagsMutuallyExclusive("installed-on-request", "no-installed-on-request")
	return cmd
}

// cliUpdate reports the self-update half of update.
type cliUpdate struct {
	selfupdate.Result
	Skipped string `json:"skipped,omitempty"`
	Error   string `json:"error,omitempty"`
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
	return append(lines, fmt.Sprintf("You have %d outdated %s installed. Upgrade with: gob upgrade", len(outdated), noun))
}

func (a *application) update(ctx context.Context, _ []string) error {
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
			fmt.Fprintln(a.stderr, "`$GOBREW_NO_AUTO_UPDATE=1`. Hide these hints with `$GOBREW_NO_ENV_HINTS=1` (see `gob help`).")
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

func (a *application) installCommand(action string) *cobra.Command {
	var dryRun bool
	args, use := cobra.MinimumNArgs(1), action+" <formula>..."
	if action == "upgrade" {
		args, use = cobra.ArbitraryArgs, "upgrade [formula...]"
	}
	cmd := a.commandFor(use, strings.ToUpper(action[:1])+action[1:]+" official core bottles and their dependencies", args, func(ctx context.Context, names []string) error {
		return a.install(ctx, model.Request{Action: action, Names: names}, dryRun)
	})
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Resolve and show the exact plan without installing")
	return cmd
}

// versionInstallCommand mirrors brew version-install formula[@version] [version].
// Homebrew extracts the version into a personal tap; gobrew verifies the bottle
// against GHCR and homebrew-core instead. Only this command takes versions:
// like brew install, plain install never splits names at "@".
func (a *application) versionInstallCommand() *cobra.Command {
	var dryRun bool
	cmd := a.commandFor("version-install <formula[@version]> [version]", "Install a specific version of a formula and pin it", cobra.RangeArgs(1, 2), func(ctx context.Context, args []string) error {
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
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Resolve and show the exact plan without installing")
	return cmd
}

func (a *application) install(ctx context.Context, request model.Request, dryRun bool) error {
	action := request.Action
	e, err := a.getEngine()
	if err != nil {
		return err
	}
	// Homebrew auto-updates before install and upgrade, but not reinstall or
	// version-install.
	if len(request.Versions) == 0 && (action == "install" || action == "upgrade") {
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
	return errors.Join(err, heldError(e, result.Held))
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
	a.names(removed, "Nothing to uninstall.")
	return nil
}

func (a *application) outdated(ctx context.Context, _ []string) error {
	e, err := a.getEngine()
	if err != nil {
		return err
	}
	if err = a.autoUpdate(ctx, e, false); err != nil {
		return err
	}
	items, err := e.Outdated(ctx)
	if err != nil {
		return err
	}
	a.setResult(items, func(w io.Writer) error {
		rows := make([][]string, 0, len(items))
		for _, item := range items {
			pinned := ""
			if item.Pinned {
				pinned = "pinned"
			}
			rows = append(rows, []string{item.Name, item.Installed, item.Available, pinned})
		}
		return table(w, []string{"FORMULA", "INSTALLED", "AVAILABLE", "PIN"}, rows)
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
		a.names(names, "No unused dependencies.")
		return nil
	})
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "List unused dependencies without removing them")
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
		unhealthy := len(findings) > 0
		if snapshot, err := e.Installed(); err == nil {
			findings = append(findings, a.pathFindings(e, snapshot.Records)...)
		}
		a.setResult(findings, func(w io.Writer) error {
			if len(findings) == 0 {
				_, err := fmt.Fprintln(w, "No installation problems found.")
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
	cmd.Flags().BoolVar(&repair, "repair", false, "Recover interrupted filesystem transactions before checking; does not replay post-install")
	return cmd
}

// cleanupCommand mirrors brew cleanup [--prune=days|all] [-n] [-s] [formula...].
func (a *application) cleanupCommand() *cobra.Command {
	var prune string
	var dryRun, scrub bool
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
			opts.MaxAge = time.Duration(days) * 24 * time.Hour
		}
		removed, err := e.Cleanup(ctx, opts)
		if err == nil && !dryRun && len(names) == 0 {
			err = e.MarkCleaned()
		}
		if removed == nil {
			removed = []model.Removed{}
		}
		a.setResult(removed, func(w io.Writer) error {
			lines := removedLines(removed, dryRun)
			if total := freed(removed); total > 0 {
				verb := "has freed"
				if dryRun {
					verb = "would free"
				}
				lines = append(lines, "==> This operation "+verb+" approximately "+humanBytes(total)+" of disk space.")
			}
			for _, line := range lines {
				if _, err := fmt.Fprintln(w, cleanText(line)); err != nil {
					return err
				}
			}
			return nil
		})
		return err
	})
	cmd.Flags().StringVar(&prune, "prune", "", "Remove cache files older than this many days, or all")
	cmd.Flags().BoolVarP(&dryRun, "dry-run", "n", false, "Show what would be removed without removing anything")
	cmd.Flags().BoolVarP(&scrub, "scrub", "s", false, "Scrub the cache, including downloads for the latest versions")
	return cmd
}

// cleanupMaxAge is Homebrew's HOMEBREW_CLEANUP_MAX_AGE_DAYS, default 120.
func cleanupMaxAge() time.Duration {
	days := 120
	if value, err := strconv.Atoi(os.Getenv("GOBREW_CLEANUP_MAX_AGE_DAYS")); err == nil && value >= 0 {
		days = value
	}
	return time.Duration(days) * 24 * time.Hour
}

func removedLines(removed []model.Removed, dryRun bool) []string {
	lines := make([]string, 0, len(removed))
	for _, r := range removed {
		if dryRun {
			lines = append(lines, "Would remove: "+r.Path+" ("+abv(r.Files, r.Bytes)+")")
		} else {
			lines = append(lines, "Removing: "+r.Path+"... ("+abv(r.Files, r.Bytes)+")")
		}
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
	formulae := slices.DeleteFunc(slices.Clone(result.Succeeded), func(name string) bool { return slices.Contains(result.Pinned, name) })
	var lines []string
	if len(formulae) > 0 {
		removed, err := e.Cleanup(ctx, engine.CleanupOptions{Formulae: formulae, Skip: skip})
		if err != nil {
			return []string{"Warning: cleanup failed: " + err.Error()}
		}
		if len(removed) > 0 {
			lines = append(append(lines, "==> Cleanup"), removedLines(removed, false)...)
		}
	}
	days := 30
	if value, err := strconv.Atoi(os.Getenv("GOBREW_CLEANUP_PERIODIC_FULL_DAYS")); err == nil && value >= 0 {
		days = value
	}
	if due, err := e.PeriodicCleanupDue(time.Duration(days) * 24 * time.Hour); err == nil && due {
		lines = append(lines, fmt.Sprintf("==> `gob cleanup` has not been run in the last %d days, running now...", days))
		removed, err := e.Cleanup(ctx, engine.CleanupOptions{Skip: skip, MaxAge: cleanupMaxAge()})
		if err == nil {
			err = e.MarkCleaned()
		}
		if err != nil {
			return append(lines, "Warning: cleanup failed: "+err.Error())
		}
		lines = append(lines, removedLines(removed, false)...)
	}
	if len(lines) > 0 && os.Getenv("GOBREW_NO_ENV_HINTS") == "" {
		lines = append(lines, "Disable this behaviour by setting `GOBREW_NO_INSTALL_CLEANUP=1`.", "Hide these hints with `GOBREW_NO_ENV_HINTS=1` (see `gob help`).")
	}
	return lines
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

func (a *application) deps(ctx context.Context, names []string) error {
	e, err := a.getEngine()
	if err != nil {
		return err
	}
	deps, err := e.Deps(ctx, names[0])
	if err != nil {
		return err
	}
	a.names(deps, "No dependencies.")
	return nil
}

func (a *application) usesCommand() *cobra.Command {
	var installed bool
	cmd := a.commandFor("uses <formula>", "List formulae with a direct dependency on a formula", cobra.ExactArgs(1), func(ctx context.Context, names []string) error {
		e, err := a.getEngine()
		if err != nil {
			return err
		}
		uses, err := e.Uses(ctx, names[0], installed)
		if err != nil {
			return err
		}
		a.names(uses, "No dependent formulae.")
		return nil
	})
	cmd.Flags().BoolVar(&installed, "installed", false, "Only inspect installed formulae, without network access")
	return cmd
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

func (a *application) shellenvCommand() *cobra.Command {
	var shell string
	cmd := a.commandFor("shellenv", "Print shell commands that put gobrew tools on PATH", cobra.NoArgs, func(_ context.Context, _ []string) error {
		e, err := a.getEngine()
		if err != nil {
			return err
		}
		prefix := e.Prefix()
		env := shellconfig.Env{Paths: []string{filepath.Join(prefix, "bin"), filepath.Join(prefix, "sbin")}, Prefix: prefix}
		script, err := env.Script(shell)
		if err != nil {
			return usageError{err}
		}
		a.setResult(map[string]string{"shell": shell, "script": script}, func(w io.Writer) error { _, err := io.WriteString(w, script); return err })
		return nil
	})
	cmd.Flags().StringVar(&shell, "shell", "zsh", "Shell syntax: zsh, bash, sh, or fish")
	return cmd
}

func (a *application) completionCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "completion <bash|zsh|fish|powershell>", Short: "Generate a shell completion script", Args: cobra.ExactArgs(1), ValidArgs: []string{"bash", "zsh", "fish", "powershell"}}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		var script bytes.Buffer
		var err error
		switch args[0] {
		case "bash":
			err = cmd.Root().GenBashCompletionV2(&script, true)
		case "zsh":
			err = cmd.Root().GenZshCompletion(&script)
		case "fish":
			err = cmd.Root().GenFishCompletion(&script, true)
		case "powershell":
			err = cmd.Root().GenPowerShellCompletionWithDesc(&script)
		default:
			return usageError{fmt.Errorf("unsupported completion shell %q", args[0])}
		}
		if err != nil {
			return err
		}
		text := script.String()
		// Both executable names share Cobra's generated completion functions.
		switch args[0] {
		case "bash":
			text += "\nif [[ $(type -t compopt) = builtin ]]; then\n    complete -o default -F __start_gobrew gob\nelse\n    complete -o default -o nospace -F __start_gobrew gob\nfi\n"
		case "zsh":
			// compinit reads the first line before the script is sourced.
			text = strings.Replace(text, "#compdef gobrew\ncompdef _gobrew gobrew\n", "#compdef gobrew gob\ncompdef _gobrew gobrew gob\n", 1)
		case "fish":
			text += "\ncomplete -c gob -e\ncomplete -c gob -w gobrew\n"
		case "powershell":
			text += "\nRegister-ArgumentCompleter -CommandName 'gob' -ScriptBlock ${__gobrewCompleterBlock}\n"
		}
		a.setResult(map[string]string{"shell": args[0], "script": text}, func(w io.Writer) error { _, err := io.WriteString(w, text); return err })
		return nil
	}
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
		for event := range events {
			if writeErr == nil {
				writeErr = p.event(event)
			}
		}
		done <- errors.Join(writeErr, p.clear())
	}()
	err := run(events)
	close(events)
	return errors.Join(err, <-done)
}

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

// renderInstall follows Homebrew: one "<keg>: N files, SIZE" line per poured
// keg, caveats, and an up-to-date warning for unchanged requests.
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
			unpin = "gob unpin " + name + " && "
		}
		messages = append(messages, fmt.Sprintf("%s %s is already installed.\nTo upgrade to %s, run:\n  %sgob upgrade %s", name, r.Formula.PkgVersion(), available[name].PkgVersion(), unpin, name))
	}
	return errors.New(strings.Join(messages, "\n"))
}

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
	for _, name := range result.Pinned {
		version := ""
		if r, ok := records[name]; ok {
			version = " " + r.Formula.PkgVersion()
		}
		lines = append(lines, "Warning: "+name+version+" is not covered by Homebrew's signed metadata; its bottle was verified against GHCR and homebrew-core history.",
			"It is pinned so upgrades keep this version. To allow upgrades: gob unpin "+name)
	}
	// Homebrew warns only about the formulae the user named.
	for _, name := range result.Unchanged {
		r, ok := records[name]
		if !ok || !slices.Contains(request.Names, name) {
			continue
		}
		version := r.Formula.PkgVersion()
		fmt.Fprintf(a.stderr, "Warning: %s %s is already installed and up-to-date.\nTo reinstall %s, run:\n  gob reinstall %s\n", name, version, version, name)
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

func renderSkipped(w io.Writer, names []string) error {
	if len(names) == 0 {
		return nil
	}
	_, err := fmt.Fprintln(w, "Skipped:", cleanText(strings.Join(names, ", ")))
	return err
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
