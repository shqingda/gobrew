package catalog

import (
	"cmp"
	"context"
	"fmt"
	"strings"

	"github.com/shqingda/gobrew/internal/model"
	"github.com/shqingda/gobrew/internal/platform"
)

// PullRequests runs brew search --pull-request: GitHub's issue search for pull
// requests in the Homebrew organization that mention query, optionally only
// those whose state is only ("open" or "closed"). It returns at most one page
// of 100, in GitHub's order.
func (h History) PullRequests(ctx context.Context, query, only string) ([]model.PullRequest, error) {
	terms := []string{query}
	if only != "" {
		terms = append(terms, "is:"+only)
	}
	terms = append(terms, "type:pr", "user:Homebrew")
	auth := ""
	if h.Token != "" {
		auth = "Bearer " + h.Token
	}
	var result struct {
		Items []struct {
			Title string `json:"title"`
			URL   string `json:"html_url"`
			State string `json:"state"`
		} `json:"items"`
	}
	u := cmp.Or(h.API, "https://api.github.com") + "/search/issues?q=" + platform.FormComponent(strings.Join(terms, " ")) + "&per_page=100"
	if err := h.getJSON(ctx, u, auth, "application/vnd.github+json", &result); err != nil {
		return nil, fmt.Errorf("%w (set HOMEBREW_GITHUB_API_TOKEN if GitHub rate limits anonymous requests)", err)
	}
	prs := make([]model.PullRequest, 0, len(result.Items))
	for _, item := range result.Items {
		prs = append(prs, model.PullRequest{Title: item.Title, URL: item.URL, State: item.State})
	}
	return prs, nil
}
