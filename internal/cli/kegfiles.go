package cli

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/shqingda/gobrew/internal/model"
)

// kegFiles lists a keg the way brew list does off a terminal, which is find's
// -not -type d: symlinks are listed and never followed. gobrew's ownership
// marker is not part of the bottle.
func kegFiles(keg string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(keg, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && entry.Name() != ".DS_Store" && path != filepath.Join(keg, model.OwnerMarker) {
			files = append(files, path)
		}
		return nil
	})
	return files, err
}

// prettyKeg ports Homebrew's PrettyListing, which brew list prints on a
// terminal: commands one by one and other directories summarized.
func prettyKeg(keg string) ([]string, error) {
	entries, err := os.ReadDir(keg)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(entries, func(a, b fs.DirEntry) int {
		return strings.Compare(strings.ToLower(a.Name()), strings.ToLower(b.Name()))
	})
	var lines []string
	for _, entry := range entries {
		path := filepath.Join(keg, entry.Name())
		var more []string
		switch entry.Name() {
		case ".brew", model.OwnerMarker:
		case "bin", "sbin":
			more, err = regularFiles(path)
		case "lib":
			more, err = summarizeDir(path, isLibrary)
		default:
			switch {
			case !isDir(path):
				if listedMetafile(entry.Name()) {
					more = []string{path}
				}
			case entry.Type()&fs.ModeSymlink != 0:
				var target string
				target, err = os.Readlink(path)
				more = []string{path + " -> " + target}
			default:
				more, err = summarizeDir(path, nil)
			}
		}
		if err != nil {
			return nil, err
		}
		lines = append(lines, more...)
	}
	return lines, nil
}

// summarizeDir ports PrettyListing#print_dir: each subdirectory, then the
// remaining files, become one line. Files that show pass through by themselves.
func summarizeDir(root string, show func(path string, entry fs.DirEntry) bool) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var lines, dirs, remaining []string
	other := ""
	for _, entry := range entries {
		path := filepath.Join(root, entry.Name())
		switch {
		case isDir(path):
			dirs = append(dirs, path)
		case show != nil && show(path, entry):
			lines = append(lines, path)
			other = "other "
		case entry.Name() != ".DS_Store":
			remaining = append(remaining, path)
		}
	}
	for _, dir := range dirs {
		files, err := regularFiles(dir)
		if err != nil {
			return nil, err
		}
		lines = append(lines, summary(files, dir, "")...)
	}
	return append(lines, summary(remaining, root, other)...), nil
}

func summary(files []string, root, other string) []string {
	switch len(files) {
	case 0:
		return nil
	case 1:
		return files
	}
	return []string{fmt.Sprintf("%s/ (%d %sfiles)", root, len(files), other)}
}

// regularFiles walks without following links, and unlike kegFiles treats a
// link to a directory as a directory, as Ruby's Pathname#directory? does.
func regularFiles(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !isDir(path) {
			files = append(files, path)
		}
		return nil
	})
	return files, err
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// isLibrary keeps real libraries; dylibs have version symlinks not worth a line.
func isLibrary(path string, entry fs.DirEntry) bool {
	return entry.Type()&fs.ModeSymlink == 0 && slices.Contains([]string{".cps", ".dylib", ".pc"}, filepath.Ext(path))
}

// listedMetafile ports Homebrew's Metafiles.list?: top-level keg files are
// listed unless they are licenses or documentation.
func listedMetafile(name string) bool {
	if name == ".DS_Store" || name == "INSTALL_RECEIPT.json" {
		return false
	}
	name = strings.ToLower(name)
	first := name
	if i := strings.IndexAny(name, ".-"); i >= 0 {
		first = name[:i]
	}
	if slices.Contains([]string{"copying", "copyright", "license", "licence"}, first) {
		return false
	}
	ext := filepath.Ext(name)
	if slices.Contains([]string{".adoc", ".asc", ".asciidoc", ".creole", ".html", ".markdown", ".md", ".mdown", ".mediawiki", ".mkdn", ".org", ".pod", ".rdoc", ".rst", ".rtf", ".textile", ".txt", ".wiki"}, ext) {
		name = strings.TrimSuffix(name, ext)
	}
	return !slices.Contains([]string{"about", "authors", "changelog", "changes", "history", "news", "notes", "notice", "readme", "todo"}, name)
}
