package cli

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// completionsDir keeps gobrew's own completion scripts inside the managed
// store, so every uninstall path that removes the store removes them too.
func completionsDir(binDir string) string { return filepath.Join(binDir, ".gobrew", "completions") }

// completionScripts are generated from this executable's commands for both
// gobrew and gob. They are static files; at a Tab press they ask the binary's
// hidden __complete command, so they keep matching the installed version.
func completionScripts() (map[string][]byte, error) {
	generate := func(name string, gen func(*cobra.Command, *bytes.Buffer) error) ([]byte, error) {
		// A fresh application: building commands resets the flags they bind.
		root := (&application{}).root()
		root.Use = name
		var b bytes.Buffer
		err := gen(root, &b)
		return b.Bytes(), err
	}
	zsh, err := generate("gobrew", func(c *cobra.Command, b *bytes.Buffer) error { return c.GenZshCompletion(b) })
	if err != nil {
		return nil, err
	}
	// One zsh function serves both names; zsh passes the typed one along.
	text := string(zsh)
	if !strings.HasPrefix(text, "#compdef gobrew\n") || !strings.Contains(text, "compdef _gobrew gobrew\n") {
		return nil, errors.New("unexpected zsh completion header")
	}
	text = strings.Replace(text, "#compdef gobrew\n", "#compdef gobrew gob\n", 1)
	text = strings.Replace(text, "compdef _gobrew gobrew\n", "compdef _gobrew gobrew gob\n", 1)
	scripts := map[string][]byte{"_gobrew": []byte(text)}
	for file, gen := range map[string]func(*cobra.Command, *bytes.Buffer) error{
		"gobrew.bash": func(c *cobra.Command, b *bytes.Buffer) error { return c.GenBashCompletionV2(b, true) },
		"gobrew.fish": func(c *cobra.Command, b *bytes.Buffer) error { return c.GenFishCompletion(b, true) },
	} {
		var all []byte
		for _, name := range []string{"gobrew", "gob"} {
			script, err := generate(name, gen)
			if err != nil {
				return nil, err
			}
			all = append(all, script...)
		}
		scripts[file] = all
	}
	return scripts, nil
}

// writeCompletions refreshes the scripts in binDir's store, one file at a
// time through a rename, refusing a completions path that is not a directory.
func writeCompletions(binDir string) error {
	scripts, err := completionScripts()
	if err != nil {
		return err
	}
	dir := completionsDir(binDir)
	if err := os.Mkdir(dir, 0755); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	if info, err := os.Lstat(dir); err != nil || !info.IsDir() {
		return fmt.Errorf("not a real directory: %s", dir)
	}
	for name, data := range scripts {
		if current, err := os.ReadFile(filepath.Join(dir, name)); err == nil && bytes.Equal(current, data) {
			continue
		}
		tmp, err := os.CreateTemp(dir, "."+name+".*")
		if err != nil {
			return err
		}
		_, err = tmp.Write(data)
		err = errors.Join(err, tmp.Chmod(0644), tmp.Close())
		if err == nil {
			err = os.Rename(tmp.Name(), filepath.Join(dir, name))
		}
		if err != nil {
			os.Remove(tmp.Name())
			return err
		}
	}
	return nil
}
