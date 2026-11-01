package engine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/shqingda/gobrew/internal/model"
	"github.com/shqingda/gobrew/internal/platform"
)

// SystemPrefix is the default Formula prefix and the only directory the
// privileged bootstrap may create.
const SystemPrefix = "/opt/gobrew"

// Initialized reports whether the prefix already has a state database. It is a
// single stat so the installed no-op path stays cheap.
func (e *Engine) Initialized() (bool, error) {
	_, err := os.Lstat(filepath.Join(e.config.Prefix, ".gobrew", "state.db"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// Init creates or completes a user-owned prefix. It is idempotent and safe to
// run concurrently. When the parent directory is not writable it returns
// model.ErrPrivilegeRequired; obtaining privilege belongs to the caller. Run as
// root, it only performs the privileged half for SystemPrefix.
func (e *Engine) Init() error {
	if _, err := e.Host(); err != nil {
		return err
	}
	prefix := filepath.Clean(e.config.Prefix)
	if !filepath.IsAbs(prefix) {
		return errors.New("prefix must be absolute")
	}
	if os.Geteuid() == 0 {
		if prefix != SystemPrefix {
			return errors.New("root bootstrap is restricted to " + SystemPrefix)
		}
		uid, err := strconv.Atoi(os.Getenv("SUDO_UID"))
		if err != nil || uid <= 0 {
			return errors.New("bootstrap requires the invoking non-root SUDO_UID")
		}
		gid, err := strconv.Atoi(os.Getenv("SUDO_GID"))
		if err != nil || gid < 0 {
			return errors.New("bootstrap requires SUDO_GID")
		}
		// No network, package code or database runs in the privileged process.
		return createOwnedPrefix(prefix, uid, gid)
	}
	// Mkdir first: it reports an existing path before checking write access,
	// and makes concurrent initializers agree on a single creator.
	err := os.Mkdir(prefix, 0755)
	switch {
	case err == nil:
	case errors.Is(err, os.ErrExist):
		if err = e.checkOwnedPrefix(prefix, os.Getuid()); err != nil {
			return err
		}
	case errors.Is(err, os.ErrPermission), prefix == SystemPrefix && errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("%w: cannot create %s: %v", model.ErrPrivilegeRequired, prefix, err)
	default:
		return fmt.Errorf("create prefix: %w", err)
	}
	return e.store().Initialize()
}

func (e *Engine) checkOwnedPrefix(prefix string, uid int) error {
	info, err := os.Lstat(prefix)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("prefix must be a real directory")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && int(stat.Uid) != uid {
		return fmt.Errorf("%s is owned by another user; gobrew never takes over existing directories", prefix)
	}
	if ok, err := e.Initialized(); err != nil || ok {
		return err
	}
	// A crash or a concurrent Init can leave the layout without a database.
	return e.store().Pristine()
}

// createOwnedPrefix is the privileged half of Init. It creates a missing parent
// (such as /opt on a fresh system, left owned by root) and the missing prefix,
// then hands only that new prefix to the invoking user. An existing prefix is
// accepted only if that user already owns it; nothing is ever chowned twice or
// adopted, and a prefix whose handover failed is removed again.
func createOwnedPrefix(prefix string, uid, gid int) error {
	parent := filepath.Dir(prefix)
	if err := os.Mkdir(parent, 0755); err == nil {
		if err = platform.SyncDir(filepath.Dir(parent)); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := os.Lstat(parent)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("unsafe %s directory", parent)
	}
	if err = os.Mkdir(prefix, 0755); errors.Is(err, os.ErrExist) {
		info, err = os.Lstat(prefix)
		if err != nil {
			return err
		}
		if stat, ok := info.Sys().(*syscall.Stat_t); ok && info.IsDir() && info.Mode()&os.ModeSymlink == 0 && int(stat.Uid) == uid {
			return nil
		}
		return errors.New("root bootstrap only creates a missing prefix; it never changes existing ownership")
	} else if err != nil {
		return err
	}
	if err = os.Chown(prefix, uid, gid); err != nil {
		return errors.Join(err, os.Remove(prefix))
	}
	return platform.SyncDir(parent)
}

func (e *Engine) checkMutation() error {
	if os.Geteuid() == 0 {
		return errors.New("package operations must not run as root")
	}
	if ok, err := e.Initialized(); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("%w: nothing is installed in %s", model.ErrNotFound, e.config.Prefix)
	}
	return nil
}
