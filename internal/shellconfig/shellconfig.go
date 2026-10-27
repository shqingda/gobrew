// Package shellconfig renders PATH setup and owns gobrew's marked block in the
// user's shell startup file. It never edits text outside its own markers.
package shellconfig

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/shqingda/gobrew/internal/platform"
)

const (
	begin = "# >>> gobrew >>>"
	end   = "# <<< gobrew <<<"
)

// Env holds what a shell needs to run gobrew commands and installed formulae.
// Directories listed first win, so gobrew precedes anything already on PATH.
type Env struct {
	Paths  []string // executable directories, in priority order
	Prefix string   // Formula prefix for GOBREW_PREFIX, MANPATH and INFOPATH; optional
}

func (e Env) Script(shell string) (string, error) {
	var b strings.Builder
	switch shell {
	case "bash", "zsh", "sh":
		if e.Prefix != "" {
			b.WriteString("export GOBREW_PREFIX=" + quote(e.Prefix) + "\n")
		}
		if len(e.Paths) > 0 {
			parts := make([]string, len(e.Paths))
			for i, p := range e.Paths {
				parts[i] = quote(p)
			}
			b.WriteString("export PATH=" + strings.Join(parts, ":") + ":\"$PATH\"\n")
		}
		if e.Prefix != "" {
			b.WriteString("export MANPATH=" + quote(filepath.Join(e.Prefix, "share/man")) + ":\"${MANPATH:-}\"\n")
			b.WriteString("export INFOPATH=" + quote(filepath.Join(e.Prefix, "share/info")) + ":\"${INFOPATH:-}\"\n")
		}
	case "fish":
		fishQuote := func(s string) string {
			return "'" + strings.ReplaceAll(strings.ReplaceAll(s, "\\", "\\\\"), "'", "\\'") + "'"
		}
		if e.Prefix != "" {
			b.WriteString("set -gx GOBREW_PREFIX " + fishQuote(e.Prefix) + "\n")
		}
		if len(e.Paths) > 0 {
			b.WriteString("fish_add_path --move --prepend --path")
			for _, p := range e.Paths {
				b.WriteString(" " + fishQuote(p))
			}
			b.WriteString("\n")
		}
		if e.Prefix != "" {
			b.WriteString("set -gx MANPATH " + fishQuote(filepath.Join(e.Prefix, "share/man")) + " $MANPATH\n")
			b.WriteString("set -gx INFOPATH " + fishQuote(filepath.Join(e.Prefix, "share/info")) + " $INFOPATH\n")
		}
	default:
		return "", fmt.Errorf("unsupported shell %q; choose bash, zsh, sh, or fish", shell)
	}
	return b.String(), nil
}

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

// Profile is the one startup file gobrew manages for the user's login shell.
type Profile struct {
	Shell string
	Path  string
	// Whole means gobrew owns the entire file (fish conf.d) rather than a block.
	Whole bool
}

// Detect chooses the startup file read by every new interactive shell. For zsh
// that is .zshrc, which runs after .zprofile and so also wins over a .zshrc that
// resets PATH. Bash follows its own login lookup so an existing .profile is
// never shadowed by a new .bash_profile.
func Detect(home, shellPath string, getenv func(string) string) (Profile, error) {
	shell := filepath.Base(shellPath)
	switch shell {
	case "zsh":
		dir := getenv("ZDOTDIR")
		if dir == "" {
			dir = home
		}
		return Profile{Shell: shell, Path: filepath.Join(dir, ".zshrc")}, nil
	case "bash":
		for _, name := range []string{".bash_profile", ".bash_login", ".profile"} {
			if _, err := os.Lstat(filepath.Join(home, name)); err == nil {
				return Profile{Shell: shell, Path: filepath.Join(home, name)}, nil
			}
		}
		return Profile{Shell: shell, Path: filepath.Join(home, ".bash_profile")}, nil
	case "fish":
		dir := getenv("XDG_CONFIG_HOME")
		if dir == "" {
			dir = filepath.Join(home, ".config")
		}
		return Profile{Shell: shell, Path: filepath.Join(dir, "fish", "conf.d", "gobrew.fish"), Whole: true}, nil
	}
	return Profile{}, fmt.Errorf("automatic PATH setup does not support shell %q", shellPath)
}

