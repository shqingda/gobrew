// Package state persists installed facts and recovery intents. A bbolt handle
// holds a process-level file lock, so handles live for one transaction only:
// a writer holds writer.lock for its whole operation, but readers wait at most
// for one short transaction, even during a long external post-install step.
package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/shqingda/gobrew/internal/model"
	"github.com/shqingda/gobrew/internal/platform"
	bolt "go.etcd.io/bbolt"
	bolterrors "go.etcd.io/bbolt/errors"
)

var bucket = []byte("state-v1")

type Store struct{ Prefix string }
// Session is a writer's access while it holds writer.lock. Each call opens the
// database for one transaction and closes it again.
type Session struct{ store Store }

type LinkChange struct {
	Path   string  `json:"path"`
	Before *string `json:"before,omitempty"`
	After  *string `json:"after,omitempty"`
}

type Move struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Owner string `json:"owner"`
}

// Journal is durable before its first filesystem effect. Postinstall marks the
// irreversible boundary; recovery never replays an external command.
type Journal struct {
	ID           string         `json:"id"`
	Phase        string         `json:"phase"`
	Before       model.Snapshot `json:"before"`
	After        model.Snapshot `json:"after"`
	Links        []LinkChange   `json:"links"`
	Moves        []Move         `json:"moves"`
	Packages     []string       `json:"packages"`
	PendingLinks []LinkChange   `json:"pending_links,omitempty"`
	LinkPackage  string         `json:"link_package,omitempty"`
	// SkipPostInstall lists packages whose post-install steps this Apply leaves
	// out; an explicit gobrew postinstall runs them later.
	SkipPostInstall []string `json:"skip_post_install,omitempty"`
}

// layout lists every directory Initialize creates, parents first.
var layout = []string{".gobrew", ".gobrew/staging", ".gobrew/backups", ".gobrew/trash", "Cellar", "opt", "bin", "sbin", "lib", "include", "share", "etc", "var"}

func (s Store) Dir() string  { return filepath.Join(s.Prefix, ".gobrew") }
func (s Store) path() string { return filepath.Join(s.Dir(), "state.db") }

// Exists reports whether the state database is present, with a single stat.
func (s Store) Exists() (bool, error) {
	_, err := os.Lstat(s.path())
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

func (s Store) Initialize() error {
	if _, err := EnsureDir(s.Prefix, ".gobrew", 0755); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	unlock, err := platform.AcquireLock(ctx, filepath.Join(s.Dir(), "writer.lock"))
	if err != nil {
		return err
	}
	defer unlock()
	existing := false
	if _, err := os.Lstat(s.path()); err == nil {
		existing = true
		// Initialization must never claim an unrelated bbolt database, including
		// one with no buckets. Only a file created here may receive our schema.
		if _, err := s.Load(); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, rel := range layout {
		if _, err := EnsureDir(s.Prefix, rel, 0755); err != nil {
			return err
		}
	}
	if existing {
		return nil
	}
	db, err := bolt.Open(s.path(), 0600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.Update(func(tx *bolt.Tx) error { _, err := tx.CreateBucket(bucket); return err }); err != nil {
		return err
	}
	// bbolt syncs database contents; persist the initial filename as well.
	return platform.SyncDir(s.Dir())
}

// Pristine reports whether the prefix holds nothing but an interrupted or
// concurrent Initialize could have left: empty layout directories, the writer
// lock and the state database (which Initialize validates before reuse). Any
// other entry means the directory belongs to something else.
func (s Store) Pristine() error {
	allowed := map[string]bool{".gobrew/writer.lock": true, ".gobrew/state.db": true}
	for _, rel := range layout {
		allowed[rel] = true
	}
	var walk func(rel string) error
	walk = func(rel string) error {
		entries, err := os.ReadDir(filepath.Join(s.Prefix, rel))
		if err != nil {
			return err
		}
		for _, entry := range entries {
			child := filepath.ToSlash(filepath.Join(rel, entry.Name()))
			if !allowed[child] {
				return fmt.Errorf("refusing to adopt %s: unexpected %s; choose an empty prefix", s.Prefix, child)
			}
			if entry.Type().IsDir() {
				if err := walk(child); err != nil {
					return err
				}
			} else if !entry.Type().IsRegular() || slices.Contains(layout, child) {
				return fmt.Errorf("refusing to adopt %s: %s is not the expected file type", s.Prefix, child)
			}
		}
		return nil
	}
	return walk(".")
}

func (s Store) Load() (model.Snapshot, error) {
	if _, err := os.Lstat(s.path()); errors.Is(err, os.ErrNotExist) {
		return empty(), nil
	} else if err != nil {
		return model.Snapshot{}, err
	}
	if _, err := SecurePath(s.Prefix, ".gobrew/state.db"); err != nil {
		return model.Snapshot{}, err
	}
	if err := s.checkFile(); err != nil {
		return model.Snapshot{}, err
	}
	var snap model.Snapshot
	err := s.transaction(false, 250*time.Millisecond, func(tx *bolt.Tx) (err error) {
		snap, err = load(tx)
		return err
	})
	return snap, err
}

func (s Store) WithLock(ctx context.Context, fn func(*Session) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := os.Lstat(s.path()); errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: nothing is installed in %s", model.ErrNotFound, s.Prefix)
	} else if err != nil {
		return err
	}
	if _, err := SecurePath(s.Prefix, ".gobrew/state.db"); err != nil {
		return err
	}
	if err := s.checkFile(); err != nil {
		return err
	}
	lockCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	unlock, err := platform.AcquireLock(lockCtx, filepath.Join(s.Dir(), "writer.lock"))
	if err != nil {
		if cause := ctx.Err(); cause != nil {
			return cause
		}
		if errors.Is(lockCtx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("%w: another gobrew process has been changing %s for over 10 seconds; wait for it to finish and try again", model.ErrBusy, s.Prefix)
		}
		return fmt.Errorf("acquire prefix lock: %w", err)
	}
	defer unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn(&Session{store: s})
}

// transaction opens the database for one transaction. Writers wait up to a
// second for readers, whose transactions are short; readers wait for writers'
// single transactions, never for a whole operation.
func (s Store) transaction(write bool, timeout time.Duration, fn func(*bolt.Tx) error) error {
	if err := s.checkFile(); err != nil {
		return err
	}
	db, err := bolt.Open(s.path(), 0600, &bolt.Options{ReadOnly: !write, Timeout: timeout})
	if err != nil {
		return dbError(err)
	}
	if write {
		err = db.Update(fn)
	} else {
		err = db.View(fn)
	}
	return errors.Join(err, db.Close())
}

func (s *Session) view(fn func(*bolt.Tx) error) error {
	return s.store.transaction(false, time.Second, fn)
}

func (s *Session) update(fn func(*bolt.Tx) error) error {
	return s.store.transaction(true, time.Second, fn)
}

func (s Store) checkFile() error {
	info, err := os.Lstat(s.path())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("state database is not a regular file: %s", s.path())
	}
	return nil
}

func empty() model.Snapshot { return model.Snapshot{Records: make(map[string]model.Record)} }
func dbError(err error) error {
	if errors.Is(err, bolterrors.ErrTimeout) {
		return model.ErrBusy
	}
	return err
}

func (s *Session) Load() (model.Snapshot, error) {
	var snap model.Snapshot
	err := s.view(func(tx *bolt.Tx) (err error) {
		snap, err = load(tx)
		return err
	})
	return snap, err
}

func load(tx *bolt.Tx) (model.Snapshot, error) {
	snap := empty()
	b := tx.Bucket(bucket)
	if b == nil {
		return snap, errors.New("unrecognized state schema")
	}
	snap.RecoveryRequired = b.Get([]byte("operation")) != nil
	var err error
	if v := b.Get([]byte("installed")); v != nil {
		err = json.Unmarshal(v, &snap)
	}
	if snap.Records == nil {
		snap.Records = make(map[string]model.Record)
	}
	return snap, err
}

// Save increments the generation even when restoring an earlier snapshot, so
// no previously prepared plan can become valid again after recovery. Reading
// the current generation and writing the new one is a single transaction.
func (s *Session) Save(snap model.Snapshot) error {
	return s.update(func(tx *bolt.Tx) error {
		current, err := load(tx)
		if err != nil {
			return err
		}
		snap.Generation = current.Generation + 1
		return put(tx, "installed", snap)
	})
}

func (s *Session) Journal() (*Journal, error) {
	var j *Journal
	err := s.view(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucket)
		if b == nil {
			return errors.New("unrecognized state schema")
		}
		v := b.Get([]byte("operation"))
		if v == nil {
			return nil
		}
		return json.Unmarshal(v, &j)
	})
	return j, err
}

