package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"slices"
	"strings"

	"github.com/shqingda/gobrew/internal/engine"
	"github.com/spf13/cobra"
)

// depsCommand mirrors brew deps [--direct] [--union] [--installed]
// [--topological] [--tree [--prune]] [--for-each] [formula...].
func (a *application) depsCommand() *cobra.Command {
	var opts engine.DepsOptions
	var tree, graph, dot, forEach bool
	cmd := a.commandFor("deps [formula...]", "Show dependencies of formulae (their intersection for several)", cobra.ArbitraryArgs, func(ctx context.Context, names []string) error {
		if dot && !graph {
			return usageError{errors.New("`--dot` cannot be passed without `--graph`")}
		}
		if opts.Prune && !tree {
			return usageError{errors.New("`--prune` cannot be passed without `--tree`")}
		}
		if (tree || graph) && len(names) == 0 && !opts.Installed {
			return usageError{errors.New("this command requires a formula argument")}
		}
		e, err := a.getEngine()
		if err != nil {
			return err
		}
		if os.Getenv("GOBREW_NO_ENV_HINTS") == "" {
			snapshot, _ := e.Installed()
			allInstalled := !slices.ContainsFunc(names, func(name string) bool {
				_, ok := snapshot.Records[strings.TrimPrefix(name, "homebrew/core/")]
				return !ok
			})
			if reason := declaredReason(opts, tree, graph, allInstalled); reason != "" {
				a.warn(fmt.Sprintf("`gobrew deps` is not the actual runtime dependencies because %s!\nThis means dependencies may differ from a formula's declared dependencies.\nHide these hints with `GOBREW_NO_ENV_HINTS=1` (see `gobrew help environment`).", reason))
			}
		}
		switch {
		case tree:
			roots, err := e.DepsTree(ctx, names, opts)
			if roots != nil {
				a.setResult(roots, func(w io.Writer) error { return renderDepsTree(w, roots, opts.Annotate) })
			}
			return err
		case graph:
			edges, err := e.DepsGraph(ctx, names, opts)
			if err != nil {
				return err
			}
			a.setResult(edges, func(w io.Writer) error {
				code := dotCode(edges)
				if dot {
					_, err := fmt.Fprintln(w, cleanText(code))
					return err
				}
				return openURLs(ctx, graphvizURL(code))
			})
			return nil
		case forEach || len(names) == 0:
			each, err := e.DepsEach(ctx, names, opts)
			if err != nil {
				return err
			}
			a.setResult(each, func(w io.Writer) error {
				for _, item := range each {
					if _, err := fmt.Fprintln(w, cleanText(item.Name+": "+strings.Join(item.Dependencies, " "))); err != nil {
						return err
					}
				}
				return nil
			})
			return nil
		}
		deps, err := e.Deps(ctx, names, opts)
		if err != nil {
			return err
		}
		a.names(deps, "")
		return nil
	})
	cmd.Flags().BoolVarP(&opts.Direct, "direct", "1", false, "Show only direct dependencies")
	cmd.Flags().BoolVar(&opts.Union, "union", false, "Show the union of dependencies for several formulae")
	cmd.Flags().BoolVar(&opts.Installed, "installed", false, "Show only installed dependencies; without formulae, cover every installed formula")
	cmd.Flags().BoolVarP(&opts.Topological, "topological", "n", false, "Sort dependencies in topological order")
	cmd.Flags().BoolVar(&tree, "tree", false, "Show dependencies as a tree, one for each formula")
	cmd.Flags().BoolVar(&opts.Prune, "prune", false, "Prune parts of a tree already seen")
	cmd.Flags().BoolVar(&graph, "graph", false, "Show dependencies as a directed graph in the browser")
	cmd.Flags().BoolVar(&dot, "dot", false, "Print the graph in DOT format instead")
	cmd.Flags().BoolVar(&opts.IncludeBuild, "include-build", false, "Include build dependencies")
	cmd.Flags().BoolVar(&opts.IncludeTest, "include-test", false, "Include test dependencies (not recursive unless --graph or --tree)")
	cmd.Flags().BoolVar(&opts.IncludeOptional, "include-optional", false, "Include optional dependencies")
	cmd.Flags().BoolVar(&opts.IncludeImplicit, "include-implicit", false, "Include dependencies used to unpack source files")
	cmd.Flags().Bool("include-requirements", false, "Accepted like brew; official formulae list no requirements")
	cmd.Flags().BoolVar(&opts.SkipRecommended, "skip-recommended", false, "Skip recommended dependencies")
	cmd.Flags().BoolVar(&opts.Missing, "missing", false, "Show only missing dependencies")
	cmd.Flags().BoolVar(&opts.Annotate, "annotate", false, "Mark build, test, optional and recommended dependencies")
	cmd.Flags().BoolVar(&forEach, "for-each", false, "List dependencies one formula per line")
	// Accepted and ignored: an official formula's full name is its name.
	cmd.Flags().Bool("full-name", false, "List dependencies by full name (official formulae have no longer one)")
	// brew's other spellings of --direct.
	for _, alias := range []string{"declared", "1"} {
		cmd.Flags().BoolVar(&opts.Direct, alias, false, "Show only direct dependencies")
		_ = cmd.Flags().MarkHidden(alias)
	}
	cmd.MarkFlagsMutuallyExclusive("tree", "graph")
	cmd.MarkFlagsMutuallyExclusive("installed", "missing")
	return cmd
}

