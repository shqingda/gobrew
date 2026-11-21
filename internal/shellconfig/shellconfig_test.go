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

// An unset MANPATH must end up with an empty element, which man(1) reads as
// "and the system defaults"; otherwise `man ls` stops working.
// Like brew shellenv: MANPATH is never set when unset (man then finds pages
// beside each PATH entry), an existing one is normalized to one leading colon
// so man keeps its defaults, and INFOPATH gains the prefix with a trailing colon.
func TestScriptsNormalizeManpathLikeBrew(t *testing.T) {
	env := Env{Paths: []string{"/opt/gobrew/bin"}, Prefix: "/opt/gobrew"}
	for shell, run := range map[string][]string{
		"sh":   {"sh", "-c"},
		"zsh":  {"/bin/zsh", "-f", "-c"},
		"bash": {"/bin/bash", "--noprofile", "--norc", "-c"},
		"csh":  {"/bin/csh", "-f", "-c"},
	} {
		if _, err := exec.LookPath(run[0]); err != nil {
			t.Logf("skipping %s: %v", shell, err)
			continue
		}
		script, err := env.Script(shell)
		if err != nil {
			t.Fatal(err)
		}
		probe := `printf '%s|%s|%s' "$MANPATH" "$INFOPATH" "$PATH"`
		if shell == "csh" {
			// csh rejects reading an unset variable, and an empty command
			// substitution would drop the word, so it gets a leading x.
			probe = "printf 'x%s|%s|%s' \"x`printenv MANPATH`\" \"$INFOPATH\" \"$PATH\" | cut -c3-"
		}
		for manpath, want := range map[string]string{"": "", "::/usr/share/man:/x::": ":/usr/share/man:/x"} {
			cmd := exec.Command(run[0], append(run[1:], script+"\n"+probe)...)
			cmd.Env = []string{"PATH=/usr/bin:/bin"}
			if manpath != "" {
				cmd.Env = append(cmd.Env, "MANPATH="+manpath)
			}
			got, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%s: %v: %s", shell, err, got)
			}
			if w := want + "|/opt/gobrew/share/info:|/opt/gobrew/bin:/usr/bin:/bin"; strings.TrimSuffix(string(got), "\n") != w {
				t.Fatalf("%s with MANPATH=%q: got %q, want %q", shell, manpath, got, w)
			}
		}
	}
	if _, err := exec.LookPath("/bin/zsh"); err == nil {
		script, _ := env.Script("zsh")
		got, err := exec.Command("/bin/zsh", "-f", "-c", script+"\nprint -r -- $fpath[1]").CombinedOutput()
		if err != nil || strings.TrimSpace(string(got)) != "/opt/gobrew/share/zsh/site-functions" {
			t.Fatalf("zsh fpath[1] = %q, %v", got, err)
		}
	}
	// fish is rarely installed; check brew's fish lines.
	script, err := env.Script("fish")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"if test -n \"$MANPATH\"; set --global --export MANPATH (string replace --regex '^:*(.*?):*$' ':$1' -- \"$MANPATH\"); end", "if not set --query INFOPATH; set INFOPATH ''; end; set --global --export INFOPATH '/opt/gobrew/share/info' $INFOPATH"} {
		if !strings.Contains(script, want) {
			t.Fatalf("fish script lacks %q:\n%s", want, script)
		}
	}
}

func TestPwshScriptFollowsBrew(t *testing.T) {
	script, err := Env{Paths: []string{"/opt/gobrew/bin", "/opt/gobrew/sbin"}, Prefix: "/opt/gobrew"}.Script("pwsh")
	if err != nil {
		t.Fatal(err)
	}
	const target = ",[System.EnvironmentVariableTarget]::Process)"
	want := "[System.Environment]::SetEnvironmentVariable('GOBREW_PREFIX','/opt/gobrew'" + target + "\n" +
		"[System.Environment]::SetEnvironmentVariable('PATH',$('/opt/gobrew/bin:/opt/gobrew/sbin:'+$ENV:PATH)" + target + "\n" +
		"if (${ENV:MANPATH}) { [System.Environment]::SetEnvironmentVariable('MANPATH',(':'+${ENV:MANPATH}.Trim(':'))" + target + "\n}\n" +
		"[System.Environment]::SetEnvironmentVariable('INFOPATH',('/opt/gobrew/share/info:'+${ENV:INFOPATH})" + target + "\n"
	if script != want {
		t.Fatalf("pwsh script:\n%s\nwant:\n%s", script, want)
	}
}
