package enrollment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/datayurei/robyou/httpclient"
)

// BaseURL is the teaching-management host. It is a var rather than a const so
// tests can point the endpoints at a stub server; nothing changes it at runtime.
var BaseURL = "https://jw.stu.edu.cn"

const (
	EndpointEnrollmentSession = "/jsxsd/xsxk/xklc_list?Ves632DSdyV=NEW_XSD_PYGL"
	EndpointEnrollmentInit    = "/jsxsd/xsxk/newXsxkzx"
	EndpointSelectBottom      = "/jsxsd/xsxk/selectBottom"
	EndpointInPlanSearch      = "/jsxsd/xsxkkc/xsxkBxqjhxk"
	EndpointPublicSearch      = "/jsxsd/xsxkkc/xsxkGgxxkxk"
	EndpointInPlanEnroll      = "/jsxsd/xsxkkc/bxqjhxkOper"
	EndpointPublicEnroll      = "/jsxsd/xsxkkc/ggxxkxkOper"

	RateLimitIndicator = "注销"
)

var ErrSessionExpired = errors.New("session expired")

type CourseType string

const (
	CourseTypeInPlan CourseType = "inplan"
	CourseTypePublic CourseType = "public"
)

type Course struct {
	LessonID     string `json:"jx0404id"`
	EnrollID     string `json:"jx02id"`
	Code         string `json:"kch"`
	Name         string `json:"kcmc"`
	GroupName    string `json:"fzmc"`
	Credit       string `json:"xf"`
	Teacher      string `json:"skls"`
	Time         string `json:"sksj"`
	Location     string `json:"skdd"`
	Campus       string `json:"xqmc"`
	Enrolled     string `json:"xkrs"`
	Remaining    string `json:"syrs"`
	TeachMode    string `json:"skfsmc"`
	ConflictNote string `json:"ctsm"`
	Operation    string `json:"czOper"`
}

func (c *Course) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	c.LessonID = rawString(raw["jx0404id"])
	c.EnrollID = rawString(raw["jx02id"])
	c.Code = rawString(raw["kch"])
	c.Name = rawString(raw["kcmc"])
	c.GroupName = rawString(raw["fzmc"])
	c.Credit = rawString(raw["xf"])
	c.Teacher = rawString(raw["skls"])
	c.Time = rawString(raw["sksj"])
	c.Location = rawString(raw["skdd"])
	c.Campus = rawString(raw["xqmc"])
	c.Enrolled = rawString(raw["xkrs"])
	c.Remaining = rawString(raw["syrs"])
	c.TeachMode = rawString(raw["skfsmc"])
	c.ConflictNote = rawString(raw["ctsm"])
	c.Operation = rawString(raw["czOper"])

	return nil
}

// DefaultPageSize is the page size the browser uses, and the one the polling
// loop keeps: ten results per search is plenty when the list is already sorted
// by relevance.
const DefaultPageSize = 10

type SearchOptions struct {
	Keyword        string
	Filters        map[string]string
	PublicCategory *int
	// Start is the row offset (DataTables iDisplayStart) and PageSize the
	// rows per request (iDisplayLength). They exist so the catalog fetch can
	// page through a whole list; the polling loop leaves both at zero and
	// gets the historical first-page-of-ten behaviour.
	Start    int
	PageSize int
}

// SearchResult is one page of search results plus the server's row counts.
type SearchResult struct {
	Courses []Course
	// Total is iTotalRecords and Filtered is iTotalDisplayRecords: how many
	// rows exist, and how many match the current filters. Paging loops use
	// Filtered to know when to stop.
	Total    int
	Filtered int
}

type searchResponse struct {
	Data     []Course        `json:"aaData"`
	Total    json.RawMessage `json:"iTotalRecords"`
	Filtered json.RawMessage `json:"iTotalDisplayRecords"`
}

type enrollResponse struct {
	Success []bool `json:"success"`
	Message string `json:"message"`
}

func InitializeSession(ctx context.Context, client *httpclient.Client, xkid string) error {
	if strings.TrimSpace(xkid) == "" {
		return fmt.Errorf("xkid is empty")
	}

	initURL := BaseURL + EndpointEnrollmentInit
	if _, err := client.GetWithParams(ctx, initURL, url.Values{"jx0502zbid": {xkid}}); err != nil {
		return fmt.Errorf("initialize enrollment page: %w", err)
	}

	bottomURL := BaseURL + EndpointSelectBottom
	params := url.Values{
		"jx0502zbid": {xkid},
		"sfylxkstr":  {""},
	}
	if _, err := client.GetWithParams(ctx, bottomURL, params); err != nil {
		return fmt.Errorf("initialize enrollment bottom frame: %w", err)
	}

	return nil
}

// SearchCourses runs one search and returns a single page of results.
//
// Note the asymmetry between the two course types: a public-elective search
// with an empty keyword lists the whole catalog, while an in-plan search with
// an empty keyword returns nothing at all. Callers that want a complete list
// can therefore only build one for public electives; the in-plan catalog has to
// be accumulated from whatever keywords are actually searched.
func SearchCourses(ctx context.Context, client *httpclient.Client, courseType CourseType, options SearchOptions) (SearchResult, error) {
	endpoint, err := searchEndpoint(courseType)
	if err != nil {
		return SearchResult{}, err
	}

	body, err := client.PostFormWithParams(
		ctx,
		BaseURL+endpoint,
		buildSearchParams(courseType, options.Keyword, options.Filters, options.PublicCategory),
		buildDataTablePayload(options.Start, options.PageSize),
	)
	if err != nil {
		return SearchResult{}, fmt.Errorf("search courses: %w", err)
	}
	if IsSessionExpiredResponse(body) {
		return SearchResult{}, ErrSessionExpired
	}

	var resp searchResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		return SearchResult{}, fmt.Errorf("parse search response: %w", err)
	}

	return SearchResult{
		Courses:  resp.Data,
		Total:    rawInt(resp.Total),
		Filtered: rawInt(resp.Filtered),
	}, nil
}

