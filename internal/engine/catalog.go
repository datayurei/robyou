package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/datayurei/robyou/enrollment"
	"github.com/datayurei/robyou/internal/catalog"
	"github.com/datayurei/robyou/internal/config"
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

	options := enrollment.SearchOptions{
		Keyword:        keyword,
		PublicCategory: request.PublicCategory,
		PageSize:       catalogPageSize,
		Filters:        catalogFilters(request.IncludeFiltered),
	}

	result := CatalogRefreshResult{Type: string(courseType), Keyword: keyword}
	describe := describeCatalogRequest(courseType, keyword)
	log.Infof("开始从服务器获取%s", describe)

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
					return result, recoverErr
				}
				return result, enrollment.ErrSessionExpired
			}
			return result, err
		}

		merged := e.catalog.Merge(xkid, string(courseType), keyword, response.Courses)
		result.Pages++
		result.Fetched += len(response.Courses)
		result.Added += merged.Added
		result.Updated += merged.Updated
		result.Total = merged.Total

		if !request.All || len(response.Courses) < catalogPageSize {
			break
		}
		log.Debugf("已获取 %d 门课程，继续下一页", result.Fetched)
	}

	if courseType == enrollment.CourseTypePublic && keyword == "" && request.All && !result.Truncated {
		e.catalog.MarkPublicFetched(xkid)
	}
	if err := e.catalog.Flush(); err != nil {
		log.Warnf("课程库写入失败: %v", err)
	}

	stats := e.catalog.Stats(xkid)
	result.Total = stats.Total
	result.Message = fmt.Sprintf("获取 %d 门，新增 %d 门，课程库共 %d 门", result.Fetched, result.Added, stats.Total)
	log.Successf("%s完成: %s", describe, result.Message)

	return result, nil
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

func describeCatalogRequest(courseType enrollment.CourseType, keyword string) string {
	if keyword == "" {
		return "全部公选课"
	}
	if courseType == enrollment.CourseTypePublic {
		return fmt.Sprintf("公选课 %q", keyword)
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
