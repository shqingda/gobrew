package postinstall

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

func (r *runner) base(base, formula string) string {
	switch base {
	case "homebrew_prefix":
		return r.env.Prefix
	case "prefix":
		return r.env.Keg
	case "opt_prefix":
		return filepath.Join(r.env.Prefix, "opt", r.env.Name)
	case "etc", "var":
		return filepath.Join(r.env.Prefix, base)
	case "pkgetc":
		return filepath.Join(r.env.Prefix, "etc", r.env.Name)
	case "pkgshare":
		return filepath.Join(r.env.Keg, "share", r.env.Name)
	case "frameworks":
		return filepath.Join(r.env.Keg, "Frameworks")
	case "rack":
		return filepath.Dir(r.env.Keg)
	case "temp":
		return filepath.Join(r.env.Prefix, "var", "gobrew", "tmp")
	case "formula_opt_prefix":
		return filepath.Join(r.env.Prefix, "opt", formula)
	case "formula_pkgetc":
		return filepath.Join(r.env.Prefix, "etc", formula)
	case "bash_completion":
		return filepath.Join(r.env.Keg, "etc", "bash_completion.d")
	case "zsh_completion":
		return filepath.Join(r.env.Keg, "share", "zsh", "site-functions")
	case "fish_completion":
		return filepath.Join(r.env.Keg, "share", "fish", "vendor_completions.d")
	case "pwsh_completion":
		return filepath.Join(r.env.Keg, "share", "pwsh", "completions")
	default:
		return filepath.Join(r.env.Keg, base)
	}
}

func (r *runner) expand(s string) string {
	return tokenPattern.ReplaceAllStringFunc(s, func(match string) string {
		token := strings.TrimSuffix(strings.TrimPrefix(match, "{{"), "}}")
		switch token {
		case "HOMEBREW_PREFIX":
			return r.env.Prefix
		case "HOMEBREW_CELLAR":
			return filepath.Join(r.env.Prefix, "Cellar")
		case "name", "formula_name":
			return r.env.Name
		case "version":
			return r.env.Version
		case "version.major":
			return strings.Split(r.env.Version, ".")[0]
		case "version.major_minor":
			parts := strings.Split(r.env.Version, ".")
			return strings.Join(parts[:min(2, len(parts))], ".")
		case "arch":
			return runtime.GOARCH
		case "user":
			return os.Getenv("USER")
		default:
			return r.base(token, "")
		}
	})
}

func (r *runner) path(spec *pathSpec) (string, error) {
	if spec == nil {
		return "", errors.New("missing path")
	}
	p := r.expand(spec.Path)
	if spec.Base != "" && spec.Base != "absolute" && spec.Base != "relative" {
		if filepath.IsAbs(p) {
			return "", errors.New("absolute path with relative base")
		}
		p = filepath.Join(r.base(spec.Base, spec.Formula), p)
	}
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("expected absolute path, got %q", p)
	}
	p = filepath.Clean(p)
	if !within(r.env.Prefix, p) {
		return "", fmt.Errorf("post-install path escapes prefix: %s", p)
	}
	return p, nil
}

func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && (rel == "." || filepath.IsLocal(rel))
}

// relative resolves existing parent links before entering os.Root. Homebrew's
// opt symlinks are absolute; their resolved destinations must still be owned by
// this prefix. Subsequent operations remain confined by os.Root against races.

func (r *runner) relative(path string, follow bool) (string, error) {
	if !within(r.env.Prefix, path) {
		return "", fmt.Errorf("path escapes prefix: %s", path)
	}
	probe := path
	var tail []string
	if !follow {
		tail = append(tail, filepath.Base(probe))
		probe = filepath.Dir(probe)
	}
	for {
		resolved, err := filepath.EvalSymlinks(probe)
		if err == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, tail[i])
			}
			if !within(r.env.Prefix, resolved) {
				return "", fmt.Errorf("symlink escapes prefix: %s", path)
			}
			return filepath.Rel(r.env.Prefix, resolved)
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		if probe == r.env.Prefix || probe == filepath.Dir(probe) {
			return "", err
		}
		tail = append(tail, filepath.Base(probe))
		probe = filepath.Dir(probe)
	}
}

func (r *runner) matchGuards(guards []guard) (bool, error) {
	for _, g := range guards {
		keyBytes, _ := json.Marshal(g)
		key := string(keyBytes)
		match, ok := r.guards[key]
		if !ok {
			if g.Condition == "on" {
				match = (g.Value == "macos" && runtime.GOOS == "darwin") || (g.Value == "linux" && runtime.GOOS == "linux")
			} else {
				p, err := r.path(&pathSpec{g.Path, g.Base, g.Formula})
				if err != nil {
					return false, err
				}
				paths, err := r.glob(p)
				if err != nil {
					return false, err
				}
				match = false
				for _, candidate := range paths {
					// Homebrew's exists guard follows the final symlink; a dangling
					// link must not satisfy it (unlike remove's glob matching).
					if _, statErr := os.Stat(candidate); statErr == nil {
						match = true
						break
					}
				}
				if g.Condition == "unless_exists" {
					match = !match
				}
			}
			r.guards[key] = match
		}
		if !match {
			return false, nil
		}
	}
	return true, nil
}

func (r *runner) glob(pattern string) ([]string, error) {
	patterns, err := braceExpand(pattern)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, pattern := range patterns {
		rel, err := filepath.Rel(r.env.Prefix, pattern)
		if err != nil {
			return nil, err
		}
		var walk func(string, []string) error
		walk = func(base string, parts []string) error {
			if len(parts) == 0 {
				if _, err := os.Lstat(base); err == nil {
					if _, err := r.relative(base, false); err != nil {
						return err
					}
					seen[base] = true
				}
				return nil
			}
			if parts[0] == "**" {
				return filepath.WalkDir(base, func(path string, entry os.DirEntry, err error) error {
					if errors.Is(err, os.ErrNotExist) {
						return nil
					}
					if err != nil {
						return err
					}
					if entry.IsDir() {
						return walk(path, parts[1:])
					}
					return nil
				})
			}
			matches, err := filepath.Glob(filepath.Join(base, parts[0]))
			if err != nil {
				return err
			}
			for _, match := range matches {
				if _, err := r.relative(match, false); err != nil {
					return err
				}
				if err := walk(match, parts[1:]); err != nil {
					return err
				}
			}
			return nil
		}
		if err := walk(r.env.Prefix, strings.Split(rel, string(filepath.Separator))); err != nil {
			return nil, err
		}
	}
	paths := make([]string, 0, len(seen))
	for p := range seen {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths, nil
}

func braceExpand(pattern string) ([]string, error) {
	start := strings.IndexByte(pattern, '{')
	if start < 0 {
		return []string{pattern}, nil
	}
	end := strings.IndexByte(pattern[start:], '}')
	if end < 0 {
		return nil, errors.New("unclosed glob brace")
	}
	end += start
	if strings.Contains(pattern[start+1:end], "{") {
		return nil, errors.New("nested glob braces unsupported")
	}
	var out []string
	for _, choice := range strings.Split(pattern[start+1:end], ",") {
		expanded, err := braceExpand(pattern[:start] + choice + pattern[end+1:])
		if err != nil {
			return nil, err
		}
		out = append(out, expanded...)
		if len(out) > 128 {
			return nil, errors.New("too many glob alternatives")
		}
	}
	return out, nil
}
