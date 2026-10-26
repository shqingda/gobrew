package artifact

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// extractBottle uses an os.Root for every write and defers links until regular
// files are complete. Even a symlink-before-file archive cannot redirect writes.
var errChecksum = errors.New("cached bottle checksum mismatch")

type extractionOptions struct {
	SHA256 string
	Prefix string
}

func extractBottle(ctx context.Context, blob, dest string, options ...extractionOptions) error {
	var cfg extractionOptions
	if len(options) > 0 {
		cfg = options[0]
	}
	file, err := os.Open(blob)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	gz, err := gzip.NewReader(io.TeeReader(file, hash))
	if err != nil {
		return err
	}
	defer gz.Close()
	if err := os.Mkdir(dest, 0700); err != nil {
		return err
	}
	root, err := os.OpenRoot(dest)
	if err != nil {
		return err
	}
	defer root.Close()
	reader := tar.NewReader(gz)
	type link struct {
		name, target string
		hard         bool
		mode         os.FileMode
	}
	var links []link
	seen := map[string]bool{}
	var count int
	var size int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		name, err := archivePath(header.Name)
		if err != nil {
			return err
		}
		if path.Base(name) == ".gobrew-owner" {
			return fmt.Errorf("bottle uses reserved ownership marker: %s", name)
		}
		if name == "." {
			if header.Typeflag == tar.TypeDir {
				continue
			}
			return errors.New("invalid archive root")
		}
		count++
		size += header.Size
		if count > 1_000_000 || size > 64<<30 || header.Size < 0 {
			return errors.New("bottle exceeds extraction limits")
		}
		if seen[name] {
			return fmt.Errorf("duplicate archive path %q", name)
		}
		seen[name] = true
		if err := root.MkdirAll(path.Dir(name), 0755); err != nil {
			return err
		}
		mode := os.FileMode(header.Mode) & 0777 // Never preserve privilege bits.
		switch header.Typeflag {
		case tar.TypeDir:
			if err := root.MkdirAll(name, mode|0700); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			out, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode|0600)
			if err != nil {
				return err
			}
			_, copyErr := io.CopyN(out, reader, header.Size)
			closeErr := out.Close()
			if err := errors.Join(copyErr, closeErr); err != nil {
				return err
			}
		case tar.TypeSymlink:
			target, err := safeLink(name, header.Linkname, cfg.Prefix)
			if err != nil {
				return err
			}
			links = append(links, link{name, target, false, mode})
		case tar.TypeLink:
			target, err := archivePath(header.Linkname)
			if err != nil {
				return err
			}
			links = append(links, link{name, target, true, mode})
		default:
			return fmt.Errorf("unsupported tar entry %q (type %d)", name, header.Typeflag)
		}
	}
	// Drain gzip to validate its trailer even when tar stopped at its EOF blocks.
	if _, err := io.Copy(io.Discard, gz); err != nil {
		return err
	}
	if cfg.SHA256 != "" && hex.EncodeToString(hash.Sum(nil)) != cfg.SHA256 {
		return errChecksum
	}
	for _, link := range links {
		if err := ctx.Err(); err != nil {
			return err
		}
		if link.hard {
			info, err := root.Lstat(link.target)
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("hardlink target is not regular: %s", link.target)
			}
			in, err := root.Open(link.target)
			if err != nil {
				return err
			}
			out, err := root.OpenFile(link.name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
			if err != nil {
				in.Close()
				return err
			}
			_, copyErr := io.Copy(out, in)
			err = errors.Join(copyErr, in.Close(), out.Close())
			if err != nil {
				return err
			}
		} else if err := root.Symlink(link.target, link.name); err != nil {
			return err
		}
	}
	return nil
}

func archivePath(name string) (string, error) {
	if strings.ContainsRune(name, 0) || strings.Contains(name, "\\") || path.IsAbs(name) {
		return "", fmt.Errorf("unsafe archive path %q", name)
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return "", fmt.Errorf("unsafe archive path %q", name)
		}
	}
	clean := path.Clean(name)
	if !filepath.IsLocal(clean) {
		return "", fmt.Errorf("unsafe archive path %q", name)
	}
	return clean, nil
}

func safeLink(name, target, prefix string) (string, error) {
	if target == "" || strings.ContainsAny(target, "\x00\\") {
		return "", fmt.Errorf("unsafe link target %q", target)
	}
	if path.IsAbs(target) {
		for _, prefix := range []string{"/opt/homebrew/", "/usr/local/", "/usr/lib/", "/System/Library/"} {
			if strings.HasPrefix(path.Clean(target), prefix) {
				return target, nil
			}
		}
		return "", fmt.Errorf("unsupported absolute link %q", target)
	}
	resolved := path.Clean(path.Join(path.Dir(name), target))
	if resolved == ".." || strings.HasPrefix(resolved, "../") {
		// A bottle's archive root corresponds to prefix/Cellar. Homebrew's
		// certifi links, for example, leave that root once to reach prefix/etc.
		// Allow only these managed sibling directories, never an arbitrary
		// escape or gobrew's private state. Convert to the planned prefix now;
		// os.Root creates this link only after every regular file is complete.
		if filepath.IsAbs(prefix) && filepath.Clean(prefix) == prefix && prefix != string(filepath.Separator) {
			for _, dir := range []string{"etc", "var", "opt", "Cellar"} {
				allowed := "../" + dir
				if resolved == allowed || strings.HasPrefix(resolved, allowed+"/") {
					return filepath.Join(prefix, filepath.FromSlash(strings.TrimPrefix(resolved, "../"))), nil
				}
			}
		}
		return "", fmt.Errorf("link escapes archive: %q -> %q", name, target)
	}
	return target, nil
}
