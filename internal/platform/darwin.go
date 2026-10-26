//go:build darwin

package platform

import (
	"errors"
	"os"
	"runtime"

	"golang.org/x/sys/unix"
)

func systemVersion() (string, error)  { return unix.Sysctl("kern.osproductversion") }
func cloneTree(src, dst string) error { return unix.Clonefile(src, dst, unix.CLONE_NOFOLLOW) }

func fullSync(file *os.File) error {
	for {
		_, err := unix.FcntlInt(file.Fd(), unix.F_FULLFSYNC, 0)
		runtime.KeepAlive(file)
		if !errors.Is(err, unix.EINTR) {
			return err
		}
	}
}
