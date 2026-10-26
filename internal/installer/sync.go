package installer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/shqingda/gobrew/internal/platform"
	"golang.org/x/sys/unix"
)

// syncPostInstall covers the current keg and shared prefix files before their
// completion flag is committed. Other kegs and private state are deliberately
// excluded. Arbitrary command side effects outside these roots are not part of
// the package transaction's durability guarantee.
func (i Installer) syncPostInstall(ctx context.Context, keg string) error {
	if _, err := i.kegPath(keg); err != nil {
		return err
	}
	if err := platform.SyncTree(ctx, keg); err != nil {
		return err
	}
	entries, err := os.ReadDir(i.Prefix)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Name() == "Cellar" || entry.Name() == ".gobrew" {
			continue
		}
		path := filepath.Join(i.Prefix, entry.Name())
		switch {
		case entry.IsDir():
			if err := platform.SyncSharedTree(ctx, path); err != nil {
				return err
			}
		case entry.Type().IsRegular():
			file, err := os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
			if err != nil {
				return err
			}
			info, err := file.Stat()
			if err == nil && !info.Mode().IsRegular() {
				err = fmt.Errorf("post-install file changed before sync: %s", path)
			}
			if err == nil {
				err = platform.SyncFile(file)
			}
			closeErr := file.Close()
			if err != nil {
				return err
			}
			if closeErr != nil {
				return closeErr
			}
		default:
			// Persist links through their parent without following targets. Shared
			// IPC endpoints, such as sockets and FIFOs, have no durable file data.
		}
	}
	return platform.SyncDir(i.Prefix)
}