func (s *Session) WriteJournal(j *Journal) error {
	if j == nil {
		return s.update(func(tx *bolt.Tx) error {
			b := tx.Bucket(bucket)
			if b == nil {
				return errors.New("unrecognized state schema")
			}
			return b.Delete([]byte("operation"))
		})
	}
	return s.update(func(tx *bolt.Tx) error { return put(tx, "operation", j) })
}

func put(tx *bolt.Tx, key string, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	state := tx.Bucket(bucket)
	if state == nil {
		return errors.New("unrecognized state schema")
	}
	return state.Put([]byte(key), b)
}

// SecurePath rejects symlink parents. Shared prefix directories are real
// directories; only owned leaf links may point into a keg. The prefix itself
// must also be a real directory (macOS's system /tmp alias is allowed above it).
func SecurePath(prefix, rel string) (string, error) {
	if !filepath.IsAbs(prefix) || !filepath.IsLocal(rel) {
		return "", fmt.Errorf("unsafe path %q", rel)
	}
	if info, err := os.Lstat(prefix); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("unsafe prefix %s", prefix)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	target := filepath.Join(prefix, rel)
	for p := filepath.Dir(target); p != prefix; p = filepath.Dir(p) {
		if p == "." || p == string(filepath.Separator) {
			return "", errors.New("path escapes prefix")
		}
		info, err := os.Lstat(p)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("unsafe parent %s", p)
		}
	}
	return target, nil
}

// SecureDir also checks the leaf, which SecurePath deliberately leaves to
// callers that operate on owned symlinks. A missing directory is permitted.
func SecureDir(prefix, rel string) (string, error) {
	path, err := SecurePath(prefix, rel)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return path, nil
	}
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("unsafe directory %s", path)
	}
	return path, nil
}

func EnsureDir(prefix, rel string, mode os.FileMode) (string, error) {
	path, err := SecureDir(prefix, rel)
	if err != nil {
		return "", err
	}
	var created []string
	for dir := path; dir != prefix; dir = filepath.Dir(dir) {
		if _, err := os.Lstat(dir); errors.Is(err, os.ErrNotExist) {
			created = append(created, dir)
		} else if err != nil {
			return "", err
		} else {
			break
		}
	}
	if err := os.MkdirAll(path, mode); err != nil {
		return "", err
	}
	if _, err := SecureDir(prefix, rel); err != nil {
		return "", err
	}
	// Persist each new directory's entry before a journal can reference it.
	// Existing directories incur neither a walk nor an extra device flush.
	for _, dir := range created {
		if err := platform.SyncDir(filepath.Dir(dir)); err != nil {
			return "", err
		}
	}
	return path, nil
}
