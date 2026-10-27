// Package selfupdate manages the CLI executable separately from Formula state.
package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"time"

	"github.com/shqingda/gobrew/internal/model"
	"github.com/shqingda/gobrew/internal/platform"
	"golang.org/x/sys/unix"
)

var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var versionPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)

const owner = "github.com/shqingda/gobrew/cli-v1"

type Result struct {
	Version string `json:"version,omitempty"`
	BinDir  string `json:"bin_dir"`
	Changed bool   `json:"changed"`
	Removed bool   `json:"removed,omitempty"`
}

// The receipt is also a redo journal. It is persisted before changing links.
// An empty Active means uninstall; retain the receipt and lock for safe retries.
type receipt struct {
	Owner   string   `json:"owner"`
	Active  string   `json:"active"`
	Version string   `json:"version"`
	Files   []string `json:"files"`
}

type Manager struct{ BinDir string }

func DefaultBinDir() (string, error) {
	home, err := os.UserHomeDir()
	return filepath.Join(home, ".local", "bin"), err
}

// Running only accepts executables inside the managed content-addressed store.
// A source build or a manually unpacked executable is never silently overwritten.
func Running() (Manager, error) {
	exe, err := os.Executable()
	if err != nil {
		return Manager{}, err
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return Manager{}, err
	}
	versions := filepath.Dir(exe)
	root := filepath.Dir(versions)
	if filepath.Base(versions) != "versions" || filepath.Base(root) != ".gobrew" || !digestPattern.MatchString(filepath.Base(exe)) {
		return Manager{}, fmt.Errorf("%w: unmanaged executable; run 'gobrew self install' first", model.ErrUnsupported)
	}
	return Manager{BinDir: filepath.Dir(root)}, nil
}

func (m Manager) root() string { return filepath.Join(m.BinDir, ".gobrew") }

// Check each ancestor without following symlinks, then create missing directories.
// The bin directory and private store must belong to this user and cannot be shared-writable.
func (m Manager) directories() error {
	if !filepath.IsAbs(m.BinDir) || filepath.Clean(m.BinDir) != m.BinDir || m.BinDir == "/" {
		return errors.New("bin directory must be a clean absolute path below root")
	}
	for _, path := range []string{m.BinDir, m.root(), filepath.Join(m.root(), "versions")} {
		if err := makeDirectory(path); err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		var st unix.Stat_t
		if err := unix.Lstat(path, &st); err != nil {
			return err
		}
		if int(st.Uid) != os.Getuid() || info.Mode().Perm()&0022 != 0 {
			return fmt.Errorf("unsafe owner or permissions: %s", path)
		}
	}
	return nil
}

func makeDirectory(path string) error {
	if path == "/" {
		return nil
	}
	if err := makeDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(path, 0755); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		if err := platform.SyncDir(filepath.Dir(path)); err != nil {
			return err
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("not a real directory: %s", path)
	}
	return nil
}

func (m Manager) lock(ctx context.Context) (func() error, error) {
	if os.Geteuid() == 0 {
		return nil, errors.New("self management must run as your normal user, not root")
	}
	if err := m.directories(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return platform.AcquireLock(ctx, filepath.Join(m.root(), "lock"))
}

func (m Manager) read() (receipt, error) {
	var r receipt
	path := filepath.Join(m.root(), "receipt.json")
	info, err := os.Lstat(path)
	if err != nil {
		return r, err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return r, errors.New("invalid CLI ownership receipt")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return r, err
	}
	if r.Owner != owner || (r.Active != "" && !digestPattern.MatchString(r.Active)) {
		return r, errors.New("foreign CLI ownership receipt")
	}
	found := r.Active == ""
	seen := map[string]bool{}
	for _, hash := range r.Files {
		if !digestPattern.MatchString(hash) || seen[hash] {
			return r, errors.New("invalid CLI file ownership")
		}
		seen[hash] = true
		if hash == r.Active {
			found = true
		}
	}
	if !found {
		return r, errors.New("unowned active executable")
	}
	return r, nil
}

func (m Manager) write(r receipt) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return atomicFile(m.root(), "receipt.json", data, 0600)
}

