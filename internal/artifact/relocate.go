package artifact

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/shqingda/gobrew/internal/model"
	"golang.org/x/sys/unix"
)

type replacement struct{ old, new string }

func replacements(prefix string, p model.Package) []replacement {
	return []replacement{
		{"@@HOMEBREW_CELLAR@@", filepath.Join(prefix, "Cellar")},
		{"@@HOMEBREW_PREFIX@@", prefix},
		{"@@HOMEBREW_REPOSITORY@@", prefix},
		{"@@HOMEBREW_LIBRARY@@", filepath.Join(prefix, "Library")},
		{"/opt/homebrew", prefix},
	}
}

func relocateTree(ctx context.Context, tree, prefix string, p model.Package) error {
	_, err := relocateTreeChecked(ctx, tree, prefix, p)
	return err
}

func relocateTreeChecked(ctx context.Context, tree, prefix string, p model.Package) ([]string, error) {
	repls := replacements(prefix, p)
	var signPaths []string
	linked := map[string]bool{}
	err := filepath.WalkDir(tree, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			linkReplacements := append(cellarMappings([]byte(link), prefix, p), repls...)
			next := replaceText(link, linkReplacements)
			if strings.Contains(next, "@@HOMEBREW_") {
				return fmt.Errorf("%w: unknown bottle placeholder in %s", model.ErrUnsupported, path)
			}
			if next != link {
				if err := os.Remove(path); err != nil {
					return err
				}
				return os.Symlink(next, path)
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Size() == 0 {
			return nil
		}
		if err := os.Chmod(path, info.Mode().Perm()|0600); err != nil {
			return err
		}
		defer os.Chmod(path, info.Mode().Perm())
		file, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			return err
		}
		defer file.Close()
		data, err := unix.Mmap(int(file.Fd()), 0, int(info.Size()), unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
		if err != nil {
			return err
		}
		defer func() {
			if data != nil {
				_ = unix.Munmap(data)
			}
		}()
		fileReplacements := append(cellarMappings(data, prefix, p), repls...)
		macho := isMachO(data)
		binaryFile := macho || bytes.IndexByte(data[:min(8192, len(data))], 0) >= 0 || !utf8.Valid(data[:min(8192, len(data))]) || bytes.HasPrefix(data, []byte("!<arch>\n"))
		if !binaryFile {
			text := string(data)
			next := replaceText(text, fileReplacements)
			if strings.Contains(next, "@@HOMEBREW_") {
				return fmt.Errorf("%w: unknown bottle placeholder in %s", model.ErrUnsupported, path)
			}
			if next == text {
				return nil
			}
			// The old mapping must not be touched after truncation; unmap before
			// replacing text whose byte length may change.
			if err := unix.Munmap(data); err != nil {
				return err
			}
			data = nil
			if err := file.Truncate(0); err != nil {
				return err
			}
			_, err = file.WriteAt([]byte(next), 0)
			return err
		}
		changed := false
		var fallback []string
		if macho {
			changed, fallback, err = patchMachO(data, fileReplacements)
			if err != nil {
				return fmt.Errorf("relocate %s: %w", path, err)
			}
		}
		// Static archives and compiled defaults also contain literal prefixes.
		// Only fixed-width substitutions are safe outside Mach-O load commands.
		for _, r := range fileReplacements {
			if len(r.new) > len(r.old) {
				// Unfit load-command strings are handled by install_name_tool.
				if bytes.Contains(data, []byte(r.old)) && (!macho || len(fallback) == 0) {
					return fmt.Errorf("%w: prefix is too long for %s", model.ErrUnsupported, path)
				}
				continue
			}
			for from := 0; from < len(data); {
				i := bytes.Index(data[from:], []byte(r.old))
				if i < 0 {
					break
				}
				i += from
				end := i + len(r.old)
				if end == len(data) || data[end] == '/' || data[end] == 0 || data[end] == ':' {
					copy(data[i:end], r.new+strings.Repeat("/", len(r.old)-len(r.new)))
					changed = true
				}
				from = end
			}
		}
		if changed {
			if err := unix.Msync(data, unix.MS_SYNC); err != nil {
				return fmt.Errorf("flush relocated mapping %s: %w", path, err)
			}
		}
		finalData := data
		if len(fallback) > 0 {
			if err := unix.Munmap(data); err != nil {
				return err
			}
			data = nil
			if runtime.GOOS != "darwin" {
				return fmt.Errorf("%w: Mach-O relocation requires macOS", model.ErrUnsupported)
			}
			args := append(fallback, path)
			if out, err := exec.CommandContext(ctx, "/usr/bin/install_name_tool", args...).CombinedOutput(); err != nil {
				return fmt.Errorf("install_name_tool: %w: %s", err, bytes.TrimSpace(out))
			}
			changed = true
			// A longer prefix can be expanded inside load commands, but not in
			// compiled string constants. Refuse remaining unrelocatable paths.
			updated, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			finalData = updated
			for _, r := range fileReplacements {
				if len(r.new) > len(r.old) && bytes.Contains(updated, []byte(r.old)) {
					return fmt.Errorf("%w: compiled prefix cannot grow in %s", model.ErrUnsupported, path)
				}
			}
		}
		if bytes.Contains(finalData, []byte("@@HOMEBREW_")) {
			return fmt.Errorf("%w: unresolved bottle placeholder in %s", model.ErrUnsupported, path)
		}
		if macho {
			dependencies, err := machoDependencies(finalData, tree, path, prefix, p.Formula)
			if err != nil {
				return fmt.Errorf("inspect linkage %s: %w", path, err)
			}
			for _, dependency := range dependencies {
				linked[dependency] = true
			}
		}
		if macho && (changed || !hasCodeSignature(data)) {
			signPaths = append(signPaths, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(signPaths) > 0 {
		if runtime.GOOS != "darwin" {
			return nil, fmt.Errorf("%w: code signing requires macOS", model.ErrUnsupported)
		}
		// Bound argv size while preserving entitlements and runtime flags.
		for start := 0; start < len(signPaths); start += 64 {
			args := []string{"--force", "--sign", "-", "--preserve-metadata=entitlements,requirements,flags,runtime"}
			args = append(args, signPaths[start:min(start+64, len(signPaths))]...)
			if out, err := exec.CommandContext(ctx, "/usr/bin/codesign", args...).CombinedOutput(); err != nil {
				return nil, fmt.Errorf("codesign: %w: %s", err, bytes.TrimSpace(out))
			}
		}
	}
	dependencies := make([]string, 0, len(linked))
	for name := range linked {
		dependencies = append(dependencies, name)
	}
	sort.Strings(dependencies)
	return dependencies, nil
}

// Bottles can name the Cellar version of a dependency used by the build farm.
// Dependency compatibility is checked separately; use its stable opt path so a
// compatible revision installed locally need not reproduce the build machine's
// historical keg directory. The package's own paths retain its exact keg.
func cellarMappings(data []byte, prefix string, p model.Package) []replacement {
	found := map[string]string{}
	validByte := func(b byte) bool {
		return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || strings.ContainsRune("@._+-", rune(b))
	}
	valid := func(part []byte) bool {
		if len(part) == 0 {
			return false
		}
		for _, b := range part {
			if !validByte(b) {
				return false
			}
		}
		return true
	}
	for _, cellar := range []string{"@@HOMEBREW_CELLAR@@/", "/opt/homebrew/Cellar/"} {
		for start := 0; start < len(data); {
			i := bytes.Index(data[start:], []byte(cellar))
			if i < 0 {
				break
			}
			i += start
			nameStart := i + len(cellar)
			nameEnd := bytes.IndexByte(data[nameStart:], '/')
			if nameEnd < 0 {
				break
			}
			nameEnd += nameStart
			versionEnd := nameEnd + 1
			for versionEnd < len(data) && validByte(data[versionEnd]) {
				versionEnd++
			}
			name, version := data[nameStart:nameEnd], data[nameEnd+1:versionEnd]
			if valid(name) && valid(version) {
				target := filepath.Join(prefix, "opt", string(name))
				if string(name) == p.Formula.Name {
					target = filepath.Join(prefix, "Cellar", p.Formula.Name, p.Formula.PkgVersion())
				}
				found[string(data[i:versionEnd])] = target
			}
			start = nameEnd + 1
		}
	}
	out := make([]replacement, 0, len(found))
	for old, new := range found {
		out = append(out, replacement{old, new})
	}
	sort.Slice(out, func(i, j int) bool { return len(out[i].old) > len(out[j].old) })
	return out
}

func replaceText(text string, repls []replacement) string {
	for _, r := range repls {
		text = strings.ReplaceAll(text, r.old, r.new)
	}
	return text
}

func isMachO(data []byte) bool {
	if len(data) < 4 {
		return false
	}
	switch binary.BigEndian.Uint32(data[:4]) {
	case 0xcafebabe:
		return !isJavaClass(data)
	case 0xfeedface, 0xcefaedfe, 0xfeedfacf, 0xcffaedfe, 0xbebafeca, 0xcafebabf, 0xbfbafeca:
		return true
	}
	return false
}

type machoSlice struct{ start, end int }

func machoSlices(data []byte) ([]machoSlice, error) {
	if len(data) < 4 {
		return nil, errors.New("truncated Mach-O")
	}
	magic := binary.BigEndian.Uint32(data[:4])
	var order binary.ByteOrder = binary.BigEndian
	if magic == 0xbebafeca || magic == 0xbfbafeca {
		order = binary.LittleEndian
	}
	if magic != 0xcafebabe && magic != 0xbebafeca && magic != 0xcafebabf && magic != 0xbfbafeca {
		return []machoSlice{{0, len(data)}}, nil
	}
	if len(data) < 8 {
		return nil, errors.New("truncated fat header")
	}
	count := order.Uint32(data[4:8])
	width := 20
	if magic == 0xcafebabf || magic == 0xbfbafeca {
		width = 32
	}
	if count == 0 || count > 64 || uint64(count)*uint64(width)+8 > uint64(len(data)) {
		return nil, errors.New("invalid fat header")
	}
	var slices []machoSlice
	for i := 0; i < int(count); i++ {
		at := 8 + i*width
		offset, size := uint64(order.Uint32(data[at+8:at+12])), uint64(order.Uint32(data[at+12:at+16]))
		if width == 32 {
			offset = order.Uint64(data[at+8 : at+16])
			size = order.Uint64(data[at+16 : at+24])
		}
		if offset > uint64(len(data)) || size > uint64(len(data))-offset {
			return nil, errors.New("invalid fat slice")
		}
		slices = append(slices, machoSlice{int(offset), int(offset + size)})
	}
	return slices, nil
}

func walkCommands(data []byte, visit func([]byte, binary.ByteOrder, uint32) error) error {
	slices, err := machoSlices(data)
	if err != nil {
		return err
	}
	for _, slice := range slices {
		part := data[slice.start:slice.end]
		if len(part) < 28 {
			return errors.New("truncated Mach-O header")
		}
		magic := binary.BigEndian.Uint32(part[:4])
		var order binary.ByteOrder = binary.BigEndian
		if magic == 0xcefaedfe || magic == 0xcffaedfe {
			order = binary.LittleEndian
		} else if magic != 0xfeedface && magic != 0xfeedfacf {
			return errors.New("invalid Mach-O magic")
		}
		header := 28
		if magic == 0xfeedfacf || magic == 0xcffaedfe {
			header = 32
		}
		count, size := order.Uint32(part[16:20]), order.Uint32(part[20:24])
		if uint64(header)+uint64(size) > uint64(len(part)) || count > size/8 {
			return errors.New("invalid load-command table")
		}
		at := header
		end := header + int(size)
		for i := uint32(0); i < count; i++ {
			if at+8 > end {
				return errors.New("truncated load command")
			}
			kind, n := order.Uint32(part[at:at+4]), order.Uint32(part[at+4:at+8])
			if n < 8 || uint64(at)+uint64(n) > uint64(end) {
				return errors.New("invalid load command length")
			}
			if err := visit(part[at:at+int(n)], order, kind); err != nil {
				return err
			}
			at += int(n)
		}
	}
	return nil
}

func patchMachO(data []byte, repls []replacement) (bool, []string, error) {
	changed := false
	var fallback []string
	err := walkCommands(data, func(command []byte, order binary.ByteOrder, kind uint32) error {
		flag := ""
		switch kind {
		case 0xd:
			flag = "-id"
		case 0xc, 0x80000018, 0x8000001f, 0x80000023, 0x20:
			flag = "-change"
		case 0x8000001c:
			flag = "-rpath"
		default:
			return nil
		}
		if len(command) < 12 {
			return errors.New("truncated path load command")
		}
		offset := int(order.Uint32(command[8:12]))
		if offset < 12 || offset >= len(command) {
			return errors.New("invalid load-command string offset")
		}
		field := command[offset:]
		nul := bytes.IndexByte(field, 0)
		if nul < 0 {
			return errors.New("unterminated Mach-O path")
		}
		old := string(field[:nul])
		next := replaceText(old, repls)
		if old == next {
			return nil
		}
		if len(next)+1 > len(field) {
			if flag == "-id" {
				fallback = append(fallback, flag, next)
			} else {
				fallback = append(fallback, flag, old, next)
			}
			return nil
		}
		clear(field)
		copy(field, next)
		changed = true
		return nil
	})
	return changed, fallback, err
}

func hasCodeSignature(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	slices, err := machoSlices(data)
	if err != nil {
		return false
	}
	found := 0
	_ = walkCommands(data, func(_ []byte, _ binary.ByteOrder, kind uint32) error {
		if kind == 0x1d {
			found++
		}
		return nil
	})
	return found == len(slices)
}
