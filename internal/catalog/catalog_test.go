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
	first := store.Merge(testXkid, TypeInPlan, "高等数学", courses(
		course("1", "高等数学A", "张三", "5"),
		course("2", "高等数学B", "李四", "0"),
	))
	if first.Added != 2 || first.Total != 2 {
		t.Fatalf("first merge = %+v, want 2 added", first)
	}

	second := store.Merge(testXkid, TypeInPlan, "线性代数", courses(
		course("3", "线性代数", "王五", "8"),
	))
	if second.Added != 1 || second.Total != 3 {
		t.Fatalf("second merge = %+v, want the cache to grow to 3", second)
	}

	// Seeing a course again refreshes it instead of duplicating it.
	third := store.Merge(testXkid, TypeInPlan, "高数", courses(
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
	store.Merge(testXkid, TypeInPlan, "音乐", courses(course("1", "音乐鉴赏", "张三", "3")))
	store.Merge(testXkid, TypePublic, "", courses(course("1", "音乐鉴赏(公选)", "李四", "9")))

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

	result := store.Merge(testXkid, TypePublic, "", courses(
		course("", "无效课程", "张三", "1"),
		course("7", "有效课程", "李四", "1"),
	))
	if result.Added != 1 || result.Total != 1 {
		t.Fatalf("merge = %+v, want the id-less row skipped", result)
	}
}

func TestSearchFiltersAndSorts(t *testing.T) {
	store := NewStore(t.TempDir())
	store.Merge(testXkid, TypePublic, "", courses(
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
	store.Merge(testXkid, TypePublic, "", courses(course("1", "音乐鉴赏", "张三", "3")))
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
	if result := reopened.Merge(testXkid, TypePublic, "", courses(course("1", "音乐鉴赏", "张三", "1"))); result.Added != 0 {
		t.Fatalf("merge after reload = %+v, want an update", result)
	}
}

func TestCachesAreKeyedByRound(t *testing.T) {
	store := NewStore(t.TempDir())
	other := "0F1E2D3C4B5A69788796A5B4C3D2E1F0"

	store.Merge(testXkid, TypePublic, "", courses(course("1", "音乐鉴赏", "张三", "3")))
	store.Merge(other, TypePublic, "", courses(course("9", "另一轮次的课", "李四", "2")))

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
	store.Merge(testXkid, TypePublic, "", courses(course("1", "音乐鉴赏", "张三", "3")))
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
		if got := store.Merge(xkid, TypePublic, "", courses(course("1", "x", "y", "1"))); got.Added != 0 {
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
	store.Merge(older, TypePublic, "", courses(course("1", "旧轮次", "张三", "1")))
	time.Sleep(2 * time.Millisecond)
	store.Merge(testXkid, TypePublic, "", courses(course("2", "新轮次", "李四", "1")))

	if latest, ok := store.LatestXkid(); !ok || latest != testXkid {
		t.Fatalf("LatestXkid() = %q / %v, want %q", latest, ok, testXkid)
	}
}
