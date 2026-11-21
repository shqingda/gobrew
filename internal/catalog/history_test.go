package catalog

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/shqingda/gobrew/internal/model"
)

const (
	tahoeDigest = "a9d5091cc5d0bb8e19f5fbc37db7167b09fcc76cee60c7af3989b73fb719fd4b"
	bumpCommit  = "300a39f1049db21b2e2a7bc92b76324fcebad740"
	bottleSHA   = "a61c6ced2316f42a4c80d68d367e851b40a84a56"
)

// fakeHistory serves the GHCR, GitHub API and raw file contracts observed for
// uv 0.13.5: the index names the version-bump commit, whose bottle block is
// stale; the later "update bottle" commit records the published digests.
func fakeHistory(t *testing.T, recipe string) History {
	t.Helper()
	tab := `{"runtime_dependencies":[{"full_name":"homebrew/core/xz","version":"5.8.4","declared_directly":true},{"full_name":"zlib","version":"1.3"}]}`
	index := map[string]any{
		"schemaVersion": 2,
		"annotations": map[string]string{
			"org.opencontainers.image.created": "2026-10-22T03:25:16Z",
			"org.opencontainers.image.source":  "https://github.com/homebrew/homebrew-core/blob/" + bumpCommit + "/Formula/u/uv.rb",
		},
		"manifests": []map[string]any{
			{"digest": "sha256:" + strings.Repeat("1", 64), "annotations": map[string]string{"org.opencontainers.image.ref.name": "0.13.5.arm64_tahoe", "sh.brew.bottle.digest": tahoeDigest, "sh.brew.tab": tab}},
			{"digest": "sha256:" + strings.Repeat("2", 64), "annotations": map[string]string{"org.opencontainers.image.ref.name": "0.13.5.arm64_golden_gate", "sh.brew.bottle.digest": strings.Repeat("3", 64), "sh.brew.tab": tab}},
		},
	}
	stale := strings.Replace(recipe, tahoeDigest, strings.Repeat("9", 64), 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			_, _ = w.Write([]byte(`{"token":"t"}`))
		case r.URL.Path == "/v2/homebrew/core/uv/tags/list":
			_, _ = w.Write([]byte(`{"tags":["0.13.4","0.13.5","0.13.6"]}`))
		case r.URL.Path == "/v2/homebrew/core/uv/manifests/0.13.5":
			_ = json.NewEncoder(w).Encode(index)
		case r.URL.Path == "/repos/Homebrew/homebrew-core/commits":
			if r.URL.Query().Get("path") != "Formula/u/uv.rb" || r.URL.Query().Get("since") != "2026-10-22T03:25:16Z" {
				t.Errorf("unexpected commit query %s", r.URL.RawQuery)
			}
			// Newest first, like the GitHub API.
			_, _ = w.Write([]byte(`[{"sha":"` + strings.Repeat("c", 40) + `","commit":{"message":"uv: update 0.13.5 bottle."}},` +
				`{"sha":"` + bottleSHA + `","commit":{"message":"uv: update 0.13.5 bottle.\n\nSigned-off-by: BrewTestBot"}},` +
				`{"sha":"` + strings.Repeat("b", 40) + `","commit":{"message":"uv 0.13.5"}}]`))
		case r.URL.Path == "/Homebrew/homebrew-core/"+strings.Repeat("b", 40)+"/Formula/u/uv.rb",
			r.URL.Path == "/Homebrew/homebrew-core/"+strings.Repeat("c", 40)+"/Formula/u/uv.rb":
			_, _ = w.Write([]byte(stale))
		case r.URL.Path == "/Homebrew/homebrew-core/"+bottleSHA+"/Formula/u/uv.rb":
			_, _ = w.Write([]byte(recipe))
		default:
			t.Errorf("unexpected request %s", r.URL)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return History{HTTP: server.Client(), Platform: model.Platform{OS: "darwin", Arch: "arm64", Major: 26}, GHCR: server.URL, API: server.URL, Raw: server.URL}
}

const uvRecipe = `class Uv < Formula
  desc "Extremely fast Python package installer"
  url "https://github.com/astral-sh/uv/archive/refs/tags/0.13.5.tar.gz"
  compatibility_version 1

  bottle do
    sha256 cellar: :any_skip_relocation, arm64_golden_gate: "` + "4444444444444444444444444444444444444444444444444444444444444444" + `"
    sha256 cellar: :any_skip_relocation, arm64_tahoe:       "` + tahoeDigest + `"
  end

  keg_only :versioned_formula
end
`

func TestHistoryAcceptsOnlyADigestBothSourcesAgreeOn(t *testing.T) {
	current := model.Formula{Name: "uv", Version: "0.13.6", Description: "kept", Steps: []json.RawMessage{json.RawMessage(`{}`)}}
	f, err := fakeHistory(t, uvRecipe).Formula(context.Background(), current, "0.13.5")
	if err != nil {
		t.Fatal(err)
	}
	b, ok := f.Bottles["arm64_tahoe"]
	if f.Version != "0.13.5" || !ok || len(f.Bottles) != 1 || b.SHA256 != tahoeDigest || b.Cellar != ":any_skip_relocation" || !strings.HasSuffix(b.URL, "/v2/homebrew/core/uv/blobs/sha256:"+tahoeDigest) {
		t.Fatalf("wrong bottle: %+v", f)
	}
	if strings.Join(f.Dependencies, ",") != "xz" || !f.KegOnly || f.CompatibilityVersion == nil || *f.CompatibilityVersion != 1 || f.Steps != nil || f.Description != "kept" {
		t.Fatalf("wrong recipe facts: %+v", f)
	}
	// golden_gate is a future macOS for this host and its digests disagree;
	// neither may be chosen.
	mismatch := strings.Replace(uvRecipe, tahoeDigest, strings.Repeat("5", 64), 1)
	if _, err := fakeHistory(t, mismatch).Formula(context.Background(), current, "0.13.5"); err == nil || !strings.Contains(err.Error(), "disagree") {
		t.Fatalf("disagreeing sources accepted: %v", err)
	}
	hook := strings.Replace(uvRecipe, "  keg_only", "  def post_install\n    system \"true\"\n  end\n\n  keg_only", 1)
	if _, err := fakeHistory(t, hook).Formula(context.Background(), current, "0.13.5"); !errors.Is(err, model.ErrUnsupported) {
		t.Fatalf("post_install accepted: %v", err)
	}
	if _, err := fakeHistory(t, uvRecipe).Formula(context.Background(), current, "0.12.0"); !errors.Is(err, model.ErrNotFound) {
		t.Fatalf("missing version: %v", err)
	}
}

func TestSelectTagPrefersNewestRevisionAndRebuild(t *testing.T) {
	tags := []string{"1.2", "1.2-1", "1.2_1", "1.2_1-2", "1.20", "1.2_x"}
	for version, want := range map[string]string{"1.2": "1.2_1-2", "1.2_1": "1.2_1-2", "1.20": "1.20", "1.3": ""} {
		tag, _, _, _, ok := selectTag(tags, version)
		if tag != want || ok != (want != "") {
			t.Errorf("selectTag(%s) = %q, want %q", version, tag, want)
		}
	}
	for _, v := range []string{"3.13", "0.13.5", "1.2_1"} {
		if !ValidVersion(v) {
			t.Errorf("%s rejected", v)
		}
	}
	for _, v := range []string{"", "latest", "../x", "1.2 3"} {
		if ValidVersion(v) {
			t.Errorf("%s accepted", v)
		}
	}
}

// TestHistoryRejectsRecipesThatDoNotDescribeTheBottle covers the checks that
// stand in for signed metadata on the unsigned history path.
func TestHistoryRejectsRecipesThatDoNotDescribeTheBottle(t *testing.T) {
	current := model.Formula{Name: "uv", Version: "0.13.6"}
	for _, tc := range []struct {
		name, old, new, want string
	}{
		{"pour condition", "  keg_only", "  pour_bottle? only_if: :clt_installed\n\n  keg_only", "pour condition"},
		{"revision", "  compatibility_version 1", "  revision 1\n  compatibility_version 1", "revision 1 does not match"},
		{"rebuild", "  bottle do\n", "  bottle do\n    rebuild 2\n", "rebuild 2 does not match"},
		{"declared version", "  compatibility_version 1", "  version \"0.13.4\"\n  compatibility_version 1", "declares version 0.13.4"},
		{"source URL", "tags/0.13.5.tar.gz", "tags/0.13.4.tar.gz", "does not name version 0.13.5"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recipe := strings.Replace(uvRecipe, tc.old, tc.new, 1)
			if recipe == uvRecipe {
				t.Fatal("the fixture did not change")
			}
			_, err := fakeHistory(t, recipe).Formula(context.Background(), current, "0.13.5")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestFormulaPathAcceptsOnlyTheFormulaInHomebrewCore(t *testing.T) {
	const core = "https://github.com/homebrew/homebrew-core/blob/" + bumpCommit + "/"
	for source, ok := range map[string]bool{
		core + "Formula/u/uv.rb":    true,
		core + "Formula/uv.rb":      true,
		core + "Formula/x/uv.rb":    false,
		core + "Formula/u/uvx.rb":   false,
		core + "Formula/u/../uv.rb": false,
		core + "Aliases/uv":         false,
		"https://github.com/homebrew/homebrew-cask/blob/" + bumpCommit + "/Formula/u/uv.rb": false,
		"https://github.com/homebrew/homebrew-core/blob/":                                   false,
		"http://github.com/homebrew/homebrew-core/blob/" + bumpCommit + "/Formula/u/uv.rb":  false,
	} {
		if _, err := formulaPath(source, "uv"); (err == nil) != ok {
			t.Errorf("formulaPath(%q) = %v, want ok=%v", source, err, ok)
		}
	}
}

// TestHistoryFindsAHostBottleAddedToAnOlderIndex follows the real oniguruma
// 6.9.9 contract. Older bottle uploads wrote a date-only creation time, and
// `brew pr-upload --keep-old` later added arm64_sequoia to the same index
// without changing its annotations. The commit that records that digest is
// eleven months newer than the index, behind a page of unrelated commits.
func TestHistoryFindsAHostBottleAddedToAnOlderIndex(t *testing.T) {
	const (
		sequoiaDigest = "378cec2d71c33ca25809db43e9fdd46957b3459fcd5b337c519fc74cc2cfdc20"
		sonomaDigest  = "53913ccbdad8ce504d0266fc20a867f2fc306d7331d066f2895f48d167102a24"
		laterBottle   = "5ce6fb6484e7e6c2d7a0b2bbde7c3e3e60b5f8e1"
		firstBottle   = "4ea1c8e0460000000000000000000000000000aa"
	)
	tab := `{"runtime_dependencies":[]}`
	recipe := func(lines string) string {
		return "class Oniguruma < Formula\n  url \"https://github.com/kkos/oniguruma/releases/download/v6.9.9/onig-6.9.9.tar.gz\"\n\n  bottle do\n" + lines + "  end\nend\n"
	}
	sonomaLine := `    sha256 cellar: :any, arm64_sonoma: "` + sonomaDigest + "\"\n"
	sequoiaLine := `    sha256 cellar: :any, arm64_sequoia: "` + sequoiaDigest + "\"\n"
	index := map[string]any{
		"schemaVersion": 2,
		"annotations": map[string]string{
			"org.opencontainers.image.created": "2023-10-14",
			"org.opencontainers.image.source":  "https://github.com/homebrew/homebrew-core/blob/0bef7da99efeb82e89861c090f1a1c2bbaec263a/Formula/o/oniguruma.rb",
		},
		"manifests": []map[string]any{
			{"annotations": map[string]string{"org.opencontainers.image.ref.name": "6.9.9.arm64_sonoma", "sh.brew.bottle.digest": sonomaDigest, "sh.brew.tab": tab}},
			{"annotations": map[string]string{"org.opencontainers.image.ref.name": "6.9.9.arm64_sequoia", "sh.brew.bottle.digest": sequoiaDigest, "sh.brew.tab": tab}},
		},
	}
	commit := func(sha, message string) map[string]any {
		return map[string]any{"sha": sha, "commit": map[string]string{"message": message}}
	}
	// Newest first, like the GitHub API; page 1 is full of unrelated commits.
	var all []map[string]any
	for i := range 100 {
		all = append(all, commit(strings.Repeat("e", 38)+strconv.Itoa(10+i), "oniguruma: style"))
	}
	all = append(all,
		commit(strings.Repeat("d", 40), "oniguruma 6.9.10"),
		commit(laterBottle, "oniguruma: update 6.9.9 bottle."),
		commit(strings.Repeat("a", 40), "oniguruma: standardize autoreconf and configure steps"),
		commit(firstBottle, "oniguruma: update 6.9.9 bottle."),
		commit(strings.Repeat("b", 40), "oniguruma 6.9.9"),
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch r.URL.Path {
		case "/token":
			_, _ = w.Write([]byte(`{"token":"t"}`))
		case "/v2/homebrew/core/oniguruma/tags/list":
			_, _ = w.Write([]byte(`{"tags":["6.9.8","6.9.9","6.9.10"]}`))
		case "/v2/homebrew/core/oniguruma/manifests/6.9.9":
			_ = json.NewEncoder(w).Encode(index)
		case "/repos/Homebrew/homebrew-core/commits":
			if q.Get("path") != "Formula/o/oniguruma.rb" || q.Get("since") != "2023-10-13T00:00:00Z" || q.Has("until") || q.Get("per_page") != "100" {
				t.Errorf("unexpected commit query %s", r.URL.RawQuery)
			}
			page, _ := strconv.Atoi(cmp.Or(q.Get("page"), "1"))
			_ = json.NewEncoder(w).Encode(all[min(len(all), (page-1)*100):min(len(all), page*100)])
		case "/Homebrew/homebrew-core/" + firstBottle + "/Formula/o/oniguruma.rb":
			_, _ = w.Write([]byte(recipe(sonomaLine)))
		case "/Homebrew/homebrew-core/" + laterBottle + "/Formula/o/oniguruma.rb":
			_, _ = w.Write([]byte(recipe(sequoiaLine + sonomaLine)))
		default:
			t.Errorf("unexpected request %s", r.URL)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	h := History{HTTP: server.Client(), Platform: model.Platform{OS: "darwin", Arch: "arm64", Major: 27}, GHCR: server.URL, API: server.URL, Raw: server.URL}
	f, err := h.Formula(context.Background(), model.Formula{Name: "oniguruma", Version: "6.9.10"}, "6.9.9")
	if err != nil {
		t.Fatal(err)
	}
	if b := f.Bottles["arm64_sequoia"]; f.Version != "6.9.9" || len(f.Bottles) != 1 || b.SHA256 != sequoiaDigest || b.Cellar != ":any" {
		t.Fatalf("wrong bottle: %+v", f)
	}
}
