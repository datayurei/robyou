package catalog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/datayurei/robyou/enrollment"
)

const testXkid = "A1B2C3D4E5F60718293A4B5C6D7E8F90"

func courses(items ...enrollment.Course) []enrollment.Course { return items }

func course(lessonID, name, teacher, remaining string) enrollment.Course {
	return enrollment.Course{
		LessonID:  lessonID,
		EnrollID:  "enroll-" + lessonID,
		Code:      "CODE" + lessonID,
		Name:      name,
		Teacher:   teacher,
		Time:      "周三<br/>3-4节",
		Location:  "A101",
		Campus:    "桑浦山校区",
		Enrolled:  "30",
		Remaining: remaining,
	}
}

func TestMergeAccumulatesAcrossSearches(t *testing.T) {
	store := NewStore(t.TempDir())

	// In-plan courses can only ever be discovered one keyword at a time.
	first := store.Merge(testXkid, Source{Type: TypeInPlan, Keyword: "高等数学"}, courses(
		course("1", "高等数学A", "张三", "5"),
		course("2", "高等数学B", "李四", "0"),
	))
	if first.Added != 2 || first.Total != 2 {
		t.Fatalf("first merge = %+v, want 2 added", first)
	}

	second := store.Merge(testXkid, Source{Type: TypeInPlan, Keyword: "线性代数"}, courses(
		course("3", "线性代数", "王五", "8"),
	))
	if second.Added != 1 || second.Total != 3 {
		t.Fatalf("second merge = %+v, want the cache to grow to 3", second)
	}

	// Seeing a course again refreshes it instead of duplicating it.
	third := store.Merge(testXkid, Source{Type: TypeInPlan, Keyword: "高数"}, courses(
		course("1", "高等数学A", "张三", "2"),
	))
	if third.Added != 0 || third.Updated != 1 || third.Total != 3 {
		t.Fatalf("re-merge = %+v, want an update and no growth", third)
	}

	results := store.Search(testXkid, Query{Text: "高等数学A"})
	if len(results.Entries) != 1 {
		t.Fatalf("expected one match, got %d", len(results.Entries))
	}
	entry := results.Entries[0]
	if entry.Remaining != "2" {
		t.Fatalf("remaining = %q, want the refreshed value 2", entry.Remaining)
	}
	if len(entry.Keywords) != 2 {
		t.Fatalf("keywords = %v, want both searches recorded", entry.Keywords)
	}
	if entry.Time != "周三 3-4节" {
		t.Fatalf("time = %q, want the HTML break cleaned", entry.Time)
	}
}

func TestMergeKeepsTypesApart(t *testing.T) {
	store := NewStore(t.TempDir())

	// The same lesson id under both types must not collide.
	store.Merge(testXkid, Source{Type: TypeInPlan, Keyword: "音乐"}, courses(course("1", "音乐鉴赏", "张三", "3")))
	store.Merge(testXkid, Source{Type: TypePublic, Keyword: ""}, courses(course("1", "音乐鉴赏(公选)", "李四", "9")))

	stats := store.Stats(testXkid)
	if stats.Total != 2 || stats.InPlan != 1 || stats.Public != 1 {
		t.Fatalf("stats = %+v, want one of each type", stats)
	}

	if got := store.Search(testXkid, Query{Type: TypePublic}); len(got.Entries) != 1 ||
		got.Entries[0].Name != "音乐鉴赏(公选)" {
		t.Fatalf("public-only search returned %+v", got.Entries)
	}
}

func TestMergeIgnoresRowsWithoutLessonID(t *testing.T) {
	store := NewStore(t.TempDir())

	result := store.Merge(testXkid, Source{Type: TypePublic, Keyword: ""}, courses(
		course("", "无效课程", "张三", "1"),
		course("7", "有效课程", "李四", "1"),
	))
	if result.Added != 1 || result.Total != 1 {
		t.Fatalf("merge = %+v, want the id-less row skipped", result)
	}
}