func EnrollCourse(ctx context.Context, client *httpclient.Client, courseType CourseType, lessonID string, enrollID string) (bool, error) {
	endpoint, err := enrollEndpoint(courseType)
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(lessonID) == "" {
		return false, fmt.Errorf("lessonID is empty")
	}
	if strings.TrimSpace(enrollID) == "" {
		return false, fmt.Errorf("enrollID is empty")
	}

	params := url.Values{
		"kcid":     {enrollID},
		"cfbs":     {"null"},
		"jx0404id": {lessonID},
		"xkzy":     {""},
		"trjf":     {""},
	}

	body, err := client.GetWithParams(ctx, BaseURL+endpoint, params)
	if err != nil {
		return false, fmt.Errorf("enroll course: %w", err)
	}
	if IsSessionExpiredResponse(body) {
		return false, ErrSessionExpired
	}

	return ParseEnrollResult(body), nil
}

func IsSessionExpiredResponse(body string) bool {
	normalized := strings.TrimSpace(strings.ToLower(body))
	return normalized == "undefined" ||
		strings.Contains(body, RateLimitIndicator)
}

func ParseEnrollResult(body string) bool {
	var resp enrollResponse
	if err := json.Unmarshal([]byte(body), &resp); err == nil {
		for _, success := range resp.Success {
			if success {
				return true
			}
		}
		return strings.Contains(resp.Message, "成功")
	}

	return strings.Contains(body, "成功")
}

func CleanHTMLBreaks(value string) string {
	value = strings.ReplaceAll(value, "<br/>", " ")
	value = strings.ReplaceAll(value, "<br />", " ")
	value = strings.ReplaceAll(value, "<br>", " ")
	return strings.Join(strings.Fields(value), " ")
}

// rawInt reads a count that the server may send as a number or a string.
func rawInt(raw json.RawMessage) int {
	value, err := strconv.Atoi(strings.TrimSpace(rawString(raw)))
	if err != nil {
		return 0
	}
	return value
}

func rawString(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}

	var str string
	if err := json.Unmarshal(raw, &str); err == nil {
		return str
	}

	var number json.Number
	if err := json.Unmarshal(raw, &number); err == nil {
		return number.String()
	}

	var boolean bool
	if err := json.Unmarshal(raw, &boolean); err == nil {
		if boolean {
			return "true"
		}
		return "false"
	}

	return string(raw)
}

func buildSearchParams(courseType CourseType, keyword string, filters map[string]string, publicCategory *int) url.Values {
	params := url.Values{
		"kcxx":  {keyword},
		"skls":  {""},
		"skxq":  {""},
		"skjc":  {""},
		"endJc": {""},
		"sfym":  {"true"},
		"sfct":  {"true"},
		"sfxx":  {"true"},
		"skfs":  {""},
		"kkdw":  {""},
		"kcxz":  {""},
	}

	if courseType == CourseTypePublic {
		params.Set("sfym", "true")
		params.Set("szjylb", "")
		params.Set("kcxz", "")
		if publicCategory != nil {
			params.Set("szjylb", strconv.Itoa(*publicCategory))
		}
	}

	for key, value := range filters {
		params.Set(key, value)
	}

	return params
}

func buildDataTablePayload(start, pageSize int) url.Values {
	if pageSize <= 0 {
		pageSize = DefaultPageSize
	}
	if start < 0 {
		start = 0
	}

	payload := url.Values{
		"sEcho":          {"1"},
		"iColumns":       {"14"},
		"sColumns":       {""},
		"iDisplayStart":  {strconv.Itoa(start)},
		"iDisplayLength": {strconv.Itoa(pageSize)},
	}

	columnMappings := map[string]string{
		"mDataProp_0":  "jx0404id",
		"mDataProp_1":  "kch",
		"mDataProp_2":  "kcmc",
		"mDataProp_3":  "fzmc",
		"mDataProp_4":  "xf",
		"mDataProp_5":  "skls",
		"mDataProp_6":  "sksj",
		"mDataProp_7":  "skdd",
		"mDataProp_8":  "xqmc",
		"mDataProp_9":  "xkrs",
		"mDataProp_10": "syrs",
		"mDataProp_11": "skfsmc",
		"mDataProp_12": "ctsm",
		"mDataProp_13": "czOper",
	}
	for column, prop := range columnMappings {
		payload.Set(column, prop)
	}

	return payload
}

func searchEndpoint(courseType CourseType) (string, error) {
	switch courseType {
	case CourseTypeInPlan:
		return EndpointInPlanSearch, nil
	case CourseTypePublic:
		return EndpointPublicSearch, nil
	default:
		return "", fmt.Errorf("unsupported course type %q", courseType)
	}
}

func enrollEndpoint(courseType CourseType) (string, error) {
	switch courseType {
	case CourseTypeInPlan:
		return EndpointInPlanEnroll, nil
	case CourseTypePublic:
		return EndpointPublicEnroll, nil
	default:
		return "", fmt.Errorf("unsupported course type %q", courseType)
	}
}
