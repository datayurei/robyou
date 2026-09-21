package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/datayurei/robyou/enrollment"
	"github.com/datayurei/robyou/internal/catalog"
	"github.com/datayurei/robyou/internal/config"
	"github.com/datayurei/robyou/internal/logbus"
	"github.com/datayurei/robyou/internal/ratelimit"
)

const catalogXkid = "A1B2C3D4E5F60718293A4B5C6D7E8F90"

// catalogServer stands in for the search endpoint and records what it was
// asked for. It answers with pageSize rows until total is exhausted.
type catalogServer struct {
	mu sync.Mutex
	// total is how many rows one search has to offer. With perCategory set,
	// every category offers that many rows of its own, so a course's id says
	// which category it came from — the real server gives no such hint.
	total       int
	perCategory bool
	queries     []url.Values
	forms       []url.Values
}

func (c *catalogServer) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()

		c.mu.Lock()
		c.queries = append(c.queries, r.URL.Query())
		c.forms = append(c.forms, r.PostForm)
		c.mu.Unlock()

		start := atoiOr(r.PostForm.Get("iDisplayStart"), 0)
		length := atoiOr(r.PostForm.Get("iDisplayLength"), enrollment.DefaultPageSize)

		prefix := ""
		if c.perCategory {
			prefix = r.URL.Query().Get("szjylb") + "-"
		}

		rows := []map[string]any{}
		for i := start; i < start+length && i < c.total; i++ {
			rows = append(rows, map[string]any{
				"jx0404id": fmt.Sprintf("lesson-%s%d", prefix, i),
				"jx02id":   fmt.Sprintf("course-%d", i),
				"kcmc":     fmt.Sprintf("课程 %d", i),
				"skls":     "张三",
				"syrs":     3,
			})
		}

		json.NewEncoder(w).Encode(map[string]any{
			"aaData":               rows,
			"iTotalRecords":        c.total,
			"iTotalDisplayRecords": fmt.Sprint(c.total),
		})
	}
}

func (c *catalogServer) requests() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.queries)
}

func atoiOr(value string, fallback int) int {
	result := 0
	if value == "" {
		return fallback
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return fallback
		}
		result = result*10 + int(char-'0')
	}
	return result
}

// newCatalogEngine points the enrollment endpoints at a stub and pretends a
// round has already been entered.
func newCatalogEngine(t *testing.T, stub *catalogServer) *Engine {
	t.Helper()

	server := httptest.NewServer(stub.handler())
	t.Cleanup(server.Close)

	original := enrollment.BaseURL
	enrollment.BaseURL = server.URL
	t.Cleanup(func() { enrollment.BaseURL = original })

	runner := New(logbus.New(100), ratelimit.Unlimited, t.TempDir())
	runner.session.xkid = catalogXkid

	return runner
}

func TestRefreshCatalogRejectsInPlanWithoutKeyword(t *testing.T) {
	runner := New(logbus.New(10), ratelimit.Unlimited, t.TempDir())
	runner.session.xkid = catalogXkid

	_, err := runner.RefreshCatalog(context.Background(), CatalogRefreshRequest{Type: config.TypeInPlan})
	if err == nil {
		t.Fatal("an in-plan refresh without a keyword should fail: the server returns nothing")
	}
	if !strings.Contains(err.Error(), "关键词") {
		t.Fatalf("error should explain the keyword requirement, got %v", err)
	}
}

func TestRefreshCatalogRequiresARound(t *testing.T) {
	runner := New(logbus.New(10), ratelimit.Unlimited, t.TempDir())

	_, err := runner.RefreshCatalog(context.Background(), CatalogRefreshRequest{Type: config.TypePublic})
	if !errors.Is(err, ErrNoRound) {
		t.Fatalf("RefreshCatalog() = %v, want ErrNoRound", err)
	}
}

