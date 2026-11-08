package engine

import (
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/shqingda/gobrew/internal/artifact"
	"github.com/shqingda/gobrew/internal/model"
	"github.com/shqingda/gobrew/internal/resolver"
)

func (e *Engine) CacheDir() string { return e.config.CacheDir }

// BottleCache is the file brew --cache names for a formula: its compressed
// bottle in the download cache, whether or not it was downloaded. An empty
// tag means the host's bottle. It returns the tag looked for, normalized like
// brew's, and ok is false when no bottle fits it.
func (e *Engine) BottleCache(f model.Formula, tag string) (path, wanted string, ok bool, err error) {
	var b model.Bottle
	if tag == "" {
		host, err := e.Host()
		if err != nil {
			return "", "", false, err
		}
		wanted = resolver.HostTag(host)
		if wanted == "" {
			wanted = "arm64_macos" + strconv.Itoa(host.Major)
		}
		b, ok = resolver.BottleFor(f, wanted, host.Arch, host.Major)
	} else {
		wanted = resolver.NormalizeTag(tag)
		arch, major := resolver.TagTarget(wanted)
		b, ok = resolver.BottleFor(f, wanted, arch, major)
	}
	if !ok {
		return "", wanted, false, nil
	}
	if sum, err := hex.DecodeString(b.SHA256); err != nil || len(sum) != 32 {
		return "", wanted, false, errors.New("invalid bottle checksum for " + f.Name)
	}
	return artifact.BlobPath(e.config.CacheDir, b.SHA256), wanted, true, nil
}

// unbrewedSkip is brew --prefix --unbrewed's UNBREWED_EXCLUDE_PATHS, matched
// like find -path, where * also matches a slash.
var unbrewedSkip = func() *regexp.Regexp {
	patterns := []string{
		"*/.keepme", ".github/*", "bin/brew", "completions/zsh/_brew", "docs/*",
		"lib/gdk-pixbuf-2.0/*", "lib/gio/*", "lib/node_modules/*",
		"lib/python[23].[0-9]/*", "lib/python3.[0-9][0-9]/*", "lib/pypy/*", "lib/pypy3/*",
		"lib/ruby/gems/[12].*", "lib/ruby/site_ruby/[12].*", "lib/ruby/vendor_ruby/[12].*",
		"manpages/brew.1", "share/pypy/*", "share/pypy3/*", "share/info/dir",
		"share/man/whatis", "share/mime/*", "texlive/*",
	}
	for i, p := range patterns {
		p = regexp.QuoteMeta(p)
		p = strings.ReplaceAll(p, `\*`, ".*")
		p = strings.ReplaceAll(p, `\[`, "[")
		patterns[i] = strings.ReplaceAll(p, `\]`, "]")
	}
	return regexp.MustCompile(`^(?:` + strings.Join(patterns, "|") + `)$`)
}()

// Unbrewed lists the regular files in the prefix that no package installed,
// as brew --prefix --unbrewed does: links are skipped, and so are the
// Cellar, etc, var and gobrew's own state directory. Paths are relative to
// the prefix, in lexical order.
func (e *Engine) Unbrewed() ([]string, error) {
	entries, err := os.ReadDir(e.config.Prefix)
	if err != nil {
		return nil, err
	}
	skip := []string{"Library", "Cellar", "Caskroom", ".git", "etc", "var", filepath.Base(e.store().Dir())}
	var files []string
	for _, entry := range entries {
		if !entry.IsDir() || slices.Contains(skip, entry.Name()) {
			continue
		}
		err := filepath.WalkDir(filepath.Join(e.config.Prefix, entry.Name()), func(path string, d fs.DirEntry, err error) error {
			if err != nil || !d.Type().IsRegular() || d.Name() == ".DS_Store" {
				return err
			}
			rel, err := filepath.Rel(e.config.Prefix, path)
			if err == nil && !unbrewedSkip.MatchString(rel) {
				files = append(files, rel)
			}
			return err
		})
		if err != nil {
			return nil, err
		}
	}
	return files, nil
}
