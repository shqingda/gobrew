package state

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shqingda/gobrew/internal/model"
	"github.com/shqingda/gobrew/internal/platform"
	bolt "go.etcd.io/bbolt"
)

func TestPersistAndGeneration(t *testing.T) {
	s := Store{Prefix: t.TempDir()}
	if err := s.Initialize(); err != nil {
		t.Fatal(err)
	}
	empty, err := s.Load()
	if err != nil || empty.Generation != 0 {
		t.Fatalf("empty: %v %v", empty, err)
	}
	err = s.WithLock(context.Background(), func(tx *Session) error {
		snap, err := tx.Load()
		if err != nil {
			return err
		}
		snap.Records["demo"] = model.Record{Formula: model.Formula{Name: "demo", Version: "1.0"}, Status: model.Ready}
		return tx.Save(snap)
	})
	if err != nil {
		t.Fatal(err)
	}
	snap, err := s.Load()
	if err != nil || snap.Generation != 1 || snap.Records["demo"].Status != model.Ready {
		t.Fatalf("persisted: %+v %v", snap, err)
	}
}

func TestWaitingWriterPreservesCallerDeadline(t *testing.T) {
	s := Store{Prefix: t.TempDir()}
	if err := s.Initialize(); err != nil {
		t.Fatal(err)
	}
	release, err := platform.AcquireLock(context.Background(), filepath.Join(s.Dir(), "writer.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err = s.WithLock(ctx, func(*Session) error { t.Fatal("entered locked operation"); return nil })
	if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, model.ErrBusy) {
		t.Fatalf("caller deadline became an unrelated busy error: %v", err)
	}
}

func TestUninitializedReadDoesNotCreateFiles(t *testing.T) {
	s := Store{Prefix: filepath.Join(t.TempDir(), "missing")}
	snap, err := s.Load()
	if err != nil || len(snap.Records) != 0 {
		t.Fatalf("%+v %v", snap, err)
	}
	err = s.WithLock(context.Background(), func(*Session) error { return nil })
	if !errors.Is(err, model.ErrNotFound) {
		t.Fatalf("want initialization error, got %v", err)
	}
}

func TestInitializeRejectsSymlinkedPrivateDirectory(t *testing.T) {
	prefix, other := t.TempDir(), t.TempDir()
	if err := os.Symlink(other, filepath.Join(prefix, ".gobrew")); err != nil {
		t.Fatal(err)
	}
	if err := (Store{Prefix: prefix}).Initialize(); err == nil {
		t.Fatal("followed private directory symlink")
	}
}

func TestInitializeRejectsForeignDatabase(t *testing.T) {
	for _, populated := range []bool{false, true} {
		t.Run(fmt.Sprint(populated), func(t *testing.T) {
			s := Store{Prefix: t.TempDir()}
			if err := os.Mkdir(s.Dir(), 0755); err != nil {
				t.Fatal(err)
			}
			db, err := bolt.Open(s.path(), 0600, nil)
			if err != nil {
				t.Fatal(err)
			}
			if populated {
				err = db.Update(func(tx *bolt.Tx) error {
					b, err := tx.CreateBucket([]byte("another-app"))
					if err != nil {
						return err
					}
					return b.Put([]byte("key"), []byte("keep me"))
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(s.path())
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Initialize(); err == nil {
				t.Fatal("adopted an unrelated database")
			}
			after, err := os.ReadFile(s.path())
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("changed foreign database")
			}
		})
	}
}
