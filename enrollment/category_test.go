package enrollment

import "testing"

func TestPublicCategoriesFollowTheDropdownOrder(t *testing.T) {
	categories := PublicCategories()
	if len(categories) != 8 {
		t.Fatalf("got %d categories, want the dropdown's 8 entries", len(categories))
	}

	// The numbers are the dropdown read top to bottom from zero, so the first
	// entry is 「--所有课程--」 and the first real category, 体育课, is 1.
	for i, category := range categories {
		if category.Value != i {
			t.Fatalf("category %q = %d, want %d", category.Name, category.Value, i)
		}
	}
	if categories[0].Value != PublicCategoryAll {
		t.Fatalf("first entry = %d, want PublicCategoryAll", categories[0].Value)
	}
	if categories[1].Name != "体育课" || categories[7].Name != "其他通识课" {
		t.Fatalf("category labels = %q … %q", categories[1].Name, categories[7].Name)
	}
}

func TestPublicCategoryNameFallsBackToTheNumber(t *testing.T) {
	if got := PublicCategoryName(2); got != "艺术教育课" {
		t.Fatalf("PublicCategoryName(2) = %q", got)
	}
	// The school can add categories; an unknown number is still usable.
	if got := PublicCategoryName(99); got != "类别 99" {
		t.Fatalf("PublicCategoryName(99) = %q", got)
	}
	if KnownPublicCategory(99) {
		t.Fatal("99 should not be reported as a known category")
	}
}

func TestNarrowsPublicSearch(t *testing.T) {
	all := PublicCategoryAll
	sport := 1

	if NarrowsPublicSearch(nil) {
		t.Fatal("an unset category searches everything")
	}
	if NarrowsPublicSearch(&all) {
		t.Fatal("全部课程 is not a filter")
	}
	if !NarrowsPublicSearch(&sport) {
		t.Fatal("体育课 restricts the search")
	}
}

func TestSearchParamsCarryTheCategory(t *testing.T) {
	category := 3

	params := buildSearchParams(CourseTypePublic, "", nil, &category)
	if got := params.Get("szjylb"); got != "3" {
		t.Fatalf("szjylb = %q, want 3", got)
	}

	if got := buildSearchParams(CourseTypePublic, "", nil, nil).Get("szjylb"); got != "" {
		t.Fatalf("szjylb without a category = %q, want empty", got)
	}

	// In-plan searches have no category dropdown at all.
	if _, ok := buildSearchParams(CourseTypeInPlan, "高数", nil, &category)["szjylb"]; ok {
		t.Fatal("an in-plan search must not send szjylb")
	}
}
