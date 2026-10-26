package postinstall

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func (r *runner) execute(ctx context.Context, command string, args []string, environment map[string]string) error {
	s := step{Command: &pathSpec{Path: command}, Args: args, Env: environment, PrintStdout: true}
	return r.command(ctx, s)
}

func (r *runner) command(ctx context.Context, s step) error {
	command := r.expand(s.Command.Path)
	if s.Command.Base != "" && s.Command.Base != "relative" {
		var err error
		command, err = r.path(s.Command)
		if err != nil {
			return err
		}
	}
	pathEnv := filepath.Join(r.env.Prefix, "bin") + ":" + filepath.Join(r.env.Prefix, "sbin") + ":/usr/bin:/bin:/usr/sbin:/sbin"
	if !filepath.IsAbs(command) {
		if strings.ContainsRune(command, '/') {
			return errors.New("relative command path unsupported")
		}
		found := ""
		for _, dir := range strings.Split(pathEnv, ":") {
			p := filepath.Join(dir, command)
			if info, err := os.Stat(p); err == nil && !info.IsDir() && info.Mode()&0111 != 0 {
				found = p
				break
			}
		}
		if found == "" {
			return fmt.Errorf("executable %q not found", command)
		}
		command = found
	}
	command = filepath.Clean(command)
	if within(r.env.Prefix, command) {
		rel, err := r.relative(command, true)
		if err != nil {
			return err
		}
		command = filepath.Join(r.env.Prefix, rel)
	} else if !strings.HasPrefix(command, "/usr/bin/") && !strings.HasPrefix(command, "/bin/") && !strings.HasPrefix(command, "/usr/sbin/") && !strings.HasPrefix(command, "/sbin/") {
		return fmt.Errorf("command outside package or system paths: %s", command)
	}
	args := make([]string, len(s.Args))
	for i, arg := range s.Args {
		args[i] = r.expand(arg)
	}
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Env = []string{"PATH=" + pathEnv, "HOME=" + os.Getenv("HOME"), "USER=" + os.Getenv("USER"), "LOGNAME=" + os.Getenv("LOGNAME"), "LANG=" + os.Getenv("LANG"), "TMPDIR=" + r.base("temp", "")}
	for key, value := range s.Env {
		if strings.ContainsAny(key, "=\x00") {
			return errors.New("invalid environment key")
		}
		cmd.Env = append(cmd.Env, key+"="+r.expand(value))
	}
	if err := r.root.MkdirAll(filepath.Join("var", "gobrew", "tmp"), 0700); err != nil {
		return err
	}
	cmd.Dir = r.env.Keg
	if s.Chdir != nil {
		p, err := r.path(s.Chdir)
		if err != nil {
			return err
		}
		rel, err := r.relative(p, true)
		if err != nil {
			return err
		}
		cmd.Dir = filepath.Join(r.env.Prefix, rel)
	}
	if !s.SuppressStderr {
		cmd.Stderr = r.env.Output
	}
	if s.PrintStdout {
		cmd.Stdout = r.env.Output
	}
	if s.StdinPath != nil {
		p, err := r.path(s.StdinPath)
		if err != nil {
			return err
		}
		rel, err := r.relative(p, true)
		if err != nil {
			return err
		}
		input, err := r.root.Open(rel)
		if err != nil {
			return err
		}
		defer input.Close()
		cmd.Stdin = input
	}
	var output *os.File
	var temp, destination string
	if s.StdoutPath != nil {
		p, err := r.path(s.StdoutPath)
		if err != nil {
			return err
		}
		destination, err = r.relative(p, false)
		if err != nil {
			return err
		}
		if err := r.root.MkdirAll(filepath.Dir(destination), 0755); err != nil {
			return err
		}
		temp = filepath.Join(filepath.Dir(destination), ".gobrew-"+rand.Text())
		output, err = r.root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
		if err != nil {
			return err
		}
		defer r.root.Remove(temp)
		defer output.Close()
		cmd.Stdout = output
	}
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if s.AllowFailure {
			return nil
		}
		return fmt.Errorf("%s: %w", command, err)
	}
	if output != nil {
		if err := output.Close(); err != nil {
			return err
		}
		return r.root.Rename(temp, destination)
	}
	return nil
}
