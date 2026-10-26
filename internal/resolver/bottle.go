package resolver

import (
	"encoding/hex"
	"fmt"
	"net/url"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/shqingda/gobrew/internal/model"
)

var macOSVersions = map[string]int{"big_sur": 11, "monterey": 12, "ventura": 13, "sonoma": 14, "sequoia": 15, "tahoe": 26, "golden_gate": 27}

// SelectBottle prefers the host tag, then a platform-independent bottle, then
// the newest older compatible macOS tag. Future or foreign-architecture tags
// are never guessed. Prefix length is measured in bytes, as Mach-O requires.
func SelectBottle(f model.Formula, host model.Platform, prefix string) (model.Bottle, error) {
	if host.OS != "darwin" || host.Arch != "arm64" || host.Major < 15 {
		return model.Bottle{}, fmt.Errorf("%w: installation requires Apple Silicon macOS 15+", model.ErrUnsupported)
	}
	if !filepath.IsAbs(prefix) || filepath.Clean(prefix) != prefix || prefix == "/" {
		return model.Bottle{}, fmt.Errorf("%w: invalid prefix %q", model.ErrUnsupported, prefix)
	}
	if f.UnsupportedReason != "" {
		return model.Bottle{}, fmt.Errorf("%w: %s: %s", model.ErrUnsupported, f.Name, f.UnsupportedReason)
	}
	if f.Disabled {
		return model.Bottle{}, fmt.Errorf("%w: %s is disabled by Homebrew", model.ErrUnsupported, f.Name)
	}
	if f.PostInstallDefined {
		return model.Bottle{}, fmt.Errorf("%w: %s uses a Ruby post-install hook", model.ErrUnsupported, f.Name)
	}
	type candidate struct {
		tag  string
		rank int
	}
	var candidates []candidate
	for tag := range f.Bottles {
		if tag == "all" {
			candidates = append(candidates, candidate{tag, 100})
			continue
		}
		if !strings.HasPrefix(tag, "arm64_") {
			continue
		}
		major, ok := macOSVersions[strings.TrimPrefix(tag, "arm64_")]
		if !ok || major > host.Major {
			continue
		}
		rank := major
		if major == host.Major {
			rank = 1000
		}
		candidates = append(candidates, candidate{tag, rank})
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].rank > candidates[j].rank })
	if len(candidates) == 0 {
		return model.Bottle{}, fmt.Errorf("%w: no compatible bottle for %s on macOS %d arm64", model.ErrUnsupported, f.Name, host.Major)
	}
	b := f.Bottles[candidates[0].tag]
	b.Tag = candidates[0].tag
	hash, err := hex.DecodeString(b.SHA256)
	if err != nil || len(hash) != 32 {
		return model.Bottle{}, fmt.Errorf("%w: invalid bottle checksum for %s", model.ErrUnsupported, f.Name)
	}
	u, err := url.Parse(b.URL)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Host != "ghcr.io" || !strings.HasPrefix(u.Path, "/v2/homebrew/core/") || !strings.HasSuffix(u.Path, "/blobs/sha256:"+b.SHA256) || u.RawQuery != "" || u.Fragment != "" {
		return model.Bottle{}, fmt.Errorf("%w: non-official bottle URL for %s", model.ErrUnsupported, f.Name)
	}
	switch b.Cellar {
	case ":any", "any", ":any_skip_relocation", "any_skip_relocation":
	case "/opt/homebrew/Cellar":
		if len(prefix) > len(path.Dir(b.Cellar)) || len(filepath.Join(prefix, "Cellar")) > len(b.Cellar) {
			return model.Bottle{}, fmt.Errorf("%w: %s requires a prefix no longer than %d bytes", model.ErrUnsupported, f.Name, len(path.Dir(b.Cellar)))
		}
	default:
		return model.Bottle{}, fmt.Errorf("%w: unsupported bottle Cellar %q for %s", model.ErrUnsupported, b.Cellar, f.Name)
	}
	return b, nil
}
