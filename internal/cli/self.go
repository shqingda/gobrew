package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/shqingda/gobrew/internal/platform"
	"github.com/shqingda/gobrew/internal/selfupdate"
	"github.com/spf13/cobra"
)

// selfCommand manages the CLI executable. It is the one command brew does not
// have; see decision 0003.
func (a *application) selfCommand() *cobra.Command {
	root := &cobra.Command{
		Use: "self", Short: "Install, update or remove the gobrew CLI itself", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	subcommand := func(use, description string, run func(context.Context) error) *cobra.Command {
		return &cobra.Command{Use: use, Short: description, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			a.command = "self " + use
			if _, err := platform.Detect(); err != nil {
				return err
			}
			return run(cmd.Context())
		}}
	}
	binDir := ""
	install := subcommand("install", "Install this executable and the gob alias in your personal bin directory", func(ctx context.Context) error {
		dir := binDir
		if dir == "" {
			var err error
			if dir, err = selfupdate.DefaultBinDir(); err != nil {
				return err
			}
		}
		cli, err := a.installCLI(ctx, dir)
		if err != nil {
			return err
		}
		a.setResult(cli, func(w io.Writer) error { return writeInstalled(w, cli) })
		return nil
	})
	install.Flags().StringVar(&binDir, "bin-dir", "", "Command directory (default: ~/.local/bin)")
	update := subcommand("update", "Update the CLI from the latest verified official release", func(ctx context.Context) error {
		manager, err := selfupdate.Running()
		if err != nil {
			return err
		}
		result, err := manager.Update(ctx)
		if err != nil {
			return err
		}
		message := "gobrew is up to date: " + result.Version
		if result.Changed {
			message = "Updated gobrew to " + result.Version
		}
		a.message(result, message)
		return nil
	})
	uninstall := subcommand("uninstall", "Remove managed CLI binaries and links; retain all Formulae, configuration and caches", func(ctx context.Context) error {
		manager, err := selfupdate.Running()
		if err != nil {
			return err
		}
		result, err := manager.Uninstall(ctx)
		if err != nil {
			return err
		}
		message := "Removed gobrew and gob. Installed Formulae, configuration and caches are retained. Small ownership and lock records remain for safe retries."
		if note := a.removePath(); note != "" {
			message += "\n" + note
		}
		a.message(result, message)
		return nil
	})
	root.AddCommand(install, update, uninstall)
	return root
}

func writeInstalled(w io.Writer, cli cliUpdate) error {
	message := "Installed gobrew " + cli.Version + " and gob → gobrew in " + cli.BinDir + "."
	if cli.Note != "" {
		message += "\n" + cli.Note
	}
	_, err := fmt.Fprintln(w, cleanText(message))
	return err
}