func TestSearchFiltersAndSorts(t *testing.T) {
	store := NewStore(t.TempDir())
	store.Merge(testXkid, Source{Type: TypePublic, Keyword: ""}, courses(
		course("1", "音乐鉴赏", "张三", "0"),
		course("2", "体育舞蹈", "李四", "12"),
		course("3", "心理健康", "王五", "4"),
	))

	if got := store.Search(testXkid, Query{OnlyAvailable: true}); len(got.Entries) != 2 {
		t.Fatalf("only-available returned %d entries, want 2", len(got.Entries))
	}

	byRemaining := store.Search(testXkid, Query{Sort: "remaining"})
	if byRemaining.Entries[0].Name != "体育舞蹈" {
		t.Fatalf("sorted by remaining, first = %q", byRemaining.Entries[0].Name)
	}

	// Text search covers teacher and location, not just the course name.
	if got := store.Search(testXkid, Query{Text: "王五"}); len(got.Entries) != 1 {
		t.Fatalf("teacher search returned %d entries", len(got.Entries))
	}
	if got := store.Search(testXkid, Query{Text: "a101"}); len(got.Entries) != 3 {
		t.Fatalf("location search returned %d entries, want all 3", len(got.Entries))
	}

	limited := store.Search(testXkid, Query{Limit: 2})
	if len(limited.Entries) != 2 || limited.Matched != 3 || limited.Total != 3 {
		t.Fatalf("limited search = %+v", limited)
	}
}

func TestPersistenceRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	store.Merge(testXkid, Source{Type: TypePublic, Keyword: ""}, courses(course("1", "音乐鉴赏", "张三", "3")))
	store.MarkPublicFetched(testXkid)
	if err := store.Flush(); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, testXkid+".json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("cache file not written: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("cache file mode = %v, want 0600", perm)
	}

	reopened := NewStore(dir)
	stats := reopened.Stats(testXkid)
	if stats.Total != 1 || stats.Public != 1 {
		t.Fatalf("reloaded stats = %+v", stats)
	}
	if stats.PublicFetchedAt == nil {
		t.Fatal("public fetch time should survive a reload")
	}

	// A reloaded entry is recognised as known, not re-added.
	if result := reopened.Merge(testXkid, Source{Type: TypePublic, Keyword: ""}, courses(course("1", "音乐鉴赏", "张三", "1"))); result.Added != 0 {
		t.Fatalf("merge after reload = %+v, want an update", result)
	}
}

func TestCachesAreKeyedByRound(t *testing.T) {
	store := NewStore(t.TempDir())
	other := "0F1E2D3C4B5A69788796A5B4C3D2E1F0"

	store.Merge(testXkid, Source{Type: TypePublic, Keyword: ""}, courses(course("1", "音乐鉴赏", "张三", "3")))
	store.Merge(other, Source{Type: TypePublic, Keyword: ""}, courses(course("9", "另一轮次的课", "李四", "2")))

	if got := store.Stats(testXkid); got.Total != 1 {
		t.Fatalf("round A total = %d, want 1", got.Total)
	}
	if got := store.Search(other, Query{}); len(got.Entries) != 1 || got.Entries[0].Name != "另一轮次的课" {
		t.Fatalf("round B search = %+v", got.Entries)
	}
}

func TestClearRemovesRound(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	store.Merge(testXkid, Source{Type: TypePublic, Keyword: ""}, courses(course("1", "音乐鉴赏", "张三", "3")))
	store.Flush()

	if err := store.Clear(testXkid); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, testXkid+".json")); !os.IsNotExist(err) {
		t.Fatal("cache file should be gone")
	}
	if got := store.Stats(testXkid); got.Total != 0 {
		t.Fatalf("stats after clear = %+v", got)
	}
}

