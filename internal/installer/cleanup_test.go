package installer

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shqingda/gobrew/internal/model"
	"github.com/shqingda/gobrew/internal/state"
)

func TestCleanupRejectsAliasedOwnershipMarker(t *testing.T) {
	for _, kind := range []string{"Cellar", "backups"} {
		t.Run(kind, func(t *testing.T) {
			prefix := t.TempDir()
			s := state.Store{Prefix: prefix}
			if err := s.Initialize(); err != nil {
				t.Fatal(err)
			}
			keg := filepath.Join(prefix, "Cellar", "demo", "1")
			if kind == "backups" {
				keg = filepath.Join(prefix, ".gobrew", "backups", "id", "demo")
			}
			if err := os.MkdirAll(keg, 0755); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(t.TempDir(), "marker")
			if err := os.WriteFile(marker, []byte(strings.Repeat("a", 32)+":demo"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(marker, filepath.Join(keg, ownerFile)); err != nil {
				t.Fatal(err)
			}
			if _, err := (Installer{Prefix: prefix}).Cleanup(context.Background()); err == nil {
				t.Fatal("accepted aliased ownership")
			}
			if _, err := os.Lstat(keg); err != nil {
				t.Fatal("removed foreign directory", err)
			}
		})
	}
}

func TestCleanupWithLimitsDryRunAndPinsLikeBrew(t *testing.T) {
	prefix := t.TempDir()
	s := state.Store{Prefix: prefix}
	if err := s.Initialize(); err != nil {
		t.Fatal(err)
	}
	keg := func(name, version string) string {
		t.Helper()
		path := filepath.Join(prefix, "Cellar", name, version)
		if err := os.MkdirAll(filepath.Join(path, "bin"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "bin", name), []byte("12345"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, ownerFile), []byte(strings.Repeat("a", 32)+":"+name), 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	records := map[string]model.Record{}
	for _, name := range []string{"uv", "jq", "node"} {
		current := keg(name, "2")
		records[name] = model.Record{Formula: model.Formula{Name: name, Version: "2"}, Keg: current, Owner: strings.Repeat("a", 32) + ":" + name, Status: model.Ready, Pinned: name == "node"}
	}
	oldUV, oldJQ, oldNode := keg("uv", "1"), keg("jq", "1"), keg("node", "1")
	if err := s.WithLock(context.Background(), func(tx *state.Session) error { return tx.Save(model.Snapshot{Records: records}) }); err != nil {
		t.Fatal(err)
	}
	in := Installer{Prefix: prefix}
	removed, err := in.CleanupWith(context.Background(), CleanupOptions{Formulae: []string{"uv"}, DryRun: true})
	if err != nil || len(removed) != 1 || removed[0].Path != oldUV || removed[0].Files != 1 {
		t.Fatalf("dry run = %+v, %v", removed, err)
	}
	if _, err := os.Stat(oldUV); err != nil {
		t.Fatal("dry run removed a keg")
	}
	if removed, err = in.CleanupWith(context.Background(), CleanupOptions{Formulae: []string{"uv"}}); err != nil || len(removed) != 1 {
		t.Fatalf("formula cleanup = %+v, %v", removed, err)
	}
	if _, err := os.Stat(oldJQ); err != nil {
		t.Fatal("formula cleanup touched another formula")
	}
	if removed, err = in.CleanupWith(context.Background(), CleanupOptions{Skip: []string{"jq"}}); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]bool{oldUV: false, oldJQ: true, oldNode: true, records["uv"].Keg: true} {
		if _, err := os.Stat(path); (err == nil) != want {
			t.Fatalf("%s exists = %v, want %v (removed %+v)", path, err == nil, want, removed)
		}
	}
	// Removing old kegs must not leave their empty trash directories behind.
	entries, err := os.ReadDir(filepath.Join(prefix, ".gobrew", "trash"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("trash left %v, %v", entries, err)
	}
}

// machoImporting is a minimal arm64 Mach-O executable whose only load command
// imports lib.
func machoImporting(lib string) []byte {
	name := append([]byte(lib), 0)
	for (24+len(name))%8 != 0 {
		name = append(name, 0)
	}
	size := uint32(24 + len(name))
	le := binary.LittleEndian
	b := le.AppendUint32(nil, 0xfeedfacf) // MH_MAGIC_64
	b = le.AppendUint32(b, 0x0100000c)    // CPU_TYPE_ARM64
	b = le.AppendUint32(b, 0)
	b = le.AppendUint32(b, 2) // MH_EXECUTE
	b = le.AppendUint32(b, 1)
	b = le.AppendUint32(b, size)
	b = le.AppendUint32(b, 0)
	b = le.AppendUint32(b, 0)
	b = le.AppendUint32(b, 0xc) // LC_LOAD_DYLIB
	b = le.AppendUint32(b, size)
	b = le.AppendUint32(b, 24)
	b = le.AppendUint32(b, 0)
	b = le.AppendUint32(b, 0)
	b = le.AppendUint32(b, 0)
	return append(b, name...)
}

func TestCleanupKeepsKegsThatInstalledBinariesImport(t *testing.T) {
	prefix := t.TempDir()
	s := state.Store{Prefix: prefix}
	if err := s.Initialize(); err != nil {
		t.Fatal(err)
	}
	keg := func(name, version string, bin []byte) string {
		t.Helper()
		path := filepath.Join(prefix, "Cellar", name, version)
		if err := os.MkdirAll(filepath.Join(path, "bin"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "bin", name), bin, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, ownerFile), []byte(strings.Repeat("a", 32)+":"+name), 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	imported := keg("lib", "1", []byte("old"))
	unused := keg("tool", "1", []byte("old"))
	records := map[string]model.Record{}
	for name, bin := range map[string][]byte{
		"lib":  []byte("new"),
		"tool": machoImporting(filepath.Join(imported, "lib", "liblib.dylib")),
	} {
		records[name] = model.Record{Formula: model.Formula{Name: name, Version: "2"}, Keg: keg(name, "2", bin), Owner: strings.Repeat("a", 32) + ":" + name, Status: model.Ready}
	}
	if err := s.WithLock(context.Background(), func(tx *state.Session) error { return tx.Save(model.Snapshot{Records: records}) }); err != nil {
		t.Fatal(err)
	}
	removed, err := (Installer{Prefix: prefix}).CleanupWith(context.Background(), CleanupOptions{})
	if err != nil || len(removed) != 1 || removed[0].Path != unused {
		t.Fatalf("cleanup = %+v, %v", removed, err)
	}
	if _, err := os.Stat(imported); err != nil {
		t.Fatal("removed a keg an installed binary imports:", err)
	}
}