func TestRefreshCatalogPagesThroughThePublicList(t *testing.T) {
	stub := &catalogServer{total: 240}
	runner := newCatalogEngine(t, stub)

	result, err := runner.RefreshCatalog(context.Background(), CatalogRefreshRequest{
		Type: config.TypePublic,
		All:  true,
	})
	if err != nil {
		t.Fatalf("RefreshCatalog() = %v", err)
	}

	if result.Fetched != 240 || result.Added != 240 {
		t.Fatalf("result = %+v, want 240 fetched and added", result)
	}
	// 100 per page: two full pages and a short one that ends the loop.
	if result.Pages != 3 || stub.requests() != 3 {
		t.Fatalf("pages = %d, requests = %d, want 3 of each", result.Pages, stub.requests())
	}
	if result.Truncated {
		t.Fatal("a completed fetch should not be marked truncated")
	}

	stats := runner.CatalogStats()
	if stats.Total != 240 || stats.Public != 240 {
		t.Fatalf("stats = %+v, want 240 public courses", stats)
	}
	if stats.PublicFetchedAt == nil {
		t.Fatal("a full public fetch should record when it happened")
	}

	starts := []string{}
	stub.mu.Lock()
	for _, form := range stub.forms {
		starts = append(starts, form.Get("iDisplayStart"))
	}
	stub.mu.Unlock()
	if strings.Join(starts, ",") != "0,100,200" {
		t.Fatalf("paging offsets = %v", starts)
	}
}

func TestRefreshCatalogSinglePageByDefault(t *testing.T) {
	stub := &catalogServer{total: 240}
	runner := newCatalogEngine(t, stub)

	result, err := runner.RefreshCatalog(context.Background(), CatalogRefreshRequest{
		Type:    config.TypePublic,
		Keyword: "音乐",
	})
	if err != nil {
		t.Fatalf("RefreshCatalog() = %v", err)
	}
	if result.Pages != 1 || result.Fetched != 100 {
		t.Fatalf("result = %+v, want one page", result)
	}

	// A keyword search is not a full catalog fetch.
	if runner.CatalogStats().PublicFetchedAt != nil {
		t.Fatal("a keyword search must not count as fetching the whole catalog")
	}
}

func TestRefreshCatalogIncludeFilteredUnhidesCourses(t *testing.T) {
	stub := &catalogServer{total: 5}
	runner := newCatalogEngine(t, stub)

	if _, err := runner.RefreshCatalog(context.Background(), CatalogRefreshRequest{
		Type:            config.TypePublic,
		IncludeFiltered: true,
	}); err != nil {
		t.Fatalf("RefreshCatalog() = %v", err)
	}

	stub.mu.Lock()
	query := stub.queries[0]
	stub.mu.Unlock()

	for _, flag := range []string{"sfym", "sfct", "sfxx"} {
		if query.Get(flag) != "false" {
			t.Fatalf("%s = %q, want false so full and clashing courses are cached", flag, query.Get(flag))
		}
	}
}

func TestRefreshCatalogKeepsServerFiltersWhenAsked(t *testing.T) {
	stub := &catalogServer{total: 5}
	runner := newCatalogEngine(t, stub)

	if _, err := runner.RefreshCatalog(context.Background(), CatalogRefreshRequest{
		Type: config.TypePublic,
	}); err != nil {
		t.Fatalf("RefreshCatalog() = %v", err)
	}

	stub.mu.Lock()
	query := stub.queries[0]
	stub.mu.Unlock()

	if query.Get("sfym") != "true" {
		t.Fatalf("sfym = %q, want the default true", query.Get("sfym"))
	}
}

func TestSearchCatalogIsLocalOnly(t *testing.T) {
	stub := &catalogServer{total: 3}
	runner := newCatalogEngine(t, stub)

	if _, err := runner.RefreshCatalog(context.Background(), CatalogRefreshRequest{
		Type: config.TypePublic,
		All:  true,
	}); err != nil {
		t.Fatalf("RefreshCatalog() = %v", err)
	}
	before := stub.requests()

	results := runner.SearchCatalog(catalog.Query{Text: "课程 1"})
	if len(results.Entries) != 1 {
		t.Fatalf("local search returned %d entries, want 1", len(results.Entries))
	}
	if stub.requests() != before {
		t.Fatal("a local search must not touch the server")
	}
}

func TestPollingSearchFeedsTheCatalog(t *testing.T) {
	stub := &catalogServer{total: 4}
	runner := newCatalogEngine(t, stub)

	job := config.Job{ID: "job-1", Name: "job", Enabled: true}
	target := config.Target{Name: "t", Type: config.TypeInPlan, Keyword: "高数", Enabled: true}
	runner.status.setJobs([]JobStatus{{ID: "job-1", Targets: []TargetStatus{{Name: "t"}}}})

	if _, err := runner.runTarget(context.Background(), job, 0, target); err != nil {
		t.Fatalf("runTarget() = %v", err)
	}

	// In-plan courses are only ever cached as a side effect of searching.
	stats := runner.CatalogStats()
	if stats.InPlan != 4 {
		t.Fatalf("cached in-plan courses = %d, want 4", stats.InPlan)
	}
	if len(stats.Keywords) != 1 || stats.Keywords[0] != "高数" {
		t.Fatalf("keywords = %v, want the search keyword recorded", stats.Keywords)
	}
}

