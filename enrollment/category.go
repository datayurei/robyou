package enrollment

import "strconv"

// Public-elective categories, the 素质教育类别 dropdown the school UI shows
// above the public-course list and sends as `szjylb`.
//
// The numbers are the dropdown's own positions, counted from the top starting
// at zero, so `0` is its first entry — 「--所有课程--」 — and filters nothing.
// That is why PublicCategoryAll and an unset category mean the same list: the
// first real category, 体育课, is `1`.
const PublicCategoryAll = 0

// PublicCategory is one entry of that dropdown.
type PublicCategory struct {
	Value int    `json:"value"`
	Name  string `json:"name"`
}

// publicCategoryNames is the dropdown read top to bottom. The labels are the
// school UI's own; the index is the value sent as `szjylb`.
var publicCategoryNames = []string{
	"全部课程",
	"体育课",
	"艺术教育课",
	"书院实践（劳动）课程",
	"公民与社会选修课",
	"文化与价值选修课",
	"科学与科学方法选修课",
	"其他通识课",
}

// PublicCategories lists the categories in dropdown order, 全部课程 first.
func PublicCategories() []PublicCategory {
	out := make([]PublicCategory, 0, len(publicCategoryNames))
	for value, name := range publicCategoryNames {
		out = append(out, PublicCategory{Value: value, Name: name})
	}
	return out
}

// PublicCategoryName returns the dropdown label for a category number. An
// unknown number gets a numbered placeholder rather than an empty string: the
// school can add categories, and the number is still sent to the server as-is.
func PublicCategoryName(value int) string {
	if value < 0 || value >= len(publicCategoryNames) {
		return "类别 " + strconv.Itoa(value)
	}
	return publicCategoryNames[value]
}

// KnownPublicCategory reports whether the number is one this build knows a
// label for.
func KnownPublicCategory(value int) bool {
	return value >= 0 && value < len(publicCategoryNames)
}

// NarrowsPublicSearch reports whether a category actually restricts a search.
// Nil and PublicCategoryAll both mean "every category", so neither does.
func NarrowsPublicSearch(category *int) bool {
	return category != nil && *category != PublicCategoryAll
}