func (p Profile) block(env Env) (string, error) {
	script, err := env.Script(p.Shell)
	if err != nil {
		return "", err
	}
	return begin + "\n# Added by gobrew so new shells find its commands first.\n# 'gobrew self uninstall' removes this block.\n" + script + end + "\n", nil
}

// Ensure installs or refreshes the managed block and reports whether the file
// changed. A file whose gobrew markers are damaged is left untouched.
func (p Profile) Ensure(env Env) (bool, error) {
	block, err := p.block(env)
	if err != nil {
		return false, err
	}
	target, data, mode, err := p.read()
	if err != nil {
		return false, err
	}
	var next []byte
	if p.Whole {
		if len(data) > 0 && !bytes.HasPrefix(data, []byte(begin+"\n")) {
			return false, fmt.Errorf("%s exists and was not written by gobrew", target)
		}
		next = []byte(block)
	} else {
		start, stop, found, err := markers(data, target)
		if err != nil {
			return false, err
		}
		switch {
		case found:
			next = append(append(append([]byte{}, data[:start]...), block...), data[stop:]...)
		case len(data) == 0:
			next = []byte(block)
		default:
			next = append([]byte{}, data...)
			if !bytes.HasSuffix(next, []byte("\n")) {
				next = append(next, '\n')
			}
			next = append(append(next, '\n'), block...)
		}
	}
	if bytes.Equal(next, data) {
		return false, nil
	}
	return true, write(target, next, mode)
}

// Remove deletes only the managed block (or the wholly owned file).
func (p Profile) Remove() (bool, error) {
	target, data, mode, err := p.read()
	if err != nil || len(data) == 0 {
		return false, err
	}
	if p.Whole {
		if !bytes.HasPrefix(data, []byte(begin+"\n")) {
			return false, nil
		}
		if err := os.Remove(target); err != nil {
			return false, err
		}
		return true, platform.SyncDir(filepath.Dir(target))
	}
	start, stop, found, err := markers(data, target)
	if err != nil || !found {
		return false, err
	}
	// Also drop the blank separator line Ensure added before the block.
	if start >= 2 && data[start-1] == '\n' && data[start-2] == '\n' {
		start--
	}
	next := append(append([]byte{}, data[:start]...), data[stop:]...)
	return true, write(target, next, mode)
}

// read follows a symlinked dotfile to its real file so the link survives, and
// accepts only a regular, singly linked file owned by the current user.
func (p Profile) read() (string, []byte, os.FileMode, error) {
	target := p.Path
	if resolved, err := filepath.EvalSymlinks(p.Path); err == nil {
		target = resolved
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", nil, 0, err
	}
	info, err := os.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		return target, nil, 0644, nil
	}
	if err != nil {
		return "", nil, 0, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || !ok || int(stat.Uid) != os.Getuid() || stat.Nlink != 1 {
		return "", nil, 0, fmt.Errorf("refusing to edit %s: not a regular file owned by you", target)
	}
	data, err := os.ReadFile(target)
	return target, data, info.Mode().Perm(), err
}

func markers(data []byte, path string) (int, int, bool, error) {
	start := bytes.Index(data, []byte(begin+"\n"))
	if start < 0 {
		if bytes.Contains(data, []byte(end)) {
			return 0, 0, false, fmt.Errorf("damaged gobrew block in %s", path)
		}
		return 0, 0, false, nil
	}
	if start > 0 && data[start-1] != '\n' {
		return 0, 0, false, fmt.Errorf("damaged gobrew block in %s", path)
	}
	rel := bytes.Index(data[start:], []byte(end+"\n"))
	if rel < 0 || bytes.Contains(data[start+rel+len(end):], []byte(begin)) {
		return 0, 0, false, fmt.Errorf("damaged gobrew block in %s", path)
	}
	return start, start + rel + len(end) + 1, true, nil
}

// write replaces the file atomically so a crash leaves either version intact.
func write(path string, data []byte, mode os.FileMode) (err error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".gobrew-shell-")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err = f.Write(data); err == nil {
		err = f.Chmod(mode)
	}
	if err == nil {
		err = platform.SyncFile(f)
	}
	if err = errors.Join(err, f.Close()); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	return platform.SyncDir(dir)
}
