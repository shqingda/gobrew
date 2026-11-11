package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/shqingda/gobrew/internal/engine"
	"github.com/shqingda/gobrew/internal/model"
	"github.com/spf13/cobra"
)

// brew's --prefix, --cellar and --cache are commands, but cobra command names
// cannot start with a dash, so pathCommand renames the leading one to
// __prefix and so on. gobrew's own --prefix DIR stays an option: its value
// is an absolute path, which no formula name is, or a command follows it, so
// a mistyped relative prefix still reports the option's error.
func pathCommand(root *cobra.Command, args []string) []string {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "-") || arg == "--" || arg == "-" {
			return args
		}
		if strings.Contains(arg, "=") || !strings.HasPrefix(arg, "--") {
			continue
		}
		option := i+1 < len(args) && (filepath.IsAbs(args[i+1]) || i+2 < len(args) && isCommand(root, args[i+2]))
		if arg == "--cellar" || arg == "--cache" || arg == "--prefix" && !option {
			renamed := slices.Clone(args)
			renamed[i] = "__" + arg[2:]
			return renamed
		}
		if f := root.PersistentFlags().Lookup(arg[2:]); f != nil && f.NoOptDefVal == "" {
			i++ // the option's value
		}
	}
	return args
}

func isCommand(root *cobra.Command, name string) bool {
	for _, cmd := range root.Commands() {
		if !cmd.Hidden && (cmd.Name() == name || slices.Contains(cmd.Aliases, name)) {
			return true
		}
	}
	return false
}

// commandName is a command's name as brew users type it.
func commandName(cmd *cobra.Command) string {
	if rest, ok := strings.CutPrefix(cmd.Name(), "__"); ok {
		return "--" + rest
	}
	return cmd.Name()
}

// namedFormulae resolves names and aliases the way brew's named arguments do:
// every name first, so an unknown one prints nothing.
func namedFormulae(ctx context.Context, e *engine.Engine, names []string) ([]model.Formula, error) {
	cat, err := e.Catalog(ctx)
	if err != nil {
		return nil, err
	}
	formulae := make([]model.Formula, 0, len(names))
	for _, name := range names {
		f, err := cat.Lookup(name)
		if err != nil {
			return nil, err
		}
		formulae = append(formulae, f)
	}
	return formulae, nil
}

func (a *application) lines(lines []string) {
	a.setResult(lines, func(w io.Writer) error {
		return writeLines(w, lines)
	})
}

func (a *application) pathCommands() []*cobra.Command {
	var installed, unbrewed bool
	prefix := &cobra.Command{
		Use: "__prefix [formula...]", Short: "Display the install prefix, or where a formula is or would be installed", Hidden: true,
		RunE: func(cmd *cobra.Command, names []string) error {
			if installed && len(names) == 0 {
				return usageError{errors.New("`--installed` requires a formula argument.")}
			}
			if unbrewed && len(names) > 0 {
				return usageError{errors.New("`--unbrewed` does not take a formula argument.")}
			}
			e, err := a.getEngine()
			if err != nil {
				return err
			}
			if unbrewed {
				files, err := e.Unbrewed()
				if err != nil {
					return err
				}
				a.lines(files)
				return nil
			}
			if len(names) == 0 {
				a.lines([]string{e.Prefix()})
				return nil
			}
			formulae, err := namedFormulae(cmd.Context(), e, names)
			if err != nil {
				return err
			}
			paths := []string{}
			var missing []string
			for _, f := range formulae {
				opt := filepath.Join(e.Prefix(), "opt", f.Name)
				link, linkErr := os.Lstat(opt)
				target, err := os.Stat(opt)
				if !installed || err == nil {
					paths = append(paths, opt)
				}
				// brew reports the formulae without an opt link to a directory.
				if installed && (linkErr != nil || link.Mode()&os.ModeSymlink == 0 || err != nil || !target.IsDir()) {
					missing = append(missing, f.Name)
				}
			}
			a.lines(paths)
			if len(missing) > 0 {
				return notFound("The following formulae are not installed:\n" + strings.Join(missing, "\n"))
			}
			return nil
		},
	}
	prefix.Flags().BoolVar(&installed, "installed", false, "Output nothing and fail if a named formula is not installed")
	prefix.Flags().BoolVar(&unbrewed, "unbrewed", false, "List files in the prefix not installed by gobrew")
	prefix.MarkFlagsMutuallyExclusive("installed", "unbrewed")

	cellar := &cobra.Command{
		Use: "__cellar [formula...]", Short: "Display the Cellar, or a formula's directory in it", Hidden: true,
		RunE: func(cmd *cobra.Command, names []string) error {
			e, err := a.getEngine()
			if err != nil {
				return err
			}
			root := filepath.Join(e.Prefix(), "Cellar")
			if len(names) == 0 {
				a.lines([]string{root})
				return nil
			}
			formulae, err := namedFormulae(cmd.Context(), e, names)
			if err != nil {
				return err
			}
			paths := make([]string, 0, len(formulae))
			for _, f := range formulae {
				paths = append(paths, filepath.Join(root, f.Name))
			}
			a.lines(paths)
			return nil
		},
	}

	var tag string
	cache := &cobra.Command{
		Use: "__cache [formula...]", Short: "Display the download cache, or the file that caches a formula's bottle", Hidden: true,
		RunE: func(cmd *cobra.Command, names []string) error {
			e, err := a.getEngine()
			if err != nil {
				return err
			}
			if len(names) == 0 {
				a.lines([]string{e.CacheDir()})
				return nil
			}
			formulae, err := namedFormulae(cmd.Context(), e, names)
			if err != nil {
				return err
			}
			type bottleCache struct {
				Formula string `json:"formula"`
				Tag     string `json:"tag"`
				Path    string `json:"path,omitempty"`
			}
			results := make([]bottleCache, 0, len(formulae))
			var paths []string
			for _, f := range formulae {
				path, wanted, ok, err := e.BottleCache(f, tag)
				if err != nil {
					return err
				}
				results = append(results, bottleCache{f.Name, wanted, path})
				if !ok {
					// gobrew has no source builds, so where brew would name
					// the source download this only warns.
					a.warn("Bottle for tag :" + wanted + " is unavailable.")
					continue
				}
				paths = append(paths, path)
			}
			a.lines(paths)
			a.result = results
			return nil
		},
	}
	cache.Flags().StringVar(&tag, "bottle-tag", "", "Show the bottle file for the given tag")
	cache.Flags().Bool("force-bottle", false, "Show the bottle file (gobrew always pours bottles)")
	cache.MarkFlagsMutuallyExclusive("force-bottle", "bottle-tag")
	for _, cmd := range []*cobra.Command{prefix, cellar, cache} {
		cmd.Flags().Bool("formula", false, "Treat all named arguments as formulae (gobrew has only formulae)")
		cmd.Flags().Bool("formulae", false, "Alias of --formula")
		_ = cmd.Flags().MarkHidden("formulae")
		cmd.Flags().BoolP("help", "h", false, "help for "+commandName(cmd))
	}
	return []*cobra.Command{prefix, cellar, cache}
}
