//go:build ignore

// check-tutorial validates the complete Go examples in the Markdown tutorial.
// Run it from the repository root with go run ./scripts/check-tutorial.go.
package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"go/format"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

type example struct {
	name string
	code []byte
	test bool
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	path := "docs/go-tutorial.md"
	if len(os.Args) > 2 {
		return fmt.Errorf("usage: go run ./scripts/check-tutorial.go [markdown-file]")
	}
	if len(os.Args) == 2 {
		path = os.Args[1]
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	examples, err := readExamples(data)
	if err != nil {
		return err
	}
	root, err := os.MkdirTemp("", "go-tutorial-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module tutorial.local/examples\n\ngo 1.27.3\n"), 0o600); err != nil {
		return err
	}
	for _, example := range examples {
		dir := filepath.Join(root, example.name)
		if err := os.Mkdir(dir, 0o700); err != nil {
			return err
		}
		name := "main.go"
		if example.test {
			name = "example_test.go"
		}
		if err := os.WriteFile(filepath.Join(dir, name), example.code, 0o600); err != nil {
			return err
		}
	}
	fmt.Printf("Validating %d examples with %s\n", len(examples), runtime.Version())
	if err := command(root, "vet", "./..."); err != nil {
		return err
	}
	if err := command(root, "test", "-race", "./..."); err != nil {
		return err
	}
	for _, example := range examples {
		if example.test {
			continue
		}
		fmt.Printf("\n[%s]\n", example.name)
		if err := command(root, "run", "-race", "./"+example.name); err != nil {
			return err
		}
	}
	fmt.Printf("\nAll %d examples passed formatting, compilation, vet, and race checks.\n", len(examples))
	return nil
}

func readExamples(data []byte) ([]example, error) {
	validName := regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	var examples []example
	var lines []string
	inGo := false
	seen := make(map[string]bool)
	for scanner.Scan() {
		line := scanner.Text()
		if !inGo {
			if line == "```go" {
				inGo = true
				lines = nil
			}
			continue
		}
		if line != "```" {
			lines = append(lines, line)
			continue
		}
		inGo = false
		if len(lines) == 0 || !strings.HasPrefix(lines[0], "// tutorial: ") {
			return nil, fmt.Errorf("every Go block must begin with // tutorial: <name> [test]")
		}
		fields := strings.Fields(strings.TrimPrefix(lines[0], "// tutorial: "))
		if len(fields) < 1 || len(fields) > 2 || !validName.MatchString(fields[0]) {
			return nil, fmt.Errorf("invalid tutorial marker: %s", lines[0])
		}
		isTest := len(fields) == 2
		if isTest && fields[1] != "test" {
			return nil, fmt.Errorf("unknown example mode: %s", lines[0])
		}
		if seen[fields[0]] {
			return nil, fmt.Errorf("duplicate example: %s", fields[0])
		}
		seen[fields[0]] = true
		code := []byte(strings.Join(lines, "\n") + "\n")
		formatted, err := format.Source(code)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", fields[0], err)
		}
		if !bytes.Equal(code, formatted) {
			return nil, fmt.Errorf("%s: code block must be gofmt-formatted", fields[0])
		}
		examples = append(examples, example{name: fields[0], code: code, test: isTest})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if inGo {
		return nil, fmt.Errorf("unclosed Go code block")
	}
	if len(examples) == 0 {
		return nil, fmt.Errorf("no tutorial examples found")
	}
	return examples, nil
}

func command(dir string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOTOOLCHAIN=local", "GOPROXY=off", "GOSUMDB=off", "CGO_ENABLED=1")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("go %s: %w", strings.Join(args, " "), err)
	}
	return nil
}
