package engine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/shqingda/gobrew/internal/model"
	"github.com/shqingda/gobrew/internal/platform"
)

// SystemPrefix is the default Formula prefix and the only directory the
// privileged bootstrap may create.
const SystemPrefix = "/opt/gobrew"

// Initialized reports whether the prefix already has a state database. It is a
// single stat so the installed no-op path stays cheap.
func (e *Engine) Initialized() (bool, error) {
	return e.store().Exists()
}

// Init creates or completes a user-owned prefix. It is idempotent and safe to
// run concurrently. When the parent directory is not writable it returns
// model.ErrPrivilegeRequired; obtaining privilege belongs to the caller. It
// refuses to run as root so the prefix is never owned by root.
func (e *Engine) Init() error {
	if _, err := e.Host(); err != nil {
		return err
	}
	prefix := filepath.Clean(e.config.Prefix)
	if !filepath.IsAbs(prefix) {
		return errors.New("prefix must be absolute")
	}
	if os.Geteuid() == 0 {
		return errors.New("run gobrew as your normal user, without sudo")
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
	if owner, ok := platform.OwnerUID(info); ok && owner != uid {
		return fmt.Errorf("%s is owned by another user; gobrew never takes over existing directories", prefix)
	}
	if ok, err := e.Initialized(); err != nil || ok {
		return err
	}
	// A crash or a concurrent Init can leave the layout without a database.
	return e.store().Pristine()
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
