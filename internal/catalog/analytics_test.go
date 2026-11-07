package catalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shqingda/gobrew/internal/model"
)

func TestFormulaAnalyticsKeepsAPIOrderAndCachesForAnHour(t *testing.T) {
	var requests atomic.Int32
	failing := atomic.Bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if failing.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		if r.URL.Path != "/api/formula/jq.json" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"name":"jq","analytics":{"install_on_request":{"90d":{"jq":3,"jq --HEAD":4},"30d":{"jq":1}},"install":{"30d":{"jq":1000,"jq --HEAD":234}},"build_error":{}}}`))
	}))
	defer server.Close()
	c := Client{Dir: t.TempDir(), HTTP: server.Client(), URL: server.URL + "/api/formula.jws.json"}
	want := []model.Analytics{
		{Category: "install-on-request", Periods: []model.AnalyticsPeriod{{Days: 90, Count: 7}, {Days: 30, Count: 1}}},
		{Category: "install", Periods: []model.AnalyticsPeriod{{Days: 30, Count: 1234}}},
		{Category: "build-error", Periods: []model.AnalyticsPeriod{}},
	}
	for range 2 {
		got, err := c.FormulaAnalytics(context.Background(), "jq")
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("analytics = %#v", got)
		}
	}
	if n := requests.Load(); n != 1 {
		t.Fatalf("a fresh response was fetched %d times", n)
	}

	// An hour later the API is down: the older response is still shown.
	cache := filepath.Join(c.Dir, "analytics", "jq.json")
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(cache, old, old); err != nil {
		t.Fatal(err)
	}
	failing.Store(true)
	got, err := c.FormulaAnalytics(context.Background(), "jq")
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("stale fallback = %#v, %v", got, err)
	}
	if n := requests.Load(); n != 2 {
		t.Fatalf("a stale response was not revalidated: %d requests", n)
	}
	if _, err := c.FormulaAnalytics(context.Background(), "missing"); err == nil {
		t.Fatal("a failed request without a cache returned no error")
	}
}

func TestFormulaAnalyticsWithoutCounts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"name":"new"}`))
	}))
	defer server.Close()
	c := Client{Dir: t.TempDir(), HTTP: server.Client(), URL: server.URL + "/formula.jws.json"}
	got, err := c.FormulaAnalytics(context.Background(), "new")
	if err != nil || got != nil {
		t.Fatalf("analytics = %#v, %v", got, err)
	}
	if _, err := c.FormulaAnalytics(context.Background(), "../etc"); err == nil {
		t.Fatal("an unsafe name was accepted")
	}
}
