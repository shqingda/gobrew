package shellconfig

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureIsIdempotentAndRemoveRestoresTheFile(t *testing.T) {
	home := t.TempDir()
	rc := filepath.Join(home, ".zshrc")
	original := "export PATH=/opt/homebrew/bin:/usr/bin:/bin\nalias ll='ls -l'"
	if err := os.WriteFile(rc, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	p := Profile{Shell: "zsh", Path: rc}
	env := Env{Paths: []string{"/opt/gobrew/bin"}, Prefix: "/opt/gobrew"}
	for i, want := range []bool{true, false} {
		changed, err := p.Ensure(env)
		if err != nil || changed != want {
			t.Fatalf("Ensure #%d = %v, %v", i, changed, err)
		}
	}
	// A changed environment replaces the block in place, keeping later lines.
	if err := appendFile(rc, "export EDITOR=vim\n"); err != nil {
		t.Fatal(err)
	}
	env.Paths = append([]string{"/Users/me/.local/bin"}, env.Paths...)
	if changed, err := p.Ensure(env); err != nil || !changed {
		t.Fatalf("refresh = %v, %v", changed, err)
	}
	data, _ := os.ReadFile(rc)
	if strings.Count(string(data), begin) != 1 || !strings.HasSuffix(string(data), "export EDITOR=vim\n") {
		t.Fatalf("block not replaced in place:\n%s", data)
	}
	if info, _ := os.Stat(rc); info.Mode().Perm() != 0600 {
		t.Fatalf("mode changed to %v", info.Mode())
	}
	if removed, err := p.Remove(); err != nil || !removed {
		t.Fatalf("Remove = %v, %v", removed, err)
	}
	data, _ = os.ReadFile(rc)
	if string(data) != original+"\nexport EDITOR=vim\n" {
		t.Fatalf("Remove changed user text:\n%q", data)
	}
}

func TestBlockWinsOverAStartupFileThatResetsPath(t *testing.T) {
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh unavailable")
	}
	home := t.TempDir()
	rc := filepath.Join(home, ".zshrc")
	if err := os.WriteFile(rc, []byte("export PATH=/opt/homebrew/bin:/usr/bin:/bin\n"), 0644); err != nil {
		t.Fatal(err)
	}
	p, err := Detect(home, "/bin/zsh", func(string) string { return "" })
	if err != nil || p.Path != rc {
		t.Fatalf("Detect = %+v, %v", p, err)
	}
	if _, err := p.Ensure(Env{Paths: []string{"/a b/bin", "/opt/gobrew/bin"}, Prefix: "/opt/gobrew"}); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(zsh, "-c", `source "$1"; print -r -- $PATH`, "zsh", rc)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + home}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if !strings.HasPrefix(string(out), "/a b/bin:/opt/gobrew/bin:/opt/homebrew/bin:") {
		t.Fatalf("gobrew does not come first: %s", out)
	}
}

func TestUnsafeOrForeignFilesAreNotEdited(t *testing.T) {
	env := Env{Paths: []string{"/opt/gobrew/bin"}}
	dir := t.TempDir()
	damaged := filepath.Join(dir, "damaged")
	if err := os.WriteFile(damaged, []byte(begin+"\nexport PATH=x\n"), 0644); err != nil {
		t.Fatal(err)
	}
	hard := filepath.Join(dir, "hard")
	if err := os.WriteFile(hard, nil, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(hard, filepath.Join(dir, "hard2")); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(dir, "conf.d", "gobrew.fish")
	if err := os.MkdirAll(filepath.Dir(foreign), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(foreign, []byte("set -gx X 1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, p := range []Profile{{Shell: "zsh", Path: damaged}, {Shell: "zsh", Path: hard}, {Shell: "fish", Path: foreign, Whole: true}} {
		before, _ := os.ReadFile(p.Path)
		if _, err := p.Ensure(env); err == nil {
			t.Fatalf("edited %s", p.Path)
		}
		if after, _ := os.ReadFile(p.Path); string(after) != string(before) {
			t.Fatalf("%s changed", p.Path)
		}
	}
}

func TestSymlinkedDotfileKeepsItsLink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "dotfiles", "zshrc")
	if err := os.MkdirAll(filepath.Dir(real), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(real, []byte("# mine\n"), 0644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, ".zshrc")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if _, err := (Profile{Shell: "zsh", Path: link}).Ensure(Env{Paths: []string{"/opt/gobrew/bin"}}); err != nil {
		t.Fatal(err)
	}
	if target, err := os.Readlink(link); err != nil || target != real {
		t.Fatalf("symlink replaced: %v %v", target, err)
	}
	if data, _ := os.ReadFile(real); !strings.Contains(string(data), "/opt/gobrew/bin") {
		t.Fatal("real file not updated")
	}
}

func TestBashUsesTheLoginFileItAlreadyReads(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".profile"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	p, err := Detect(home, "/bin/bash", func(string) string { return "" })
	if err != nil || filepath.Base(p.Path) != ".profile" {
		t.Fatalf("Detect = %+v, %v; a new .bash_profile would shadow .profile", p, err)
	}
}

func appendFile(path, text string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	_, err = f.WriteString(text)
	return errors.Join(err, f.Close())
}