func atomicFile(dir, name string, data []byte, mode os.FileMode) (err error) {
	f, err := os.CreateTemp(dir, ".stage-")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err = f.Write(data); err == nil {
		err = f.Chmod(mode)
	}
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	if err = os.Rename(f.Name(), filepath.Join(dir, name)); err != nil {
		return err
	}
	return platform.SyncDir(dir)
}

func hashFile(path string) (string, error) {
	// O_NOFOLLOW prevents an owned filename from being substituted with a link.
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return "", err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > 64<<20 {
		return "", errors.New("invalid CLI executable")
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func linkTarget(path string) (string, error) {
	target, err := os.Readlink(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("%w: refusing to replace %s", model.ErrConflict, path)
	}
	return target, nil
}

func (m Manager) checkLinks(r receipt) error {
	for name, expected := range map[string]string{"gobrew": ".gobrew/current", "gob": "gobrew"} {
		got, err := linkTarget(filepath.Join(m.BinDir, name))
		if err != nil {
			return err
		}
		if got != "" && (r.Owner != owner || got != expected) {
			return fmt.Errorf("%w: foreign command %s", model.ErrConflict, name)
		}
	}
	got, err := linkTarget(filepath.Join(m.root(), "current"))
	if err != nil {
		return err
	}
	if got != "" {
		owned := false
		for _, hash := range r.Files {
			if got == "versions/"+hash {
				owned = true
			}
		}
		if !owned {
			return fmt.Errorf("%w: foreign current link", model.ErrConflict)
		}
	}
	return nil
}

func setLink(path, target string, allowed ...string) error {
	got, err := linkTarget(path)
	if err != nil {
		return err
	}
	if got != "" && !slices.Contains(allowed, got) {
		return fmt.Errorf("%w: link changed ownership: %s", model.ErrConflict, path)
	}
	if got == target {
		return platform.SyncDir(filepath.Dir(path))
	}
	if target == "" {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	} else if got == "" {
		// Initial publication must be exclusive, even if another tool creates
		// the command between the ownership check and this syscall.
		if err := os.Symlink(target, path); err != nil {
			return err
		}
	} else {
		// A private temporary directory avoids predictable temporary-link collisions.
		dir, err := os.MkdirTemp(filepath.Dir(path), ".link-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		tmp := filepath.Join(dir, "link")
		if err := os.Symlink(target, tmp); err != nil {
			return err
		}
		if err := os.Rename(tmp, path); err != nil {
			return err
		}
	}
	return platform.SyncDir(filepath.Dir(path))
}

// reconcile replays only declared, owned link operations. It never runs binaries.
func (m Manager) reconcile(r receipt) error {
	if err := m.checkLinks(r); err != nil {
		return err
	}
	currentTargets := make([]string, 0, len(r.Files))
	for _, hash := range r.Files {
		currentTargets = append(currentTargets, "versions/"+hash)
	}
	if r.Active != "" {
		hash, err := hashFile(filepath.Join(m.root(), "versions", r.Active))
		if err != nil {
			return err
		}
		if hash != r.Active {
			return errors.New("managed executable checksum mismatch")
		}
		if err := setLink(filepath.Join(m.root(), "current"), "versions/"+r.Active, currentTargets...); err != nil {
			return err
		}
		for _, item := range [][2]string{{"gobrew", ".gobrew/current"}, {"gob", "gobrew"}} {
			if err := setLink(filepath.Join(m.BinDir, item[0]), item[1], item[1]); err != nil {
				return err
			}
		}
	} else {
		for _, item := range [][2]string{{"gob", "gobrew"}, {"gobrew", ".gobrew/current"}} {
			if err := setLink(filepath.Join(m.BinDir, item[0]), "", item[1]); err != nil {
				return err
			}
		}
		if err := setLink(filepath.Join(m.root(), "current"), "", currentTargets...); err != nil {
			return err
		}
	}
	// Retain changed or unknown files; remove only digest-proven retired executables.
	for _, hash := range r.Files {
		if hash == r.Active {
			continue
		}
		path := filepath.Join(m.root(), "versions", hash)
		actual, err := hashFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if actual != hash {
			return fmt.Errorf("%w: modified managed executable %s", model.ErrConflict, path)
		}
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	return platform.SyncDir(filepath.Join(m.root(), "versions"))
}

func (m Manager) Install(ctx context.Context, source, version string) (Result, error) {
	return m.install(ctx, source, version, "")
}

func (m Manager) install(ctx context.Context, source, version, expected string) (result Result, err error) {
	result = Result{Version: version, BinDir: m.BinDir}
	release, err := m.lock(ctx)
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, release()) }()
	r, err := m.read()
	if errors.Is(err, os.ErrNotExist) {
		// A missing receipt cannot grant ownership of pre-existing store files.
		entries, scanErr := os.ReadDir(m.root())
		if scanErr != nil {
			return result, scanErr
		}
		for _, entry := range entries {
			if entry.Name() != "lock" && entry.Name() != "versions" {
				return result, fmt.Errorf("%w: unrecognized CLI store entry %s", model.ErrConflict, entry.Name())
			}
		}
		entries, scanErr = os.ReadDir(filepath.Join(m.root(), "versions"))
		if scanErr != nil {
			return result, scanErr
		}
		if len(entries) != 0 {
			return result, fmt.Errorf("%w: executable store has no ownership receipt", model.ErrConflict)
		}
		r, err = receipt{}, nil
	}
	if err != nil {
		return result, err
	}
	if expected != "" && r.Active != expected {
		return result, model.ErrStalePlan
	}
	if err := m.checkLinks(r); err != nil {
		return result, err
	}
	data, err := readExecutable(source)
	if err != nil {
		return result, err
	}
	if len(data) == 0 || len(data) > 64<<20 {
		return result, errors.New("invalid executable size")
	}
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	dest := filepath.Join(m.root(), "versions", hash)
	found := false
	for _, old := range r.Files {
		if old == hash {
			found = true
		}
	}
	_, statErr := os.Lstat(dest)
	if statErr == nil {
		if !found {
			return result, fmt.Errorf("%w: unowned executable at %s", model.ErrConflict, dest)
		}
		actual, err := hashFile(dest)
		if err != nil || actual != hash {
			return result, errors.New("existing executable differs from its digest")
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return result, statErr
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if !found {
		r.Files = append(r.Files, hash)
		r.Owner = owner
		// Persist ownership before publishing the immutable executable. Active
		// remains unchanged until the executable and its directory are durable.
		if err := m.write(r); err != nil {
			return result, err
		}
	}
	if errors.Is(statErr, os.ErrNotExist) {
		if err := atomicFile(filepath.Dir(dest), hash, data, 0755); err != nil {
			return result, err
		}
	}
	result.Changed = r.Active != hash
	r.Owner, r.Active, r.Version = owner, hash, version
	if err := m.write(r); err != nil {
		return result, err
	}
	// Once the intent is durable, complete its bounded filesystem transition even
	// when cancellation arrives; a later invocation can replay a failed transition.
	return result, m.reconcile(r)
}

func (m Manager) Uninstall(ctx context.Context) (result Result, err error) {
	result = Result{BinDir: m.BinDir, Removed: true}
	release, err := m.lock(ctx)
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, release()) }()
	r, err := m.read()
	if err != nil {
		return result, err
	}
	if err := m.checkLinks(r); err != nil {
		return result, err
	}
	result.Changed = r.Active != ""
	r.Active = ""
	if err := m.write(r); err != nil {
		return result, err
	}
	return result, m.reconcile(r)
}

func readExecutable(path string) ([]byte, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 64<<20 {
		return nil, errors.New("invalid executable size or file type")
	}
	data, err := io.ReadAll(io.LimitReader(f, (64<<20)+1))
	if len(data) > 64<<20 {
		return nil, errors.New("executable exceeds size limit")
	}
	return data, err
}
