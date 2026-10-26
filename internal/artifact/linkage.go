package artifact

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/shqingda/gobrew/internal/model"
)

// machoDependencies uses only loader commands, never arbitrary strings or a
// receipt's historical transitive dependencies. It runs on already-relocated
// mapped bytes before publication, avoiding a second read of each binary.
func machoDependencies(data []byte, tree, file, prefix string, formula model.Formula) ([]string, error) {
	var imports, rpaths []string
	err := walkCommands(data, func(command []byte, order binary.ByteOrder, kind uint32) error {
		switch kind {
		case 0xc, 0x80000018, 0x8000001f, 0x80000023, 0x20, 0x8000001c:
		default:
			return nil // LC_ID_DYLIB names this library; it is not an import.
		}
		if len(command) < 12 {
			return fmt.Errorf("truncated linkage command")
		}
		offset := int(order.Uint32(command[8:12]))
		if offset < 12 || offset >= len(command) {
			return fmt.Errorf("invalid linkage string offset")
		}
		end := bytes.IndexByte(command[offset:], 0)
		if end < 0 {
			return fmt.Errorf("unterminated linkage string")
		}
		value := string(command[offset : offset+end])
		if kind == 0x8000001c {
			rpaths = append(rpaths, value)
		} else {
			imports = append(imports, value)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(tree, file)
	if err != nil || !filepath.IsLocal(rel) {
		return nil, fmt.Errorf("Mach-O file is outside its keg")
	}
	keg := filepath.Join(prefix, "Cellar", formula.Name, formula.PkgVersion())
	loader := filepath.Dir(filepath.Join(keg, rel))
	isExecutable := false
	if slices, err := machoSlices(data); err == nil && len(slices) > 0 {
		part := data[slices[0].start:slices[0].end]
		var order binary.ByteOrder = binary.BigEndian
		if magic := binary.BigEndian.Uint32(part); magic == 0xcefaedfe || magic == 0xcffaedfe {
			order = binary.LittleEndian
		}
		isExecutable = order.Uint32(part[12:16]) == 2 // MH_EXECUTE
	}
	expand := func(path string) (string, error) {
		switch {
		case filepath.IsAbs(path):
			return filepath.Clean(path), nil
		case path == "@loader_path" || strings.HasPrefix(path, "@loader_path/"):
			return filepath.Join(loader, strings.TrimPrefix(strings.TrimPrefix(path, "@loader_path"), "/")), nil
		case isExecutable && (path == "@executable_path" || strings.HasPrefix(path, "@executable_path/")):
			return filepath.Join(loader, strings.TrimPrefix(strings.TrimPrefix(path, "@executable_path"), "/")), nil
		default:
			return "", fmt.Errorf("%w: cannot resolve loader path %q", model.ErrUnsupported, path)
		}
	}
	owner := func(path string) (name, ownRelative string, err error) {
		if strings.HasPrefix(path, "/usr/lib/") || strings.HasPrefix(path, "/System/Library/") {
			return "", "", nil
		}
		for _, dir := range []string{"opt", "Cellar"} {
			base := filepath.Join(prefix, dir) + string(filepath.Separator)
			if !strings.HasPrefix(path, base) {
				continue
			}
			parts := strings.Split(strings.TrimPrefix(path, base), string(filepath.Separator))
			minimum := 2
			if dir == "Cellar" {
				minimum = 3
			}
			if len(parts) < minimum || parts[0] == "" {
				break
			}
			if parts[0] == formula.Name {
				return parts[0], filepath.Join(parts[minimum-1:]...), nil
			}
			return parts[0], "", nil
		}
		return "", "", fmt.Errorf("%w: unowned external library %q", model.ErrUnsupported, path)
	}
	dependencies := map[string]bool{}
	for _, imported := range imports {
		var candidates []string
		rpath := strings.HasPrefix(imported, "@rpath/")
		if rpath {
			for _, base := range rpaths {
				resolved, err := expand(base)
				if err != nil {
					return nil, err
				}
				candidates = append(candidates, filepath.Join(resolved, strings.TrimPrefix(imported, "@rpath/")))
			}
		} else {
			resolved, err := expand(imported)
			if err != nil {
				return nil, err
			}
			candidates = []string{resolved}
		}
		resolved := false
		for _, candidate := range candidates {
			name, ownRelative, err := owner(candidate)
			if err != nil {
				return nil, err
			}
			if name == formula.Name {
				if rpath {
					target, exists, err := resolveKegLink(tree, keg, filepath.Join(prefix, "opt", formula.Name), ownRelative)
					if err != nil {
						return nil, err
					}
					if !exists {
						continue
					}
					name, _, err = owner(target)
					if err != nil {
						return nil, err
					}
					if name != "" && name != formula.Name {
						dependencies[name] = true
					}
					resolved = true
					break // A present in-keg library wins over later run paths.
				}
			} else if name != "" {
				dependencies[name] = true
			}
			resolved = true
		}
		if !resolved {
			return nil, fmt.Errorf("%w: unresolved library %q", model.ErrUnsupported, imported)
		}
	}
	result := make([]string, 0, len(dependencies))
	for name := range dependencies {
		result = append(result, name)
	}
	sort.Strings(result)
	return result, nil
}

// Resolve in-keg symlinks against the staged tree, without following them into
// the live prefix. An external target is returned for explicit owner validation
// even when its dependency has not yet been installed.
func resolveKegLink(tree, keg, opt, relative string) (string, bool, error) {
	target := filepath.Join(keg, relative)
	for hops := 0; hops < 40; hops++ {
		local := ""
		for _, base := range []string{keg, opt} {
			if strings.HasPrefix(target, base+string(filepath.Separator)) {
				local = strings.TrimPrefix(target, base+string(filepath.Separator))
				break
			}
		}
		if local == "" {
			return target, true, nil
		}
		parts := strings.Split(local, string(filepath.Separator))
		redirected := false
		for i := range parts {
			part := filepath.Join(parts[:i+1]...)
			file := filepath.Join(tree, part)
			info, err := os.Lstat(file)
			if errors.Is(err, os.ErrNotExist) {
				return "", false, nil
			}
			if err != nil {
				return "", false, err
			}
			if info.Mode()&os.ModeSymlink != 0 {
				link, err := os.Readlink(file)
				if err != nil {
					return "", false, err
				}
				if !filepath.IsAbs(link) {
					link = filepath.Join(filepath.Dir(filepath.Join(keg, part)), link)
				}
				target = filepath.Clean(link)
				if i+1 < len(parts) {
					target = filepath.Join(target, filepath.Join(parts[i+1:]...))
				}
				redirected = true
				break
			}
		}
		if !redirected {
			return target, true, nil
		}
	}
	return "", false, fmt.Errorf("%w: cyclic linkage symlink", model.ErrUnsupported)
}
