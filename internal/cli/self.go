package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/shqingda/gobrew/internal/platform"
	"github.com/shqingda/gobrew/internal/selfupdate"
	"github.com/spf13/cobra"
)

func (a *application) selfCommand() *cobra.Command {
	root := &cobra.Command{Use: "self", Short: "Install, update or remove the gobrew CLI itself"}
	binDir := ""
	install := &cobra.Command{Use: "install", Short: "Install this executable and the gob alias in your personal bin directory", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		a.command = "self install"
		if _, err := platform.Detect(); err != nil {
			return err
		}
		var err error
		if binDir == "" {
			binDir, err = selfupdate.DefaultBinDir()
			if err != nil {
				return err
			}
		}
		if !filepath.IsAbs(binDir) {
			return usageError{fmt.Errorf("--bin-dir must be absolute")}
		}
		binDir = filepath.Clean(binDir)
		source, err := os.Executable()
		if err != nil {
			return err
		}
		source, err = filepath.EvalSymlinks(source)
		if err != nil {
			return err
		}
		result, err := (selfupdate.Manager{BinDir: binDir}).Install(cmd.Context(), source, a.build.Version)
		if err != nil {
			return err
		}
		a.message(result, fmt.Sprintf("Installed gobrew %s and gob → gobrew in %s.\nAdd this directory to PATH if needed.\nThe first gob install sets up Formula storage, asking once for an administrator password if needed.", result.Version, result.BinDir))
		return nil
	}}
	install.Flags().StringVar(&binDir, "bin-dir", "", "Command directory (default: ~/.local/bin)")
	root.AddCommand(install)
	for _, action := range []string{"update", "uninstall"} {
		description := "Update the CLI from the latest verified official release"
		if action == "uninstall" {
			description = "Remove managed CLI binaries and links; retain all Formulae, configuration and caches"
		}
		root.AddCommand(&cobra.Command{Use: action, Short: description, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			a.command = "self " + action
			if _, err := platform.Detect(); err != nil {
				return err
			}
			manager, err := selfupdate.Running()
			if err != nil {
				return err
			}
			var result selfupdate.Result
			if action == "update" {
				result, err = manager.Update(cmd.Context())
			} else {
				result, err = manager.Uninstall(cmd.Context())
			}
			if err != nil {
				return err
			}
			message := "gobrew is up to date: " + result.Version
			if result.Changed {
				message = "Updated gobrew to " + result.Version
			}
			if action == "uninstall" {
				message = "Removed gobrew and gob. Installed Formulae, configuration and caches are retained. Small ownership and lock records remain for safe retries."
			}
			a.message(result, message)
			return nil
		}})
	}
	return root
}
