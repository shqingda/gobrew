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
	"strings"
	"time"

	"github.com/shqingda/gobrew/internal/model"
	"github.com/shqingda/gobrew/internal/platform"
)

const (
	// analyticsStale is brew's UNSIGNED_API_STALE_SECONDS: one hour.
	analyticsStale   = time.Hour
	maxAnalyticsBody = 4 << 20
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
	base, err := url.Parse(cmp.Or(c.URL, defaultURL))
	if err != nil {
		return nil, err
	}
	base.Path = path.Join(path.Dir(base.Path), "formula", name+".json")
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
	body, err := io.ReadAll(io.LimitReader(res.Body, maxAnalyticsBody+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxAnalyticsBody {
		return nil, errors.New("analytics exceed size limit")
	}
	var formula struct {
		Analytics json.RawMessage `json:"analytics"`
	}
	if err = json.Unmarshal(body, &formula); err != nil {
		return nil, fmt.Errorf("decode analytics: %w", err)
	}
	if len(formula.Analytics) == 0 || formula.Analytics[0] != '{' {
		return []byte("{}"), nil
	}
	return formula.Analytics, nil
}

// parseAnalytics follows brew's output_analytics: each period's count is the
// sum over its formula variants, and "30d" counts as 30 days.
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
			var counts map[string]int64
			if err = json.Unmarshal(period.value, &counts); err != nil {
				return nil, fmt.Errorf("decode analytics: %w", err)
			}
			p := model.AnalyticsPeriod{Days: leadingInt(period.key)}
			for _, n := range counts {
				p.Count += n
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
