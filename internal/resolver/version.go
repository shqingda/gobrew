package resolver

import (
	"cmp"
	"regexp"
	"strings"

	"github.com/shqingda/gobrew/internal/model"
)

// Compare orders formula releases by Homebrew's version epoch, version tokens,
// and recipe revision. A bottle rebuild is not a new package version.
func Compare(a, b model.Formula) int {
	if c := cmp.Compare(a.VersionScheme, b.VersionScheme); c != 0 {
		return c
	}
	if c := CompareVersions(a.Version, b.Version); c != 0 {
		return c
	}
	return cmp.Compare(a.Revision, b.Revision)
}

// Token precedence follows Homebrew Version (BSD-2-Clause):
// https://github.com/Homebrew/brew/blob/main/Library/Homebrew/version.rb
// Parsing does not assume SemVer: dates, alphabetic suffixes and epochs occur
// throughout core. Numeric comparison has no machine-integer size limit.
var tokenPattern = regexp.MustCompile(`(?i)alpha[0-9]*|a[0-9]+|beta[0-9]*|b[0-9]+|pre[0-9]*|rc[0-9]*|\.post[0-9]+|p[0-9]*|[0-9]+|[a-z]+`)
var digits = regexp.MustCompile(`[0-9]+$`)

type versionToken struct {
	value    string
	kind     int
	revision string
}

const (
	nullToken = iota
	numberToken
	stringToken
	alphaToken
	betaToken
	preToken
	rcToken
	patchToken
	postToken
)

func tokens(version string) []versionToken {
	parts := tokenPattern.FindAllString(strings.ToLower(version), -1)
	out := make([]versionToken, 0, len(parts))
	for _, part := range parts {
		t := versionToken{value: part, kind: stringToken, revision: digits.FindString(part)}
		switch {
		case part[0] >= '0' && part[0] <= '9':
			t.kind = numberToken
		case strings.HasPrefix(part, "alpha") || (strings.HasPrefix(part, "a") && len(part) > 1 && part[1] >= '0' && part[1] <= '9'):
			t.kind = alphaToken
		case strings.HasPrefix(part, "beta") || (strings.HasPrefix(part, "b") && len(part) > 1 && part[1] >= '0' && part[1] <= '9'):
			t.kind = betaToken
		case strings.HasPrefix(part, "pre"):
			t.kind = preToken
		case strings.HasPrefix(part, "rc"):
			t.kind = rcToken
		case strings.HasPrefix(part, ".post"):
			t.kind = postToken
		case part == "p" || (part[0] == 'p' && len(part) > 1 && part[1] >= '0' && part[1] <= '9'):
			t.kind = patchToken
		}
		out = append(out, t)
	}
	return out
}

func CompareVersions(a, b string) int {
	left, right := tokens(a), tokens(b)
	for i := 0; i < max(len(left), len(right)); i++ {
		var x, y versionToken
		if i < len(left) {
			x = left[i]
		}
		if i < len(right) {
			y = right[i]
		}
		if c := compareToken(x, y); c != 0 {
			return c
		}
	}
	return 0
}

func compareToken(a, b versionToken) int {
	if a.kind == nullToken {
		switch b.kind {
		case nullToken:
			return 0
		case numberToken:
			return -compareNumber(b.value, "0")
		case alphaToken, betaToken, preToken, rcToken:
			return 1
		default:
			return -1
		}
	}
	if b.kind == nullToken {
		return -compareToken(b, a)
	}
	if a.kind == numberToken {
		if b.kind == numberToken {
			return compareNumber(a.value, b.value)
		}
		return 1
	}
	if b.kind == numberToken {
		return -1
	}
	if a.kind >= alphaToken && b.kind >= alphaToken {
		if a.kind == b.kind {
			return compareNumber(a.revision, b.revision)
		}
		if a.kind <= rcToken || b.kind <= rcToken {
			return cmp.Compare(a.kind, b.kind)
		}
	}
	return strings.Compare(a.value, b.value)
}

func compareNumber(a, b string) int {
	a = strings.TrimLeft(a, "0")
	b = strings.TrimLeft(b, "0")
	if c := cmp.Compare(len(a), len(b)); c != 0 {
		return c
	}
	return strings.Compare(a, b)
}
