// Package postinstall executes the supported subset of Homebrew's declarative
// install_steps wire format. It never evaluates Ruby or silently skips a step.
package postinstall

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/shqingda/gobrew/internal/model"
)

type Env struct {
	Prefix, Keg, Name, Version string
	Output                     io.Writer
}

type pathSpec struct {
	Path    string `json:"path"`
	Base    string `json:"base"`
	Formula string `json:"formula"`
}

type guard struct {
	Path      string `json:"path"`
	Base      string `json:"base"`
	Formula   string `json:"formula"`
	Condition string `json:"condition"`
	Value     string `json:"value"`
	ID        string `json:"id"`
}

type step struct {
	Type           string            `json:"type"`
	Guards         []guard           `json:"guards"`
	Path           *pathSpec         `json:"path"`
	Source         *pathSpec         `json:"source"`
	Target         *pathSpec         `json:"target"`
	Command        *pathSpec         `json:"command"`
	StdinPath      *pathSpec         `json:"stdin_path"`
	StdoutPath     *pathSpec         `json:"stdout_path"`
	Chdir          *pathSpec         `json:"chdir"`
	Paths          []pathSpec        `json:"paths"`
	Args           []string          `json:"args"`
	Env            map[string]string `json:"env"`
	Content        string            `json:"content"`
	Before         string            `json:"before"`
	After          string            `json:"after"`
	Message        string            `json:"message"`
	Permissions    string            `json:"permissions"`
	Prefix         string            `json:"prefix"`
	Suffix         string            `json:"suffix"`
	ID             string            `json:"id"`
	Force          bool              `json:"force"`
	Recursive      bool              `json:"recursive"`
	SourceGlob     bool              `json:"source_glob"`
	Uninstall      bool              `json:"uninstall"`
	FirstOnly      bool              `json:"first_only"`
	SkipAudit      bool              `json:"skip_audit"`
	NonRecursive   bool              `json:"non_recursive"`
	PrintStdout    bool              `json:"print_stdout"`
	SuppressStderr bool              `json:"suppress_stderr"`
	AllowFailure   bool              `json:"allow_failure"`
	ResolveSource  bool              `json:"resolve_source"`
	Overwrite      *bool             `json:"overwrite"`
	Sudo           json.RawMessage   `json:"sudo"`
}

func decode(raw json.RawMessage) (step, error) {
	var value step
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	err := decoder.Decode(&value)
	return value, err
}

var operationFields = map[string]string{
	"mkdir": "path", "mkdir_p": "path", "touch": "path", "write": "path content overwrite", "warn": "message",
	"symlink":       "source target force source_glob uninstall sudo",
	"copy":          "source target recursive overwrite source_glob",
	"move":          "source target force overwrite source_glob",
	"move_children": "source target", "move_contents": "source target",
	"remove":    "paths recursive sudo",
	"inreplace": "path before after first_only skip_audit",
	"link_dir":  "source target", "link_children": "source target prefix suffix",
	"run":                       "command args env stdin_path stdout_path chdir print_stdout suppress_stderr allow_failure sudo",
	"set_permissions":           "paths permissions non_recursive",
	"change_dylib_id":           "source id resolve_source",
	"compile_gsettings_schemas": "path", "gio_querymodules": "path", "gdk_pixbuf_query_loaders": "",
	"gtk_update_icon_cache": "path", "update_mime_database": "path", "update_desktop_database": "path",
}

var tokenPattern = regexp.MustCompile(`\{\{([^{}]+)\}\}`)
var tokens = map[string]bool{"HOMEBREW_PREFIX": true, "HOMEBREW_CELLAR": true, "formula_name": true, "name": true, "version": true, "version.major": true, "version.major_minor": true, "arch": true, "user": true, "prefix": true, "opt_prefix": true, "bin": true, "sbin": true, "lib": true, "libexec": true, "share": true, "pkgshare": true, "etc": true, "pkgetc": true, "var": true, "frameworks": true, "include": true, "rack": true, "temp": true, "bash_completion": true, "zsh_completion": true, "fish_completion": true, "pwsh_completion": true}
var bases = map[string]bool{"": true, "absolute": true, "relative": true, "prefix": true, "opt_prefix": true, "bin": true, "sbin": true, "lib": true, "libexec": true, "share": true, "pkgshare": true, "etc": true, "pkgetc": true, "var": true, "frameworks": true, "include": true, "rack": true, "temp": true, "homebrew_prefix": true, "formula_opt_prefix": true, "formula_pkgetc": true, "bash_completion": true, "zsh_completion": true, "fish_completion": true, "pwsh_completion": true}

