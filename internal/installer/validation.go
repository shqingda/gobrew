package installer

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/shqingda/gobrew/internal/model"
	"github.com/shqingda/gobrew/internal/state"
	"golang.org/x/sys/unix"
)

// kegPath confines journal moves and deletions to complete kegs below known
// roots. Checking both parents and the leaf rejects directory aliases before
// filesystem operations; cooperating writers hold the prefix lock throughout.
func (i Installer) kegPath(path string) (string, error) {
	rel, err := filepath.Rel(i.Prefix, path)
	if err != nil || !filepath.IsLocal(rel) {
		return "", fmt.Errorf("%w: keg path is outside prefix: %s", model.ErrConflict, path)
	}
	parts := strings.Split(rel, string(filepath.Separator))
	allowed := len(parts) == 3 && parts[0] == "Cellar"
	if len(parts) >= 4 && parts[0] == ".gobrew" {
		allowed = parts[1] == "staging" || parts[1] == "backups" || parts[1] == "trash"
	}
	if !allowed {
		return "", fmt.Errorf("%w: invalid keg location: %s", model.ErrConflict, path)
	}
	return state.SecureDir(i.Prefix, rel)
}

func readOwner(keg string) (string, error) {
	info, err := os.Lstat(keg)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("%w: keg is not a real directory: %s", model.ErrConflict, keg)
	}
	root, err := os.OpenRoot(keg)
	if err != nil {
		return "", err
	}
	defer root.Close()
	file, err := root.OpenFile(ownerFile, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > 4096 {
		return "", fmt.Errorf("%w: invalid ownership marker in %s", model.ErrConflict, keg)
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Nlink != 1 {
		return "", fmt.Errorf("%w: ownership marker is hardlinked in %s", model.ErrConflict, keg)
	}
	data, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil {
		return "", err
	}
	if len(data) > 4096 {
		return "", fmt.Errorf("%w: ownership marker is too large", model.ErrConflict)
	}
	return string(data), nil
}

func (i Installer) removeOwned(path, owner string) error {
	checked, err := i.kegPath(path)
	if err != nil {
		return err
	}
	if !owns(checked, owner) {
		return fmt.Errorf("%w: keg ownership changed at %s", model.ErrConflict, path)
	}
	return os.RemoveAll(checked)
}

// ValidateState checks existing dependencies without hashing package contents
// or writing state. Apply repeats it under the writer lock, after recovery.
func (i Installer) ValidateState(prepared model.Prepared, snap model.Snapshot) error {
	replacing := make(map[string]bool)
	names := append([]string(nil), prepared.Plan.Unchanged...)
	names = append(names, prepared.Plan.Promote...)
	for _, pkg := range prepared.Plan.Packages {
		replacing[pkg.Formula.Name] = true
		names = append(names, pkg.Formula.Dependencies...)
	}
	for _, pkg := range prepared.Packages {
		replacing[pkg.Package.Formula.Name] = true
		names = append(names, pkg.Package.Formula.Dependencies...)
		for _, dep := range pkg.RuntimeDependencies {
			names = append(names, dep.Name)
		}
	}
	seen := make(map[string]bool)
	for len(names) > 0 {
		name := strings.TrimPrefix(names[len(names)-1], "homebrew/core/")
		names = names[:len(names)-1]
		if seen[name] || replacing[name] {
			continue
		}
		seen[name] = true
		rec, ok := snap.Records[name]
		if !ok {
			return fmt.Errorf("%w: missing installed dependency %s", model.ErrNeedsRepair, name)
		}
		if rec.Status != model.Ready {
			return fmt.Errorf("%w: %s has status %s", model.ErrNeedsRepair, name, rec.Status)
		}
		if rec.Formula.Name != name || !validPart(name) || !validOwner(rec.Owner, name) || !validPart(rec.Formula.PkgVersion()) || rec.Keg != filepath.Join(i.Prefix, "Cellar", name, rec.Formula.PkgVersion()) {
			return fmt.Errorf("%w: invalid installed keg for %s", model.ErrNeedsRepair, name)
		}
		if _, err := i.kegPath(rec.Keg); err != nil {
			return fmt.Errorf("%w: %s: %v", model.ErrNeedsRepair, name, err)
		}
		if !owns(rec.Keg, rec.Owner) {
			return fmt.Errorf("%w: missing or unowned keg for %s", model.ErrNeedsRepair, name)
		}
		if len(rec.Dependencies) == 0 {
			names = append(names, rec.Formula.Dependencies...)
		} else {
			for _, dep := range rec.Dependencies {
				names = append(names, dep.Name)
			}
		}
	}
	return nil
}

func (i Installer) ensureParent(path string) error {
	rel, err := filepath.Rel(i.Prefix, filepath.Dir(path))
	if err != nil {
		return err
	}
	_, err = state.EnsureDir(i.Prefix, rel, 0755)
	return err
}
