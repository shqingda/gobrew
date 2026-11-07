package cli

import (
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/shqingda/gobrew/internal/model"
)

// kegOnlyCaveat ports Homebrew's Caveats#keg_only_text for a keg that is not
// linked. An empty keg means it is not installed, so only the reason applies.
func (a *application) kegOnlyCaveat(f model.Formula, prefix, keg string) string {
	first := f.Name + " is keg-only, which means it was not symlinked into " + prefix
	if reason := kegOnlyReason(f); reason != "" {
		first += ",\nbecause " + reason
	}
	lines := []string{first + "."}
	if keg == "" {
		return lines[0]
	}
	opt := filepath.Join(prefix, "opt", f.Name)
	has := func(dir string) bool { return isDir(filepath.Join(keg, dir)) }
	lines = append(lines, a.pathHint("If you need to have "+f.Name+" first in your PATH, run:", opt, keg)...)
	if has("lib") || has("include") {
		lines = append(lines, "", "For compilers to find "+f.Name+" you may need to set:")
		if has("lib") {
			lines = append(lines, "  "+a.exportValue("LDFLAGS", "-L"+filepath.Join(opt, "lib")))
		}
		if has("include") {
			lines = append(lines, "  "+a.exportValue("CPPFLAGS", "-I"+filepath.Join(opt, "include")))
		}
		if _, err := exec.LookPath("pkgconf"); err == nil && (has("lib/pkgconfig") || has("share/pkgconfig")) {
			lines = append(lines, "", "For pkgconf to find "+f.Name+" you may need to set:")
			for _, dir := range []string{"lib", "share"} {
				if has(dir + "/pkgconfig") {
					lines = append(lines, "  "+a.exportValue("PKG_CONFIG_PATH", filepath.Join(opt, dir, "pkgconfig")))
				}
			}
		}
		if _, err := exec.LookPath("cmake"); err == nil && (has("lib/cmake") || has("share/cmake")) {
			lines = append(lines, "", "For cmake to find "+f.Name+" you may need to set:", "  "+a.exportValue("CMAKE_PREFIX_PATH", opt))
		}
	}
	return strings.Join(lines, "\n")
}

// kegOnlyReason ports KegOnlyReason#to_s. A catalog from before gobrew kept the
// reason has none to show.
func kegOnlyReason(f model.Formula) string {
	if f.KegOnlyExplanation != "" {
		return strings.TrimSpace(f.KegOnlyExplanation)
	}
	switch f.KegOnlyReason {
	case model.KegOnlyVersioned:
		return "this is an alternate version of another formula"
	case ":provided_by_macos":
		return "macOS already provides this software and installing another version in\nparallel can cause all kinds of trouble"
	case ":shadowed_by_macos":
		return "macOS provides similar software and installing this software in\nparallel can cause all kinds of trouble"
	}
	return strings.TrimPrefix(f.KegOnlyReason, ":")
}

// pathHint returns a blank line, the heading and one command for each of the
// keg's command directories, or nothing if it has none.
func (a *application) pathHint(heading, opt, keg string) []string {
	var commands []string
	for _, dir := range []string{"bin", "sbin"} {
		if isDir(filepath.Join(keg, dir)) {
			commands = append(commands, "  "+a.prependPath(filepath.Join(opt, dir)))
		}
	}
	if len(commands) == 0 {
		return nil
	}
	return append([]string{"", heading}, commands...)
}

// prependPath ports Utils::Shell.prepend_path_in_profile for the login shell.
func (a *application) prependPath(path string) string {
	profile, err := a.profile()
	if err != nil {
		return "export PATH=" + brewQuote(path) + ":$PATH"
	}
	if profile.Shell == "fish" {
		return "fish_add_path " + brewQuote(path)
	}
	return `printf '%s\n' ` + brewQuote("export PATH="+brewQuote(path)+":$PATH") + " >> " + brewQuote(tildePath(profile.Path))
}

func (a *application) exportValue(key, value string) string {
	if profile, err := a.profile(); err == nil && profile.Shell == "fish" {
		return "set -gx " + key + " " + brewQuote(value)
	}
	return "export " + key + "=" + brewQuote(value)
}

var unsafeShellChar = regexp.MustCompile(`[^A-Za-z0-9_\-.,:/@~+\n]`)

// brewQuote ports Utils::Shell.sh_quote, so printed commands match brew's.
func brewQuote(s string) string {
	if s == "" {
		return "''"
	}
	s = unsafeShellChar.ReplaceAllStringFunc(s, func(c string) string { return `\` + c })
	return strings.ReplaceAll(s, "\n", "'\n'")
}