func Validate(steps []json.RawMessage) error {
	for i, raw := range steps {
		if _, err := validateStep(raw); err != nil {
			return fmt.Errorf("%w: post-install step %d: %v", model.ErrUnsupported, i+1, err)
		}
	}
	return nil
}

func validateStep(raw json.RawMessage) (step, error) {
	s, err := decode(raw)
	if err != nil {
		return s, err
	}
	fields, ok := operationFields[s.Type]
	if !ok {
		return s, fmt.Errorf("unknown operation %q", s.Type)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return s, err
	}
	allowed := " type guards " + fields + " "
	for key := range object {
		if !strings.Contains(allowed, " "+key+" ") {
			return s, fmt.Errorf("unsupported %s field %q", s.Type, key)
		}
	}
	if len(s.Sudo) > 0 && string(s.Sudo) != "false" {
		return s, fmt.Errorf("elevated post-install steps are unsupported")
	}
	for _, p := range []*pathSpec{s.Path, s.Source, s.Target, s.Command, s.StdinPath, s.StdoutPath, s.Chdir} {
		if p != nil {
			if err := validatePath(*p); err != nil {
				return s, err
			}
		}
	}
	for _, p := range s.Paths {
		if err := validatePath(p); err != nil {
			return s, err
		}
	}
	for _, g := range s.Guards {
		switch g.Condition {
		case "on":
			if g.Value != "macos" && g.Value != "linux" {
				return s, fmt.Errorf("unknown platform guard %q", g.Value)
			}
		case "if_exists", "unless_exists":
			if err := validatePath(pathSpec{g.Path, g.Base, g.Formula}); err != nil {
				return s, err
			}
		default:
			return s, fmt.Errorf("unknown guard %q", g.Condition)
		}
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return s, err
	}
	if err := validateTemplates(value); err != nil {
		return s, err
	}
	need := func(keys ...string) error {
		for _, key := range keys {
			if value, ok := object[key]; !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return fmt.Errorf("%s requires %s", s.Type, key)
			}
		}
		return nil
	}
	switch s.Type {
	case "mkdir", "mkdir_p", "touch", "write", "inreplace", "compile_gsettings_schemas", "gio_querymodules", "gtk_update_icon_cache", "update_mime_database", "update_desktop_database":
		err = need("path")
	case "copy", "move", "move_children", "move_contents", "symlink", "link_dir", "link_children":
		err = need("source", "target")
	case "run":
		err = need("command")
	case "remove", "set_permissions":
		err = need("paths")
	case "change_dylib_id":
		err = need("source", "id")
	case "warn":
		err = need("message")
	}
	if err != nil {
		return s, err
	}
	if s.Type == "write" {
		if err := need("content"); err != nil {
			return s, err
		}
	}
	if s.Type == "inreplace" {
		if err := need("before", "after"); err != nil {
			return s, err
		}
		if s.Before == "" {
			return s, fmt.Errorf("empty inreplace pattern")
		}
	}
	if s.Type == "set_permissions" {
		if !validPermissions(s.Permissions) {
			return s, fmt.Errorf("unsupported permissions %q", s.Permissions)
		}
	}
	return s, nil
}

func validatePath(p pathSpec) error {
	if !bases[p.Base] {
		return fmt.Errorf("unknown path base %q", p.Base)
	}
	if strings.ContainsRune(p.Path, 0) {
		return fmt.Errorf("NUL in path")
	}
	if (p.Base == "formula_opt_prefix" || p.Base == "formula_pkgetc") && (p.Formula == "" || strings.ContainsAny(p.Formula, "/\\") || p.Formula == ".." || p.Formula == ".") {
		return fmt.Errorf("invalid path formula %q", p.Formula)
	}
	return nil
}

func validateTemplates(value any) error {
	switch v := value.(type) {
	case string:
		for _, match := range tokenPattern.FindAllStringSubmatch(v, -1) {
			if !tokens[match[1]] {
				return fmt.Errorf("unknown template token %q", match[1])
			}
		}
	case []any:
		for _, item := range v {
			if err := validateTemplates(item); err != nil {
				return err
			}
		}
	case map[string]any:
		for _, item := range v {
			if err := validateTemplates(item); err != nil {
				return err
			}
		}
	}
	return nil
}

func validPermissions(s string) bool {
	if s == "u+w" || s == "u-w" || s == "a+r" || s == "a+rx" {
		return true
	}
	if len(s) != 3 && len(s) != 4 {
		return false
	}
	n, err := strconv.ParseUint(s, 8, 12)
	return err == nil && n&07000 == 0
}
