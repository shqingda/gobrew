package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/shqingda/gobrew/internal/model"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func (a *application) root() *cobra.Command {
	var version bool
	root := &cobra.Command{
		Use: "gobrew", Short: "A Go package manager for official Homebrew core bottles",
		Long:          "Install official Homebrew core bottles in an independent prefix.\nSupports macOS ARM64 15+; unsupported formula semantics fail before activation.",
		SilenceErrors: true, SilenceUsage: true,
		// No Args validator: cobra then rejects an unknown command with
		// "Did you mean this?" suggestions.
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			a.started, a.command = true, commandName(cmd)
			a.verbose = a.verbose || a.debug
			if f := cmd.Flags().Lookup("cask"); f != nil && f.Changed {
				return fmt.Errorf("%w: gobrew installs only Homebrew formulae; casks are not supported", model.ErrUnsupported)
			}
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
	root.Flags().BoolVar(&version, "version", false, "Print build information (also -v without a command)")
	root.PersistentFlags().StringVar(&a.config.Prefix, "prefix", a.config.Prefix, "Installation prefix")
	root.PersistentFlags().StringVar(&a.config.CacheDir, "cache-dir", a.config.CacheDir, "Catalog and artifact cache directory")
	root.PersistentFlags().IntVar(&a.config.Downloads, "downloads", a.config.Downloads, "Maximum concurrent downloads")
	root.PersistentFlags().IntVar(&a.config.Jobs, "jobs", a.config.Jobs, "Maximum concurrent preparation jobs")
	root.PersistentFlags().Int64Var(&a.config.BlobBudget, "download-cache-bytes", a.config.BlobBudget, "Compressed bottle cache budget in logical bytes")
	root.PersistentFlags().Int64Var(&a.config.TreeBudget, "prepared-cache-bytes", a.config.TreeBudget, "Prepared tree cache budget in logical bytes")
	root.PersistentFlags().BoolVar(&a.json, "json", false, "Write one versioned JSON result to stdout")
	root.PersistentFlags().BoolVarP(&a.quiet, "quiet", "q", false, "Suppress progress messages")
	root.PersistentFlags().BoolVarP(&a.verbose, "verbose", "v", false, "Print more detail, such as stage timings")
	root.PersistentFlags().BoolVarP(&a.debug, "debug", "d", false, "Print debugging detail, such as full error text; implies --verbose")
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return usageError{err} })
	root.SetHelpFunc(func(cmd *cobra.Command, _ []string) {
		description := cmd.Long
		if description == "" {
			description = cmd.Short
		}
		usage := strings.ReplaceAll(cmd.UsageString(), "gobrew __", "gobrew --")
		a.command = commandName(cmd)
		a.setResult(map[string]string{"description": description, "usage": usage}, func(w io.Writer) error {
			_, err := fmt.Fprintf(w, "%s\n\n%s", description, usage)
			return err
		})
	})
	root.SetHelpCommand(&cobra.Command{
		Use: "help [command]", Short: "Show help for a command", Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if text, ok := helpTopic(args); ok {
				a.message(map[string]string{"topic": args[0], "text": text}, strings.TrimSuffix(text, "\n"))
				return nil
			}
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
	})
	root.CompletionOptions.DisableDefaultCmd = true
	root.AddCommand(a.searchCommand())
	root.AddCommand(a.infoCommand(), a.listCommand())
	root.AddCommand(a.commandFor("update", "Update gobrew itself and refresh the verified formula catalog", cobra.NoArgs, a.update))
	root.AddCommand(a.installCommand("install"), a.installCommand("reinstall"), a.installCommand("upgrade"))
	root.AddCommand(a.versionInstallCommand())
	root.AddCommand(a.commandFor("uninstall <formula>...", "Remove packages while retaining configuration and data", cobra.MinimumNArgs(1), a.uninstall))
	root.AddCommand(a.outdatedCommand())
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
			verb := "Pinned "
			if !pin {
				verb = "Unpinned "
			}
			a.setResult(names, func(w io.Writer) error {
				for _, name := range names {
					if _, err := fmt.Fprintln(w, cleanText(verb+name)); err != nil {
						return err
					}
				}
				return nil
			})
			return nil
		}))
	}
	root.AddCommand(a.linkCommand(true), a.linkCommand(false), a.autoremoveCommand(), a.doctorCommand())
	root.AddCommand(a.cleanupCommand())
	root.AddCommand(a.commandFor("postinstall <formula>...", "Explicitly retry supported post-install initialization", cobra.MinimumNArgs(1), a.postinstall))
	root.AddCommand(a.depsCommand(), a.usesCommand())
	root.AddCommand(a.shellenvCommand(), a.selfCommand())
	root.AddCommand(a.pathCommands()...)
	for _, command := range root.Commands() {
		command.Aliases = brewAliases[command.Name()]
		// gobrew has only formulae, so brew's --formula changes nothing.
		if brewFormulaFlag[command.Name()] && command.Flags().Lookup("formula") == nil {
			command.Flags().Bool("formula", false, "Treat all named arguments as formulae (gobrew has only formulae)")
		}
		addCaskFlags(command)
		// brew's --cask; gobrew refuses it plainly instead of "unknown flag".
		if brewFormulaFlag[command.Name()] {
			command.Flags().Bool("cask", false, "Refused: gobrew has no casks")
			_ = command.Flags().MarkHidden("cask")
			command.Flags().SetNormalizeFunc(func(_ *pflag.FlagSet, name string) pflag.NormalizedName {
				if name == "casks" {
					name = "cask"
				}
				return pflag.NormalizedName(name)
			})
			command.MarkFlagsMutuallyExclusive("formula", "cask")
		}
	}
	addHelpText(root)
	return root
}

func (a *application) commandFor(use, description string, args cobra.PositionalArgs, run func(context.Context, []string) error) *cobra.Command {
	return &cobra.Command{
		Use: use, Short: description, Args: args,
		RunE: func(cmd *cobra.Command, names []string) error { return run(cmd.Context(), names) },
	}
}

func (a *application) version() {
	a.command = "version"
	a.message(a.build, fmt.Sprintf("gobrew %s (commit %s, built %s)", a.build.Version, a.build.Commit, a.build.Date))
}
