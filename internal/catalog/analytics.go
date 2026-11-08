package catalog

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/shqingda/gobrew/internal/model"
	"github.com/shqingda/gobrew/internal/platform"
)

const (
	// analyticsStale is brew's UNSIGNED_API_STALE_SECONDS: one hour.
	analyticsStale   = time.Hour
	maxAnalyticsBody = 4 << 20
	// A ranking lists every formula, so it is far larger than one formula.
	maxGlobalAnalyticsBody = 32 << 20
)

// FormulaAnalytics reads the install counts brew info shows. They are display
// data outside the signed catalog, so nothing else reads them. A response is
// reused for an hour, and an older one when the API cannot be reached. It
// returns nil when the API lists no counts.
func (c Client) FormulaAnalytics(ctx context.Context, name string) ([]model.Analytics, error) {
	if !formulaName.MatchString(name) {
		return nil, fmt.Errorf("unsafe formula name %q", name)
	}
	cache := filepath.Join(c.Dir, "analytics", name+".json")
	info, statErr := os.Stat(cache)
	if statErr == nil && time.Since(info.ModTime()) < analyticsStale {
		if data, err := os.ReadFile(cache); err == nil {
			return parseAnalytics(data)
		}
	}
	data, err := c.fetchAnalytics(ctx, name)
	if err != nil {
		if statErr == nil {
			if cached, readErr := os.ReadFile(cache); readErr == nil {
				return parseAnalytics(cached)
			}
		}
		return nil, err
	}
	analytics, err := parseAnalytics(data)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(filepath.Dir(cache), 0700); err == nil {
		err = platform.WriteFileAtomic(cache, data, 0600)
	}
	return analytics, err
}

// fetchAnalytics makes one attempt, so an unreachable API does not hold up
// info, and returns the raw analytics object.
func (c Client) fetchAnalytics(ctx context.Context, name string) ([]byte, error) {
	body, err := c.apiGet(ctx, "formula/"+name+".json", maxAnalyticsBody)
	if err != nil {
		return nil, err
	}
	var formula struct {
		Analytics json.RawMessage `json:"analytics"`
	}
	if err := json.Unmarshal(body, &formula); err != nil {
		return nil, fmt.Errorf("decode analytics: %w", err)
	}
	if len(formula.Analytics) == 0 || formula.Analytics[0] != '{' {
		return []byte("{}"), nil
	}
	return formula.Analytics, nil
}

// apiGet makes one GET of an unsigned file next to the catalog index.
func (c Client) apiGet(ctx context.Context, endpoint string, limit int64) ([]byte, error) {
	base, err := url.Parse(cmp.Or(c.URL, defaultURL))
	if err != nil {
		return nil, err
	}
	base.Path = path.Join(path.Dir(base.Path), endpoint)
	base.RawQuery = ""
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", model.UserAgent)
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download analytics: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, errors.New("download analytics: HTTP " + res.Status)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, errors.New("analytics exceed size limit")
	}
	return body, nil
}

// parseAnalytics follows brew's output_analytics: each period's count is the
// sum over its formula variants, and "30d" counts as 30 days. Every level
// keeps the API's order.
func parseAnalytics(data []byte) ([]model.Analytics, error) {
	categories, err := orderedObject(data)
	if err != nil {
		return nil, err
	}
	var result []model.Analytics
	for _, category := range categories {
		periods, err := orderedObject(category.value)
		if err != nil {
			return nil, err
		}
		a := model.Analytics{Category: strings.ReplaceAll(category.key, "_", "-"), Periods: []model.AnalyticsPeriod{}}
		for _, period := range periods {
			counts, err := orderedObject(period.value)
			if err != nil {
				return nil, err
			}
			p := model.AnalyticsPeriod{Days: leadingInt(period.key), Results: make([]model.AnalyticsCount, 0, len(counts))}
			for _, count := range counts {
				var n int64
				if err = json.Unmarshal(count.value, &n); err != nil {
					return nil, fmt.Errorf("decode analytics: %w", err)
				}
				p.Count += n
				p.Results = append(p.Results, model.AnalyticsCount{Name: count.key, Count: n})
			}
			a.Periods = append(a.Periods, p)
		}
		result = append(result, a)
	}
	return result, nil
}

type member struct {
	key   string
	value json.RawMessage
}

// orderedObject decodes one JSON object, keeping its key order.
func orderedObject(data []byte) ([]member, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	if token, err := dec.Token(); err != nil || token != json.Delim('{') {
		return nil, errors.New("decode analytics: expected an object")
	}
	var members []member
	for dec.More() {
		token, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("decode analytics: %w", err)
		}
		key, _ := token.(string)
		var value json.RawMessage
		if err = dec.Decode(&value); err != nil {
			return nil, fmt.Errorf("decode analytics: %w", err)
		}
		members = append(members, member{key, value})
	}
	return members, nil
}

// leadingInt is Ruby's String#to_i for the API's period keys.
func leadingInt(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			break
		}
		n = n*10 + int(r-'0')
	}
	return n
}

var analyticsCategory = regexp.MustCompile(`^[a-z][a-z-]*$`)

// GlobalAnalytics reads one formulae.brew.sh ranking, such as install over 30
// days, like brew's Analytics.fetch: one attempt and no cache. Rows keep the
// API's order; a name listed twice keeps its first place and its last count,
// as a Ruby hash does.
func (c Client) GlobalAnalytics(ctx context.Context, category string, days int) ([]model.AnalyticsCount, error) {
	if !analyticsCategory.MatchString(category) || days <= 0 {
		return nil, fmt.Errorf("unsafe analytics category %q or days %d", category, days)
	}
	data, err := c.apiGet(ctx, fmt.Sprintf("analytics/%s/%dd.json", category, days), maxGlobalAnalyticsBody)
	if err != nil {
		return nil, err
	}
	var ranking struct {
		Items []map[string]any `json:"items"`
	}
	if err = json.Unmarshal(data, &ranking); err != nil {
		return nil, fmt.Errorf("decode analytics: %w", err)
	}
	key := map[string]string{"os-version": "os_version", "cask-install": "cask"}[category]
	if key == "" {
		key = "formula"
	}
	rows := []model.AnalyticsCount{}
	index := map[string]int{}
	for _, item := range ranking.Items {
		name, _ := item[key].(string)
		count, _ := item["count"].(string)
		n := int64(leadingInt(strings.ReplaceAll(count, ",", "")))
		if i, seen := index[name]; seen {
			rows[i].Count = n
			continue
		}
		index[name] = len(rows)
		rows = append(rows, model.AnalyticsCount{Name: name, Count: n})
	}
	return rows, nil
}
