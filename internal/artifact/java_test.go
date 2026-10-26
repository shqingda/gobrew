package artifact

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/shqingda/gobrew/internal/model"
)

func javaClassFixture() []byte {
	var data bytes.Buffer
	put := func(value uint16) { _ = binary.Write(&data, binary.BigEndian, value) }
	_ = binary.Write(&data, binary.BigEndian, uint32(0xcafebabe))
	put(0)  // minor
	put(52) // Java 8; interpreted as 52 architectures by a naive fat reader.
	put(5)  // constant_pool_count
	for i, name := range []string{"Demo", "java/lang/Object"} {
		data.WriteByte(1)
		put(uint16(len(name)))
		data.WriteString(name)
		data.WriteByte(7)
		put(uint16(1 + i*2))
	}
	for _, value := range []uint16{0x21, 2, 4, 0, 0, 0, 0} {
		put(value)
	}
	return data.Bytes()
}

func TestJavaClassIsPreservedWithoutMaskingMalformedFatMachO(t *testing.T) {
	class := javaClassFixture()
	root := t.TempDir()
	path := filepath.Join(root, "Demo.class")
	if err := os.WriteFile(path, class, 0644); err != nil {
		t.Fatal(err)
	}
	if err := relocateTree(context.Background(), root, "/opt/gobrew", model.Package{}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, class) {
		t.Fatalf("Java class changed: %v", err)
	}
	for name, malformed := range map[string][]byte{
		"truncated fat header":         {0xca, 0xfe, 0xba, 0xbe, 0, 0, 0, 2},
		"Java-like architecture count": {0xca, 0xfe, 0xba, 0xbe, 0, 0, 0, 52, 0, 0},
		"truncated class":              class[:len(class)-1],
	} {
		t.Run(name, func(t *testing.T) {
			if !isMachO(malformed) {
				t.Fatal("malformed fat bypassed Mach-O validation")
			}
			if _, _, err := patchMachO(malformed, nil); err == nil {
				t.Fatal("malformed fat accepted")
			}
		})
	}
	// A valid one-slice universal binary must continue down the Mach-O path.
	fat := make([]byte, 64)
	binary.BigEndian.PutUint32(fat, 0xcafebabe)
	binary.BigEndian.PutUint32(fat[4:], 1)
	binary.BigEndian.PutUint32(fat[8:], 0x100000c)
	binary.BigEndian.PutUint32(fat[16:], 32)
	binary.BigEndian.PutUint32(fat[20:], 32)
	binary.BigEndian.PutUint32(fat[24:], 2)
	binary.LittleEndian.PutUint32(fat[32:], 0xfeedfacf)
	if !isMachO(fat) {
		t.Fatal("valid universal Mach-O misclassified")
	}
	if _, _, err := patchMachO(fat, nil); err != nil {
		t.Fatal(err)
	}
}
