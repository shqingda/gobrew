//go:build !darwin

package platform

import (
	"errors"
	"os"
)

func systemVersion() (string, error)  { return "", errors.New("macOS version unavailable") }
func cloneTree(src, dst string) error { return errors.New("clonefile unavailable") }
func fullSync(file *os.File) error    { return file.Sync() }
