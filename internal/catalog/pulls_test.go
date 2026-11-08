package catalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/shqingda/gobrew/internal/model"
)

func TestPullRequestsQueriesHomebrewPullRequests(t *testing.T) {
	var query string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		if r.URL.Path != "/search/issues" || r.Header.Get("Authorization") != "Bearer token" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"items":[{"title":"jq 1.8","html_url":"https://github.com/Homebrew/homebrew-core/pull/1","state":"closed"}]}`))
	}))
	defer server.Close()
	h := History{HTTP: server.Client(), API: server.URL, Token: "token"}
	got, err := h.PullRequests(context.Background(), "jq 1.8", "closed")
	if err != nil {
		t.Fatal(err)
	}
	if want := "q=jq+1.8+is%3Aclosed+type%3Apr+user%3AHomebrew&per_page=100"; query != want {
		t.Fatalf("query = %q, want %q", query, want)
	}
	if want := []model.PullRequest{{Title: "jq 1.8", URL: "https://github.com/Homebrew/homebrew-core/pull/1", State: "closed"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("pull requests = %#v", got)
	}
	if _, err = h.PullRequests(context.Background(), "jq", ""); err != nil || query != "q=jq+type%3Apr+user%3AHomebrew&per_page=100" {
		t.Fatalf("any state: %q, %v", query, err)
	}
}