func TestClearCatalog(t *testing.T) {
	stub := &catalogServer{total: 3}
	runner := newCatalogEngine(t, stub)

	if _, err := runner.RefreshCatalog(context.Background(), CatalogRefreshRequest{Type: config.TypePublic}); err != nil {
		t.Fatalf("RefreshCatalog() = %v", err)
	}
	if err := runner.ClearCatalog(); err != nil {
		t.Fatalf("ClearCatalog() = %v", err)
	}
	if got := runner.CatalogStats().Total; got != 0 {
		t.Fatalf("catalog total after clear = %d", got)
	}
}

func TestRefreshCatalogSendsTheRequestedCategory(t *testing.T) {
	stub := &catalogServer{total: 3}
	runner := newCatalogEngine(t, stub)
	category := 4

	if _, err := runner.RefreshCatalog(context.Background(), CatalogRefreshRequest{
		Type:           config.TypePublic,
		PublicCategory: &category,
		All:            true,
	}); err != nil {
		t.Fatalf("RefreshCatalog() = %v", err)
	}

	stub.mu.Lock()
	sent := stub.queries[0].Get("szjylb")
	stub.mu.Unlock()
	if sent != "4" {
		t.Fatalf("szjylb = %q, want 4", sent)
	}

	// Every course fetched under a category is filed under it, and a fetch
	// that saw only one category has not seen the whole catalog.
	stats := runner.CatalogStats()
	if len(stats.Categories) != 1 || stats.Categories[0].Value != 4 || stats.Categories[0].Count != 3 {
		t.Fatalf("categories = %+v", stats.Categories)
	}
	if stats.PublicFetchedAt != nil {
		t.Fatal("a single-category fetch is not a full catalog fetch")
	}
}

func TestRefreshCatalogEachCategoryWalksTheDropdown(t *testing.T) {
	stub := &catalogServer{total: 2, perCategory: true}
	runner := newCatalogEngine(t, stub)

	result, err := runner.RefreshCatalog(context.Background(), CatalogRefreshRequest{
		Type:         config.TypePublic,
		EachCategory: true,
		All:          true,
	})
	if err != nil {
		t.Fatalf("RefreshCatalog() = %v", err)
	}

	stub.mu.Lock()
	sent := []string{}
	for _, query := range stub.queries {
		sent = append(sent, query.Get("szjylb"))
	}
	stub.mu.Unlock()

	// 全部课程 is skipped: it is the dropdown's "no filter" entry, and a
	// course found through it would land in the cache without a category.
	if strings.Join(sent, ",") != "1,2,3,4,5,6,7" {
		t.Fatalf("categories searched = %v", sent)
	}
	if result.Fetched != 14 {
		t.Fatalf("fetched = %d, want 2 courses in each of the 7 categories", result.Fetched)
	}

	stats := runner.CatalogStats()
	if len(stats.Categories) != 7 || stats.Uncategorized != 0 {
		t.Fatalf("stats = %+v, want every course filed under a category", stats)
	}
	if stats.PublicFetchedAt == nil {
		t.Fatal("walking every category with no keyword does fetch the whole catalog")
	}

	// The category is a local filter afterwards, with no further requests.
	sport := 1
	if got := runner.SearchCatalog(catalog.Query{PublicCategory: &sport}); len(got.Entries) != 2 {
		t.Fatalf("体育课 search returned %d entries, want 2", len(got.Entries))
	}
}

func TestPollingSearchRecordsTheTargetCategory(t *testing.T) {
	stub := &catalogServer{total: 2}
	runner := newCatalogEngine(t, stub)
	category := 5

	job := config.Job{ID: "job-1", Name: "job", Enabled: true}
	target := config.Target{
		Name:           "t",
		Type:           config.TypePublic,
		Keyword:        "价值",
		Enabled:        true,
		PublicCategory: &category,
	}
	runner.status.setJobs([]JobStatus{{ID: "job-1", Targets: []TargetStatus{{Name: "t"}}}})

	if _, err := runner.runTarget(context.Background(), job, 0, target); err != nil {
		t.Fatalf("runTarget() = %v", err)
	}

	stats := runner.CatalogStats()
	if len(stats.Categories) != 1 || stats.Categories[0].Value != category {
		t.Fatalf("categories = %+v, want the target's category recorded", stats.Categories)
	}
}