func TestRejectsUnsafeRoundIDs(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	// The round id comes from scraped HTML, so it must never reach the
	// filesystem unchecked.
	for _, xkid := range []string{"", "../escape", "a/b", strings.Repeat("x", 65)} {
		if got := store.Merge(xkid, Source{Type: TypePublic, Keyword: ""}, courses(course("1", "x", "y", "1"))); got.Added != 0 {
			t.Fatalf("merge with xkid %q was accepted", xkid)
		}
		if got := store.Search(xkid, Query{}); len(got.Entries) != 0 {
			t.Fatalf("search with xkid %q returned entries", xkid)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("unsafe ids wrote %d files", len(entries))
	}
}

func TestLatestXkidPrefersMostRecent(t *testing.T) {
	store := NewStore(t.TempDir())
	if _, ok := store.LatestXkid(); ok {
		t.Fatal("an empty store has no latest round")
	}

	older := "0F1E2D3C4B5A69788796A5B4C3D2E1F0"
	store.Merge(older, Source{Type: TypePublic, Keyword: ""}, courses(course("1", "旧轮次", "张三", "1")))
	time.Sleep(2 * time.Millisecond)
	store.Merge(testXkid, Source{Type: TypePublic, Keyword: ""}, courses(course("2", "新轮次", "李四", "1")))

	if latest, ok := store.LatestXkid(); !ok || latest != testXkid {
		t.Fatalf("LatestXkid() = %q / %v, want %q", latest, ok, testXkid)
	}
}

func TestMergeRecordsAndKeepsThePublicCategory(t *testing.T) {
	store := NewStore(t.TempDir())
	sport := 1
	all := enrollment.PublicCategoryAll

	// A category-restricted search is the only thing that says where a course
	// belongs: the rows themselves carry no category.
	store.Merge(testXkid, Source{Type: TypePublic, PublicCategory: &sport}, courses(
		course("1", "篮球", "张三", "3"),
	))
	entry := store.Search(testXkid, Query{}).Entries[0]
	if entry.PublicCategory == nil || *entry.PublicCategory != sport {
		t.Fatalf("category = %v, want 1", entry.PublicCategory)
	}

	// Seeing the same course through a search that spanned every category
	// must refresh the seats without forgetting the category.
	store.Merge(testXkid, Source{Type: TypePublic, PublicCategory: &all}, courses(
		course("1", "篮球", "张三", "0"),
	))
	store.Merge(testXkid, Source{Type: TypePublic}, courses(course("1", "篮球", "张三", "1")))

	entry = store.Search(testXkid, Query{}).Entries[0]
	if entry.PublicCategory == nil || *entry.PublicCategory != sport {
		t.Fatalf("category after an uncategorised sighting = %v, want 1 kept", entry.PublicCategory)
	}
	if entry.Remaining != "1" {
		t.Fatalf("remaining = %q, want the refreshed value", entry.Remaining)
	}

	// An in-plan search never files a course under a category.
	store.Merge(testXkid, Source{Type: TypeInPlan, Keyword: "篮球", PublicCategory: &sport}, courses(
		course("1", "篮球(计划内)", "李四", "2"),
	))
	inplan := store.Search(testXkid, Query{Type: TypeInPlan}).Entries[0]
	if inplan.PublicCategory != nil {
		t.Fatalf("in-plan category = %v, want none", inplan.PublicCategory)
	}
}

func TestSearchFiltersByCategory(t *testing.T) {
	store := NewStore(t.TempDir())
	sport, art, all := 1, 2, enrollment.PublicCategoryAll

	store.Merge(testXkid, Source{Type: TypePublic, PublicCategory: &sport}, courses(
		course("1", "篮球", "张三", "3"),
		course("2", "游泳", "李四", "1"),
	))
	store.Merge(testXkid, Source{Type: TypePublic, PublicCategory: &art}, courses(
		course("3", "合唱", "王五", "2"),
	))
	store.Merge(testXkid, Source{Type: TypePublic}, courses(
		course("4", "其他课", "赵六", "5"),
	))

	if got := store.Search(testXkid, Query{PublicCategory: &sport}); len(got.Entries) != 2 {
		t.Fatalf("体育课 search returned %d entries, want 2", len(got.Entries))
	}
	// 全部课程 is the dropdown's "no filter" entry, not a category.
	if got := store.Search(testXkid, Query{PublicCategory: &all}); len(got.Entries) != 4 {
		t.Fatalf("全部课程 search returned %d entries, want all 4", len(got.Entries))
	}
	if got := store.Search(testXkid, Query{OnlyUncategorized: true}); len(got.Entries) != 1 ||
		got.Entries[0].Name != "其他课" {
		t.Fatalf("uncategorised search = %+v", got.Entries)
	}
	// The category label is searchable text too.
	if got := store.Search(testXkid, Query{Text: "艺术教育"}); len(got.Entries) != 1 ||
		got.Entries[0].Name != "合唱" {
		t.Fatalf("category-name search = %+v", got.Entries)
	}

	stats := store.Stats(testXkid)
	if stats.Uncategorized != 1 {
		t.Fatalf("uncategorised count = %d, want 1", stats.Uncategorized)
	}
	if len(stats.Categories) != 2 ||
		stats.Categories[0].Value != sport || stats.Categories[0].Count != 2 ||
		stats.Categories[1].Name != "艺术教育课" || stats.Categories[1].Count != 1 {
		t.Fatalf("category counts = %+v", stats.Categories)
	}
}

func TestCategorySurvivesAReload(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	sport := 1

	store.Merge(testXkid, Source{Type: TypePublic, PublicCategory: &sport}, courses(
		course("1", "篮球", "张三", "3"),
	))
	if err := store.Flush(); err != nil {
		t.Fatal(err)
	}

	stats := NewStore(dir).Stats(testXkid)
	if len(stats.Categories) != 1 || stats.Categories[0].Value != sport {
		t.Fatalf("reloaded categories = %+v", stats.Categories)
	}
}
