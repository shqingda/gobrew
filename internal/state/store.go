// Package state persists installed facts and recovery intents. Handles never
// escape an operation: a bbolt read handle would otherwise block another client.
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
)

var bucket = []byte("state-v1")

type Store struct{ Prefix string }
type Session struct{ db *bolt.DB }

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
}

// layout lists every directory Initialize creates, parents first.
var layout = []string{".gobrew", ".gobrew/staging", ".gobrew/backups", ".gobrew/trash", "Cellar", "opt", "bin", "sbin", "lib", "include", "share", "etc", "var"}

func (s Store) Dir() string  { return filepath.Join(s.Prefix, ".gobrew") }
func (s Store) path() string { return filepath.Join(s.Dir(), "state.db") }

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
	return SyncDir(s.Dir())
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
	db, err := bolt.Open(s.path(), 0600, &bolt.Options{ReadOnly: true, Timeout: 250 * time.Millisecond})
	if err != nil {
		return model.Snapshot{}, dbError(err)
	}
	defer db.Close()
	return (&Session{db: db}).Load()
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
			return fmt.Errorf("%w: %v", model.ErrBusy, err)
		}
		return fmt.Errorf("acquire prefix lock: %w", err)
	}
	defer unlock()
	db, err := bolt.Open(s.path(), 0600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		if cause := ctx.Err(); cause != nil {
			return cause
		}
		return dbError(err)
	}
	defer db.Close()
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn(&Session{db: db})
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
	if errors.Is(err, bolt.ErrTimeout) {
		return model.ErrBusy
	}
	return err
}

func (s *Session) Load() (model.Snapshot, error) {
	snap := empty()
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucket)
		if b == nil {
			return errors.New("unrecognized state schema")
		}
		snap.RecoveryRequired = b.Get([]byte("operation")) != nil
		v := b.Get([]byte("installed"))
		if v == nil {
			return nil
		}
		return json.Unmarshal(v, &snap)
	})
	if snap.Records == nil {
		snap.Records = make(map[string]model.Record)
	}
	return snap, err
}

// Save increments the generation even when restoring an earlier snapshot, so
// no previously prepared plan can become valid again after recovery.
func (s *Session) Save(snap model.Snapshot) error {
	current, err := s.Load()
	if err != nil {
		return err
	}
	snap.Generation = current.Generation + 1
	return s.put("installed", snap)
}

func (s *Session) Journal() (*Journal, error) {
	var j *Journal
	err := s.db.View(func(tx *bolt.Tx) error {
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
		return s.db.Update(func(tx *bolt.Tx) error {
			b := tx.Bucket(bucket)
			if b == nil {
				return errors.New("unrecognized state schema")
			}
			return b.Delete([]byte("operation"))
		})
	}
	return s.put("operation", j)
}

func (s *Session) put(key string, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(bucket)
		if bucket == nil {
			return errors.New("unrecognized state schema")
		}
		return bucket.Put([]byte(key), b)
	})
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
		if err := SyncDir(filepath.Dir(dir)); err != nil {
			return "", err
		}
	}
	return path, nil
}

func SyncDir(path string) error {
	return platform.SyncDir(path)
}
