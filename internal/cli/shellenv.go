package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/shqingda/gobrew/internal/shellconfig"
	"github.com/spf13/cobra"
)

func (a *application) shellenvCommand() *cobra.Command {
	var shell string
	cmd := a.commandFor("shellenv [shell]", "Print shell commands that put gobrew tools on PATH", cobra.MaximumNArgs(1), func(_ context.Context, args []string) error {
		// Like brew, an explicit shell wins and $SHELL is only a fallback.
		if len(args) == 1 {
			shell = args[0]
		} else if shell == "" {
			shell = filepath.Base(os.Getenv("SHELL"))
		}
		e, err := a.getEngine()
		if err != nil {
			return err
		}
		prefix := e.Prefix()
		env := shellconfig.Env{Paths: []string{filepath.Join(prefix, "bin"), filepath.Join(prefix, "sbin")}, Prefix: prefix}
		// Like brew's shellenv, which puts brew itself on PATH, include the
		// managed command directory so the evaluated line also finds gobrew.
		if bin := managedBinDir(); bin != "" {
			env.Paths = append([]string{bin}, env.Paths...)
		}
		script, err := env.Script(shell)
		if err != nil {
			return usageError{err}
		}
		a.setResult(map[string]string{"shell": shell, "script": script}, func(w io.Writer) error { _, err := io.WriteString(w, script); return err })
		return nil
	})
	// --shell predates the positional form that brew shellenv uses.
	cmd.Flags().StringVar(&shell, "shell", "", "Shell syntax")
	_ = cmd.Flags().MarkHidden("shell")
	cmd.Example = "  eval \"$(gobrew shellenv zsh)\""
	return cmd
}

// openURLs shows pages in the default browser, as brew does with exec_browser.
// Its failure is reported as such, not as a failure to write the output.
func openURLs(ctx context.Context, urls ...string) error {
	if err := exec.CommandContext(ctx, "/usr/bin/open", urls...).Run(); err != nil {
		return actionError{fmt.Errorf("could not open %s: %w", strings.Join(urls, " "), err)}
	}
	return nil
}
