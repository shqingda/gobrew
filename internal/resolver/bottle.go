package resolver

import (
	"encoding/hex"
	"fmt"
	"net/url"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/shqingda/gobrew/internal/model"
)

var macOSVersions = map[string]int{"big_sur": 11, "monterey": 12, "ventura": 13, "sonoma": 14, "sequoia": 15, "tahoe": 26, "golden_gate": 27}

// HostTag is the bottle tag of an Apple Silicon host, such as
// arm64_sequoia, or "" for a macOS version this build does not name.
func HostTag(host model.Platform) string {
	for name, major := range macOSVersions {
		if major == host.Major {
			return "arm64_" + name
		}
	}
	return ""
}

// NormalizeTag spells a --bottle-tag the way brew's Utils::Bottles::Tag does:
// an Intel macOS tag has no architecture, any other tag without one is Intel.
func NormalizeTag(tag string) string {
	if rest, ok := strings.CutPrefix(tag, "x86_64_"); ok && macOSVersions[rest] > 0 {
		return rest
	}
	if strings.HasPrefix(tag, "arm64_") || strings.HasPrefix(tag, "x86_64_") || macOSVersions[tag] > 0 {
		return tag
	}
	return "x86_64_" + tag
}

// TagTarget is the architecture and macOS major version of a bottle tag; the
// version is 0 for a tag that is not macOS, such as x86_64_linux.
func TagTarget(tag string) (arch string, major int) {
	arch, name := "x86_64", tag
	if rest, ok := strings.CutPrefix(tag, "arm64_"); ok {
		arch, name = "arm64", rest
	} else if rest, ok := strings.CutPrefix(tag, "x86_64_"); ok {
		name = rest
	}
	return arch, macOSVersions[name]
}

// BottleFor follows brew's bottle_for_tag: the tag itself or a bottle for
// that macOS version, then a platform-independent bottle, then the newest
// older macOS bottle for the architecture. Newer macOS versions are never
// guessed. Its tag field names the bottle chosen.
func BottleFor(f model.Formula, tag, arch string, major int) (model.Bottle, bool) {
	best, rank := "", -1
	for candidate := range f.Bottles {
		r := -1
		a, m := TagTarget(candidate)
		switch {
		case candidate == tag, major > 0 && a == arch && m == major:
			r = 1000
		case candidate == "all":
			r = 100
		case major > 0 && a == arch && m > 0 && m < major:
			r = m
		}
		if r > rank {
			best, rank = candidate, r
		}
	}
	if rank < 0 {
		return model.Bottle{}, false
	}
	b := f.Bottles[best]
	b.Tag = best
	return b, true
}

// selectBottle prefers the host tag, then a platform-independent bottle, then
// the newest older compatible macOS tag. Future or foreign-architecture tags
// are never guessed. Prefix length is measured in bytes, as Mach-O requires.
// allowDisabled is --force: a disabled formula's bottle is still signed.
func selectBottle(f model.Formula, host model.Platform, prefix string, allowDisabled bool) (model.Bottle, error) {
	if host.OS != "darwin" || host.Arch != "arm64" || host.Major < 15 {
		return model.Bottle{}, fmt.Errorf("%w: installation requires Apple Silicon macOS 15+", model.ErrUnsupported)
	}
	if !filepath.IsAbs(prefix) || filepath.Clean(prefix) != prefix || prefix == "/" {
		return model.Bottle{}, fmt.Errorf("%w: invalid prefix %q", model.ErrUnsupported, prefix)
	}
	if f.UnsupportedReason != "" {
		return model.Bottle{}, fmt.Errorf("%w: %s: %s", model.ErrUnsupported, f.Name, f.UnsupportedReason)
	}
	if f.Disabled && !allowDisabled {
		return model.Bottle{}, fmt.Errorf("%w: %s has been %s", model.ErrUnsupported, f.Name, f.DeprecationMessage(time.Now()))
	}
	if f.PostInstallDefined {
		return model.Bottle{}, fmt.Errorf("%w: %s uses a Ruby post-install hook", model.ErrUnsupported, f.Name)
	}
	tag := HostTag(host)
	b, ok := BottleFor(f, tag, "arm64", host.Major)
	if !ok {
		return model.Bottle{}, fmt.Errorf("%w: no compatible bottle for %s on macOS %d arm64", model.ErrUnsupported, f.Name, host.Major)
	}
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
