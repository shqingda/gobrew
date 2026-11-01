// Package installer owns the mutable prefix. All filesystem commits are
// journaled; post-install commands have an explicit, non-rollbackable boundary.
package installer

import (
	"crypto/rand"
	"encoding/hex"
	"strings"

	"github.com/shqingda/gobrew/internal/model"
)

const ownerFile = model.OwnerMarker

type Installer struct {
	Prefix string
	// The filesystem barrier is replaceable in package-local fault tests.
	syncDirectories func([]string) error
}

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // Never fails: since Go 1.24 the runtime aborts instead.
	return hex.EncodeToString(b[:])
}
func cloneSnapshot(s model.Snapshot) model.Snapshot {
	r := model.Snapshot{Generation: s.Generation, Records: make(map[string]model.Record, len(s.Records))}
	for k, v := range s.Records {
		r.Records[k] = v
	}
	return r
}

func kegInUse(s model.Snapshot, keg string) bool {
	for _, r := range s.Records {
		if r.Keg == keg {
			return true
		}
	}
	return false
}
func owns(path, owner string) bool {
	parts := strings.SplitN(owner, ":", 2)
	if len(parts) != 2 || !validOwner(owner, parts[1]) {
		return false
	}
	actual, err := readOwner(path)
	return err == nil && actual == owner
}
