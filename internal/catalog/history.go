package catalog

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/shqingda/gobrew/internal/model"
)

// History resolves one explicitly requested older version of a core formula.
// Such versions are outside Homebrew's signed API snapshot, so a bottle is only
// accepted when two official GitHub sources agree on its digest: the GHCR OCI
// index for that version and the "update bottle" commit of the formula file in
// homebrew-core history. The file must also define no Ruby post-install hook.
type History struct {
	HTTP     *http.Client
	Platform model.Platform
	Token    string // optional GitHub API token for higher rate limits
	// Base URLs default to the official hosts; tests point them elsewhere.
	GHCR, API, Raw string
}

const (
	maxHistoryBody = 4 << 20
	// The bottle commit lands after the bottles are built and uploaded.
	bottleCommitWindow = 7 * 24 * time.Hour
)

var (
	historyVersion = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+~-]*(_[0-9]+)?$`)
	bottleLine     = regexp.MustCompile(`(?m)^\s*sha256\s+(?:cellar:\s*(:[a-z_]+|"[^"]+"),\s*)?([a-z0-9_]+):\s*"([0-9a-f]{64})"`)
	rubyInt        = func(key string) *regexp.Regexp { return regexp.MustCompile(`(?m)^\s*` + key + `\s+([0-9]+)\s*$`) }
	revisionLine   = rubyInt("revision")
	rebuildLine    = rubyInt("rebuild")
	schemeLine     = rubyInt("version_scheme")
	compatLine     = rubyInt("compatibility_version")
	versionLine    = regexp.MustCompile(`(?m)^\s*version\s+"([^"]+)"`)
	urlLine        = regexp.MustCompile(`(?m)^\s*url\s+"([^"]+)"`)
	postInstall    = regexp.MustCompile(`(?m)^\s*def\s+post_install\b`)
	pourOnlyIf     = regexp.MustCompile(`(?m)^\s*pour_bottle\?`)
	kegOnly        = regexp.MustCompile(`(?m)^\s*keg_only\b`)
)

// ValidVersion reports whether s can name a version in "<formula>@<version>".
func ValidVersion(s string) bool { return historyVersion.MatchString(s) && s[0] >= '0' && s[0] <= '9' }

