package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/datayurei/robyou/enrollment"
	"github.com/datayurei/robyou/internal/catalog"
	"github.com/datayurei/robyou/internal/config"
	"github.com/datayurei/robyou/internal/logbus"
)

// CatalogXkid is the round the cache is showing: the live round when one has
// been entered, otherwise the most recently cached one, so the course list is
// browsable before logging in.
func (e *Engine) CatalogXkid() string {
	if xkid := e.session.Xkid(); xkid != "" {
		return xkid
	}
	if xkid, ok := e.catalog.LatestXkid(); ok {
		return xkid
	}
	return ""
}

// CatalogStats summarises the cached course list for the current round.
func (e *Engine) CatalogStats() catalog.Stats {
	return e.catalog.Stats(e.CatalogXkid())
}

// SearchCatalog queries the cache. It is a local, offline search: no request
// is made, nothing is fetched, and it works while a run is in progress or
// while the session is logged out.
func (e *Engine) SearchCatalog(query catalog.Query) catalog.Results {
	return e.catalog.Search(e.CatalogXkid(), query)
}

// ClearCatalog drops the cached course list for the current round.
func (e *Engine) ClearCatalog() error {
	xkid := e.CatalogXkid()
	if err := e.catalog.Clear(xkid); err != nil {
		return err
	}

	e.logger("", "").Infof("已清空课程库缓存 (轮次 %s)", xkid)
	return nil
}

// FlushCatalog persists any pending cache changes.
func (e *Engine) FlushCatalog() error { return e.catalog.Flush() }

// RefreshCatalog asks the server for course information and merges it into the
// cache. This is the server-side search, kept separate from SearchCatalog: it
// costs requests, is rate limited like everything else, and is the only thing
// that can bring new courses into the cache.
func (e *Engine) RefreshCatalog(ctx context.Context, request CatalogRefreshRequest) (CatalogRefreshResult, error) {
	courseType, err := courseTypeOf(request.Type)
	if err != nil {
		return CatalogRefreshResult{}, err
	}

	keyword := strings.TrimSpace(request.Keyword)
	if courseType == enrollment.CourseTypeInPlan && keyword == "" {
		// Not a limitation worth hiding: the server answers an empty in-plan
		// search with an empty list, so there is nothing to fetch.
		return CatalogRefreshResult{}, errors.New("计划内课程必须填写搜索关键词，服务器不会返回完整的计划内课程列表 (in-plan search returns nothing without a keyword)")
	}

	xkid := e.session.Xkid()
	if xkid == "" {
		return CatalogRefreshResult{}, ErrNoRound
	}

	if !e.catalogFetching.CompareAndSwap(false, true) {
		return CatalogRefreshResult{}, ErrCatalogBusy
	}
	defer e.catalogFetching.Store(false)

	log := e.logger("课程库", "")
	if e.IsRunning() {
		log.Warnf("选课任务正在运行，课程库更新会与其共用限速额度")
	}

	passes := refreshPasses(courseType, request)
	result := CatalogRefreshResult{Type: string(courseType), Keyword: keyword}

	for _, category := range passes {
		if err := e.fetchCatalogPass(ctx, log, xkid, courseType, keyword, category, request, &result); err != nil {
			return result, err
		}
		if result.Truncated {
			break
		}
	}

	// Only a fetch that asked for every course, in every category, has seen
	// the whole public catalog: one restricted to a category saw a slice of it.
	everyCategory := request.EachCategory || !enrollment.NarrowsPublicSearch(request.PublicCategory)
	if courseType == enrollment.CourseTypePublic && keyword == "" && request.All && everyCategory && !result.Truncated {
		e.catalog.MarkPublicFetched(xkid)
	}
	if err := e.catalog.Flush(); err != nil {
		log.Warnf("课程库写入失败: %v", err)
	}

	stats := e.catalog.Stats(xkid)
	result.Total = stats.Total
	result.Message = fmt.Sprintf("获取 %d 门，新增 %d 门，课程库共 %d 门", result.Fetched, result.Added, stats.Total)
	log.Successf("%s完成: %s", describeCatalogRequest(courseType, keyword, nil), result.Message)

	return result, nil
}

