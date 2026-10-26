package artifact

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/shqingda/gobrew/internal/model"
)

type linkageCommand struct {
	kind uint32
	path string
}

func linkageFixture(commands ...linkageCommand) []byte {
	data := make([]byte, 32)
	binary.LittleEndian.PutUint32(data, 0xfeedfacf)
	binary.LittleEndian.PutUint32(data[12:], 2)
	binary.LittleEndian.PutUint32(data[16:], uint32(len(commands)))
	for _, command := range commands {
		offset := 24
		if command.kind == 0x8000001c {
			offset = 12
		}
		size := (offset + len(command.path) + 1 + 7) &^ 7
		value := make([]byte, size)
		binary.LittleEndian.PutUint32(value, command.kind)
		binary.LittleEndian.PutUint32(value[4:], uint32(size))
		binary.LittleEndian.PutUint32(value[8:], uint32(offset))
		copy(value[offset:], command.path)
		data = append(data, value...)
	}
	binary.LittleEndian.PutUint32(data[20:], uint32(len(data)-32))
	return data
}

func TestLinkageUsesImportsAndResolvesRunPaths(t *testing.T) {
	tree := t.TempDir()
	if err := os.Mkdir(filepath.Join(tree, "lib"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tree, "lib", "libself.dylib"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	data := linkageFixture(
		linkageCommand{0xd, "/opt/gobrew/opt/not-a-dependency/lib/self.dylib"},
		linkageCommand{0xc, "/usr/lib/libSystem.B.dylib"},
		linkageCommand{0x8000001f, "/opt/gobrew/Cellar/reexported/1/lib/libdep.dylib"},
		linkageCommand{0xc, "@loader_path/../../../../opt/loader-dep/lib/libdep.dylib"},
		linkageCommand{0xc, "@rpath/libself.dylib"},
		linkageCommand{0xc, "@rpath/libexternal.dylib"},
		linkageCommand{0x8000001c, "@loader_path/../lib"},
		linkageCommand{0x8000001c, "/opt/gobrew/opt/rpath-dep/lib"},
	)
	got, err := machoDependencies(data, tree, filepath.Join(tree, "bin", "demo"), "/opt/gobrew", model.Formula{Name: "demo", Version: "1"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"loader-dep", "reexported", "rpath-dep"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dependencies=%v, want=%v", got, want)
	}
	for _, path := range []string{"/outside/lib/libbad.dylib", "@rpath/libmissing.dylib", "relative.dylib"} {
		data := linkageFixture(linkageCommand{0xc, path})
		if _, err := machoDependencies(data, tree, filepath.Join(tree, "bin", "demo"), "/opt/gobrew", model.Formula{Name: "demo", Version: "1"}); err == nil {
			t.Fatalf("unresolved external import accepted: %s", path)
		}
	}
	if err := os.Symlink("/outside/lib/libbad.dylib", filepath.Join(tree, "lib", "libredirected.dylib")); err != nil {
		t.Fatal(err)
	}
	redirected := linkageFixture(linkageCommand{0xc, "@rpath/libredirected.dylib"}, linkageCommand{0x8000001c, "@loader_path/../lib"})
	if _, err := machoDependencies(redirected, tree, filepath.Join(tree, "bin", "demo"), "/opt/gobrew", model.Formula{Name: "demo", Version: "1"}); err == nil {
		t.Fatal("external symlink was incorrectly classified as an in-keg library")
	}
}