// declaredReason is why brew deps reads declared dependencies, if it does; when
// several options apply, brew names the last in this order.
func declaredReason(opts engine.DepsOptions, tree, graph, allInstalled bool) string {
	reason := ""
	if !allInstalled && !opts.Installed {
		reason = "`--installed` was not passed"
	}
	for _, option := range []struct {
		set  bool
		name string
	}{
		{opts.Direct, "direct"}, {tree, "tree"}, {graph, "graph"}, {opts.SkipRecommended, "skip-recommended"}, {opts.Missing, "missing"},
		{opts.IncludeImplicit, "include-implicit"}, {opts.IncludeBuild, "include-build"}, {opts.IncludeTest, "include-test"}, {opts.IncludeOptional, "include-optional"},
	} {
		if option.set {
			reason = "--" + option.name + " was passed"
		}
	}
	return reason
}

// dotCode ports brew deps --graph --dot.
func dotCode(edges []engine.DepEdge) string {
	lines := make([]string, len(edges))
	for i, edge := range edges {
		dep := edge.Dependency
		var attributes, tags []string
		if dep.Build {
			attributes = append(attributes, "style = dotted")
			tags = append(tags, ":build")
		}
		if dep.Test {
			attributes = append(attributes, "arrowhead = empty")
			tags = append(tags, ":test")
		}
		if dep.Recommended {
			tags = append(tags, ":recommended")
		}
		if dep.Optional {
			attributes = append(attributes, "color = red")
			tags = append(tags, ":optional")
		} else if dep.Recommended {
			attributes = append(attributes, "color = green")
		}
		if dep.Implicit {
			tags = append(tags, ":implicit")
		}
		if dep.NoLinkage {
			tags = append(tags, ":no_linkage")
		}
		lines[i] = fmt.Sprintf("  %q -> %q", edge.From, dep.Name)
		if len(attributes) > 0 {
			lines[i] += " [" + strings.Join(attributes, ", ") + "]"
		}
		if len(tags) > 0 {
			lines[i] += " # " + strings.Join(tags, ", ")
		}
	}
	return "digraph {\n" + strings.Join(lines, "\n") + "\n}"
}

// graphvizURL is where brew deps --graph opens a graph. The code stays in the
// URL fragment, which browsers do not send to the site.
func graphvizURL(code string) string {
	return "https://dreampuf.github.io/GraphvizOnline/#" + strings.ReplaceAll(url.QueryEscape(code), "+", "%20")
}

// renderDepsTree follows brew deps --tree: each tree ends with a blank line, and
// --annotate adds a space to every name, tagged or not.
func renderDepsTree(w io.Writer, roots []engine.DepNode, annotate bool) error {
	var walk func(nodes []engine.DepNode, prefix string) error
	walk = func(nodes []engine.DepNode, prefix string) error {
		for i, node := range nodes {
			branch, indent := "├── ", "│   "
			if i == len(nodes)-1 {
				branch, indent = "└── ", "    "
			}
			mark := ""
			if node.Circular {
				mark = " (CIRCULAR DEPENDENCY)"
			} else if node.Pruned {
				mark = " (PRUNED)"
			}
			label := node.Name
			if annotate {
				label += " "
			}
			for _, tag := range node.Tags {
				label += " [" + tag + "]"
			}
			if _, err := fmt.Fprintln(w, cleanText(prefix+branch+label+mark)); err != nil {
				return err
			}
			if err := walk(node.Dependencies, prefix+indent); err != nil {
				return err
			}
		}
		return nil
	}
	for _, root := range roots {
		if _, err := fmt.Fprintln(w, cleanText(root.Name)); err != nil {
			return err
		}
		if err := walk(root.Dependencies, ""); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(w); err != nil {
			return err
		}
	}
	return nil
}

// usesCommand mirrors brew uses [--installed] [--recursive] formula...
func (a *application) usesCommand() *cobra.Command {
	var opts engine.DepsOptions
	var recursive bool
	cmd := a.commandFor("uses <formula>...", "Show formulae that depend on formulae (their intersection for several)", cobra.MinimumNArgs(1), func(ctx context.Context, names []string) error {
		e, err := a.getEngine()
		if err != nil {
			return err
		}
		opts.Direct = !recursive
		if !opts.Installed {
			if err := a.ensureCatalog(ctx, e); err != nil {
				return err
			}
		}
		uses, err := e.Uses(ctx, names, opts)
		if err != nil {
			return err
		}
		a.names(uses, "")
		return nil
	})
	cmd.Flags().BoolVar(&opts.Installed, "installed", false, "Only list installed formulae, without network access")
	cmd.Flags().BoolVar(&opts.Missing, "missing", false, "Only list formulae that are not installed")
	cmd.Flags().BoolVar(&recursive, "recursive", false, "Resolve more than one level of dependencies")
	cmd.Flags().BoolVar(&opts.IncludeBuild, "include-build", false, "Include formulae that need it to build")
	cmd.Flags().BoolVar(&opts.IncludeTest, "include-test", false, "Include formulae that need it to test")
	cmd.Flags().BoolVar(&opts.IncludeOptional, "include-optional", false, "Include formulae that use it optionally")
	cmd.Flags().BoolVar(&opts.IncludeImplicit, "include-implicit", false, "Include formulae that need it to unpack source files")
	cmd.Flags().BoolVar(&opts.SkipRecommended, "skip-recommended", false, "Skip formulae that only recommend it")
	cmd.MarkFlagsMutuallyExclusive("installed", "missing")
	return cmd
}