// fetchCatalogPass pages through one search and folds every page into the
// cache. A pass is one keyword and at most one category: the categories are
// walked one pass at a time because the search rows carry no category of their
// own, so the only way the cache learns one is from the request that found the
// course.
func (e *Engine) fetchCatalogPass(
	ctx context.Context,
	log *logbus.Logger,
	xkid string,
	courseType enrollment.CourseType,
	keyword string,
	category *int,
	request CatalogRefreshRequest,
	result *CatalogRefreshResult,
) error {
	options := enrollment.SearchOptions{
		Keyword:        keyword,
		PublicCategory: category,
		PageSize:       catalogPageSize,
		Filters:        catalogFilters(request.IncludeFiltered),
	}
	source := catalog.Source{
		Type:           string(courseType),
		Keyword:        keyword,
		PublicCategory: category,
	}

	describe := describeCatalogRequest(courseType, keyword, category)
	log.Infof("开始从服务器获取%s", describe)
	fetchedHere := 0

	for page := 0; ; page++ {
		if ctx.Err() != nil {
			result.Truncated = true
			break
		}
		if page >= catalogMaxPages {
			result.Truncated = true
			log.Warnf("已达最大页数 %d，停止获取", catalogMaxPages)
			break
		}

		options.Start = page * catalogPageSize
		response, err := enrollment.SearchCourses(ctx, e.client, courseType, options)
		if err != nil {
			if errors.Is(err, enrollment.ErrSessionExpired) {
				if recoverErr := e.recoverSession(ctx, log); recoverErr != nil {
					return recoverErr
				}
				return enrollment.ErrSessionExpired
			}
			return err
		}

		merged := e.catalog.Merge(xkid, source, response.Courses)
		result.Pages++
		result.Fetched += len(response.Courses)
		result.Added += merged.Added
		result.Updated += merged.Updated
		result.Total = merged.Total
		fetchedHere += len(response.Courses)

		if !request.All || len(response.Courses) < catalogPageSize {
			break
		}
		log.Debugf("已获取 %d 门课程，继续下一页", result.Fetched)
	}

	if category != nil {
		log.Infof("%s: %d 门", describe, fetchedHere)
	}

	return nil
}

// refreshPasses decides which searches one refresh runs. Normally that is a
// single pass, restricted to the requested category or to none; EachCategory
// turns it into one pass per category, which is what fills in the category of
// every public course in the cache.
func refreshPasses(courseType enrollment.CourseType, request CatalogRefreshRequest) []*int {
	if courseType != enrollment.CourseTypePublic || !request.EachCategory {
		return []*int{request.PublicCategory}
	}

	passes := make([]*int, 0, len(enrollment.PublicCategories()))
	for _, category := range enrollment.PublicCategories() {
		if category.Value == enrollment.PublicCategoryAll {
			continue
		}
		value := category.Value
		passes = append(passes, &value)
	}
	return passes
}

// catalogFilters decides what the server is allowed to hide. The polling loop
// wants full and clashing courses filtered out; a catalog wants the real list.
func catalogFilters(includeFiltered bool) map[string]string {
	if !includeFiltered {
		return nil
	}
	return map[string]string{
		"sfym": "false",
		"sfct": "false",
		"sfxx": "false",
	}
}

func describeCatalogRequest(courseType enrollment.CourseType, keyword string, category *int) string {
	suffix := ""
	if enrollment.NarrowsPublicSearch(category) {
		suffix = fmt.Sprintf(" [%s]", enrollment.PublicCategoryName(*category))
	}
	if keyword == "" {
		return "全部公选课" + suffix
	}
	if courseType == enrollment.CourseTypePublic {
		return fmt.Sprintf("公选课 %q%s", keyword, suffix)
	}
	return fmt.Sprintf("计划内课程 %q", keyword)
}

func courseTypeOf(value string) (enrollment.CourseType, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", config.TypeInPlan:
		return enrollment.CourseTypeInPlan, nil
	case config.TypePublic:
		return enrollment.CourseTypePublic, nil
	default:
		return "", fmt.Errorf("课程类型必须是 inplan 或 public，收到 %q", value)
	}
}
