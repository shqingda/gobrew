package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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
			_, _ = w.Write([]byte(`[{"sha":"` + strings.Repeat("c", 40) + `"},{"sha":"` + bottleSHA + `"},{"sha":"` + strings.Repeat("b", 40) + `"}]`))
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
