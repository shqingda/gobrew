package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/shqingda/gobrew/internal/engine"
	"github.com/shqingda/gobrew/internal/model"
)

// ensureInitialized makes the first install set up the prefix; there is no
// separate init command. Already initialized prefixes cost one stat.
func (a *application) ensureInitialized(ctx context.Context, e *engine.Engine) error {
	ok, err := e.Initialized()
	if err != nil || ok {
		return err
	}
	return a.initialize(ctx, e)
}

// createOwnedPrefix runs as root through sudo. It creates a missing parent
// (such as /opt on a fresh system), refuses a symlinked parent, creates only a
// missing prefix and hands just that directory to the invoking user, removing
// it again if the handover fails. Existing ownership is never changed. No
// gobrew code, download or package code runs as root.
const createOwnedPrefix = `set -eu
parent=${3%/*}
[ -L "$parent" ] && exit 1
[ -d "$parent" ] || /bin/mkdir -m 0755 "$parent"
/bin/mkdir -m 0755 "$3"
/usr/sbin/chown "$1:$2" "$3" || { /bin/rmdir "$3"; exit 1; }
/bin/sync`

// initialize runs Init and, when only administrator access is missing for the
// default prefix, creates it once through sudo. sudo reads the password from
// the controlling terminal, never from our stdin, so this also works under
// curl | sh. Everything else stays unprivileged.
func (a *application) initialize(ctx context.Context, e *engine.Engine) error {
	err := e.Init()
	if !errors.Is(err, model.ErrPrivilegeRequired) || e.Prefix() != a.systemPrefix {
		return err
	}
	uid, gid := strconv.Itoa(os.Getuid()), strconv.Itoa(os.Getgid())
	manual := "sudo /bin/mkdir -p " + shellQuote(e.Prefix()) + " && sudo /usr/sbin/chown " + uid + ":" + gid + " " + shellQuote(e.Prefix())
	if a.json || !a.terminal() {
		return fmt.Errorf("%w; run once in a terminal: %s", err, manual)
	}
	fmt.Fprintf(a.stderr, "gobrew needs administrator access once to create %s and hand it to you.\nNo downloads or package code run as root.\n", e.Prefix())
	elevateErr := a.elevate(ctx, a.stderr, []string{"/bin/sh", "-c", createOwnedPrefix, "sh", uid, gid, filepath.Clean(e.Prefix())})
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

func sudo(ctx context.Context, stderr io.Writer, command []string) error {
	// An absolute sudo avoids PATH lookups; stdin stays closed so a piped
	// installer script is never consumed as a password.
	cmd := exec.CommandContext(ctx, "/usr/bin/sudo", append([]string{"-p", "Password for %u: ", "--"}, command...)...)
	cmd.Stdout = io.Discard
	cmd.Stderr = stderr
	// SIGTERM lets sudo forward cancellation to the root helper; SIGKILL would
	// orphan it. The helper only performs a few short directory operations.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 5 * time.Second
	return cmd.Run()
}
