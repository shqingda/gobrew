package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/shqingda/gobrew/internal/engine"
	"github.com/shqingda/gobrew/internal/model"
)

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