// Formula returns current's recipe at version, with exactly one dual-verified
// bottle for this host. Dependencies come from that bottle's build-time tab;
// Prepare verifies the same tab again through the content-addressed manifest.
func (h History) Formula(ctx context.Context, current model.Formula, version string) (model.Formula, error) {
	if !ValidVersion(version) {
		return model.Formula{}, fmt.Errorf("%w: invalid version %q", model.ErrUnsupported, version)
	}
	repository := "homebrew/core/" + strings.NewReplacer("@", "/", "+", "x").Replace(current.Name)
	token, err := h.ghcrToken(ctx, repository)
	if err != nil {
		return model.Formula{}, err
	}
	var tags struct {
		Tags []string `json:"tags"`
	}
	if err := h.getJSON(ctx, h.ghcr()+"/v2/"+repository+"/tags/list?n=100000", "Bearer "+token, "", &tags); err != nil {
		return model.Formula{}, fmt.Errorf("list %s versions: %w", current.Name, err)
	}
	tag, pkgVersion, revision, rebuild, ok := selectTag(tags.Tags, version)
	if !ok {
		return model.Formula{}, fmt.Errorf("%w: no Homebrew bottle for %s %s", model.ErrNotFound, current.Name, version)
	}
	var index struct {
		SchemaVersion int `json:"schemaVersion"`
		Manifests     []struct {
			Digest      string            `json:"digest"`
			Annotations map[string]string `json:"annotations"`
		} `json:"manifests"`
		Annotations map[string]string `json:"annotations"`
	}
	if err := h.getJSON(ctx, h.ghcr()+"/v2/"+repository+"/manifests/"+tag, "Bearer "+token, "application/vnd.oci.image.index.v1+json", &index); err != nil {
		return model.Formula{}, fmt.Errorf("fetch %s %s bottle index: %w", current.Name, tag, err)
	}
	if index.SchemaVersion != 2 {
		return model.Formula{}, errors.New("unsupported OCI index schema")
	}
	created, err := time.Parse(time.RFC3339, index.Annotations["org.opencontainers.image.created"])
	if err != nil {
		return model.Formula{}, errors.New("OCI index has no creation time")
	}
	path, err := formulaPath(index.Annotations["org.opencontainers.image.source"], current.Name)
	if err != nil {
		return model.Formula{}, err
	}
	// GHCR side: one digest and tab per macOS arm64 platform.
	ghcr := map[string]bottlePlatform{}
	suffix := ""
	if rebuild > 0 {
		suffix = "." + strconv.Itoa(rebuild)
	}
	for _, m := range index.Manifests {
		ref, ok := strings.CutPrefix(m.Annotations["org.opencontainers.image.ref.name"], pkgVersion+".")
		if !ok {
			continue
		}
		if ref, ok = strings.CutSuffix(ref, suffix); !ok || strings.Contains(ref, ".") || !strings.HasPrefix(ref, "arm64_") {
			continue
		}
		digest := m.Annotations["sh.brew.bottle.digest"]
		if _, err := hex.DecodeString(digest); err != nil || len(digest) != 64 {
			return model.Formula{}, fmt.Errorf("invalid OCI bottle digest for %s", ref)
		}
		if _, dup := ghcr[ref]; dup {
			return model.Formula{}, fmt.Errorf("ambiguous OCI bottle metadata for %s", ref)
		}
		ghcr[ref] = bottlePlatform{digest, m.Annotations["sh.brew.tab"]}
	}
	best, err := h.bestTag(ghcr)
	if err != nil {
		return model.Formula{}, fmt.Errorf("%w: %s %s: %v", model.ErrUnsupported, current.Name, version, err)
	}
	recipe, cellar, err := h.bottleCommit(ctx, path, created, best, ghcr[best].digest)
	if err != nil {
		return model.Formula{}, fmt.Errorf("verify %s %s against homebrew-core: %w", current.Name, version, err)
	}
	if postInstall.MatchString(recipe) || pourOnlyIf.MatchString(recipe) {
		return model.Formula{}, fmt.Errorf("%w: %s %s defines a Ruby post-install hook or pour condition", model.ErrUnsupported, current.Name, version)
	}
	plain, _, _ := strings.Cut(pkgVersion, "_")
	if got := intValue(revisionLine, recipe); got != revision {
		return model.Formula{}, fmt.Errorf("homebrew-core revision %d does not match bottle %s", got, pkgVersion)
	}
	if got := intValue(rebuildLine, recipe); got != rebuild {
		return model.Formula{}, fmt.Errorf("homebrew-core bottle rebuild %d does not match %s", got, tag)
	}
	if m := versionLine.FindStringSubmatch(recipe); m != nil {
		if m[1] != plain {
			return model.Formula{}, fmt.Errorf("homebrew-core declares version %s, not %s", m[1], plain)
		}
	} else if m := urlLine.FindStringSubmatch(recipe); m == nil || !strings.Contains(m[1], plain) {
		return model.Formula{}, fmt.Errorf("homebrew-core source URL does not name version %s", plain)
	}
	deps, err := tabDependencies(ghcr[best].tab)
	if err != nil {
		return model.Formula{}, err
	}
	f := current
	f.Version, f.Revision, f.BottleRebuild = plain, revision, rebuild
	f.VersionScheme = intValue(schemeLine, recipe)
	f.CompatibilityVersion = nil
	if m := compatLine.FindStringSubmatch(recipe); m != nil {
		n, _ := strconv.Atoi(m[1])
		f.CompatibilityVersion = &n
	}
	f.Bottles = map[string]model.Bottle{best: {
		URL:    h.ghcr() + "/v2/" + repository + "/blobs/sha256:" + ghcr[best].digest,
		SHA256: ghcr[best].digest,
		Cellar: cellar,
	}}
	f.Dependencies, f.NoLinkageDependencies, f.RuntimeDependencies = deps, nil, nil
	f.KegOnly = kegOnly.MatchString(recipe)
	f.Disabled, f.Deprecated, f.PostInstallDefined, f.Steps, f.UnsupportedReason = false, false, false, nil, ""
	return f, nil
}

// selectTag accepts "1.2" (newest revision and rebuild) or an exact "1.2_1".
func selectTag(tags []string, version string) (tag, pkgVersion string, revision, rebuild int, ok bool) {
	for _, t := range tags {
		base, rb, hasRebuild := strings.Cut(t, "-")
		r := 0
		if hasRebuild {
			var err error
			if r, err = strconv.Atoi(rb); err != nil || r < 1 {
				continue
			}
		}
		plain, rev, hasRevision := strings.Cut(base, "_")
		v := 0
		if hasRevision {
			var err error
			if v, err = strconv.Atoi(rev); err != nil || v < 1 {
				continue
			}
		}
		if base != version && (strings.Contains(version, "_") || plain != version) {
			continue
		}
		if !ok || v > revision || v == revision && r > rebuild {
			tag, pkgVersion, revision, rebuild, ok = t, base, v, r, true
		}
	}
	return
}

func formulaPath(source, name string) (string, error) {
	const prefix = "https://github.com/homebrew/homebrew-core/blob/"
	if len(source) <= len(prefix) || !strings.EqualFold(source[:len(prefix)], prefix) {
		return "", errors.New("OCI index does not reference homebrew-core")
	}
	_, path, _ := strings.Cut(source[len(prefix):], "/")
	if path != "Formula/"+name+".rb" && path != "Formula/"+name[:1]+"/"+name+".rb" {
		return "", fmt.Errorf("unexpected homebrew-core formula path %q", path)
	}
	return path, nil
}

// bestTag mirrors the resolver's preference: the host macOS, else the newest
// older one. Future macOS bottles are never chosen.
type bottlePlatform struct{ digest, tab string }

func (h History) bestTag(platforms map[string]bottlePlatform) (string, error) {
	if h.Platform.OS != "darwin" || h.Platform.Arch != "arm64" {
		return "", errors.New("explicit versions require Apple Silicon macOS")
	}
	best, rank := "", 0
	for tag := range platforms {
		major, ok := macOSNames[strings.TrimPrefix(tag, "arm64_")]
		if !ok || major > h.Platform.Major {
			continue
		}
		if major == h.Platform.Major {
			major = 1000
		}
		if major > rank {
			best, rank = tag, major
		}
	}
	if best == "" {
		return "", fmt.Errorf("no bottle for macOS %d arm64", h.Platform.Major)
	}
	return best, nil
}

// bottleCommit finds the homebrew-core commit whose bottle block records this
// digest for tag, searching the formula file's history after the bottle build.
func (h History) bottleCommit(ctx context.Context, path string, created time.Time, tag, digest string) (string, string, error) {
	query := url.Values{"path": {path}, "since": {created.UTC().Format(time.RFC3339)}, "until": {created.Add(bottleCommitWindow).UTC().Format(time.RFC3339)}, "per_page": {"30"}}
	var commits []struct {
		SHA string `json:"sha"`
	}
	auth := ""
	if h.Token != "" {
		auth = "Bearer " + h.Token
	}
	if err := h.getJSON(ctx, cmp.Or(h.API, "https://api.github.com")+"/repos/Homebrew/homebrew-core/commits?"+query.Encode(), auth, "application/vnd.github+json", &commits); err != nil {
		return "", "", fmt.Errorf("%w (set HOMEBREW_GITHUB_API_TOKEN if GitHub rate limits anonymous requests)", err)
	}
	slices.Reverse(commits) // oldest first: the first bottle commit for this build
	for _, c := range commits[:min(len(commits), 10)] {
		if _, err := hex.DecodeString(c.SHA); err != nil || len(c.SHA) != 40 {
			return "", "", errors.New("invalid commit identifier from GitHub")
		}
		body, err := h.get(ctx, cmp.Or(h.Raw, "https://raw.githubusercontent.com")+"/Homebrew/homebrew-core/"+c.SHA+"/"+path, "", "")
		if err != nil {
			return "", "", err
		}
		recipe := string(body)
		start := strings.Index(recipe, "\n  bottle do\n")
		if start < 0 {
			continue
		}
		block, _, _ := strings.Cut(recipe[start:], "\n  end\n")
		for _, m := range bottleLine.FindAllStringSubmatch(block, -1) {
			if m[2] != tag {
				continue
			}
			if m[3] != digest {
				break
			}
			cellar := strings.Trim(m[1], `"`)
			if cellar == "" {
				cellar = "/opt/homebrew/Cellar"
			}
			return recipe, cellar, nil
		}
	}
	return "", "", fmt.Errorf("no homebrew-core commit records bottle %s for %s; the two sources disagree", digest, tag)
}

func tabDependencies(tab string) ([]string, error) {
	var parsed struct {
		Runtime *[]model.RuntimeDependency `json:"runtime_dependencies"`
	}
	if err := json.Unmarshal([]byte(tab), &parsed); err != nil || parsed.Runtime == nil {
		return nil, errors.New("bottle metadata does not declare runtime dependencies")
	}
	var direct, all []string
	for _, d := range *parsed.Runtime {
		name, ok := strings.CutPrefix(d.Name, "homebrew/core/")
		if !ok && strings.Contains(d.Name, "/") || !formulaName.MatchString(name) {
			return nil, fmt.Errorf("%w: runtime dependency %q is not an official core formula", model.ErrUnsupported, d.Name)
		}
		all = append(all, name)
		if d.DeclaredDirectly {
			direct = append(direct, name)
		}
	}
	if len(direct) == 0 {
		return all, nil
	}
	return direct, nil
}

func intValue(re *regexp.Regexp, recipe string) int {
	if m := re.FindStringSubmatch(recipe); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 0
}

func (h History) ghcr() string { return cmp.Or(h.GHCR, "https://ghcr.io") }

func (h History) ghcrToken(ctx context.Context, repository string) (string, error) {
	var token struct {
		Token string `json:"token"`
	}
	u := h.ghcr() + "/token?" + url.Values{"service": {"ghcr.io"}, "scope": {"repository:" + repository + ":pull"}}.Encode()
	if err := h.getJSON(ctx, u, "", "", &token); err != nil || token.Token == "" {
		return "", fmt.Errorf("GHCR token: %w", cmp.Or(err, errors.New("empty token")))
	}
	return token.Token, nil
}

func (h History) getJSON(ctx context.Context, u, auth, accept string, v any) error {
	body, err := h.get(ctx, u, auth, accept)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, v)
}

func (h History) get(ctx context.Context, u, auth, accept string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "gobrew")
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	client := h.HTTP
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s returned HTTP %s", req.URL.Host, res.Status)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, maxHistoryBody+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxHistoryBody {
		return nil, errors.New("response exceeds size limit")
	}
	if digest := res.Header.Get("Docker-Content-Digest"); digest != "" {
		sum := sha256.Sum256(body)
		if digest != "sha256:"+hex.EncodeToString(sum[:]) {
			return nil, errors.New("OCI response digest mismatch")
		}
	}
	return body, nil
}
