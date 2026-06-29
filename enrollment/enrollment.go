package enrollment

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/datayurei/robyou/httpclient"
)

const (
	BaseURL = "https://jw.stu.edu.cn"

	EndpointEnrollmentInit = "/jsxsd/xsxk/newXsxkzx"
	EndpointSelectBottom   = "/jsxsd/xsxk/selectBottom"
	EndpointInPlanSearch   = "/jsxsd/xsxkkc/xsxkBxqjhxk"
	EndpointPublicSearch   = "/jsxsd/xsxkkc/xsxkGgxxkxk"
	EndpointInPlanEnroll   = "/jsxsd/xsxkkc/bxqjhxkOper"
	EndpointPublicEnroll   = "/jsxsd/xsxkkc/ggxxkxkOper"

	RateLimitIndicator = "注销"
)

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

type SearchOptions struct {
	Keyword string
	Filters map[string]string
}

type searchResponse struct {
	Data []Course `json:"aaData"`
}

type enrollResponse struct {
	Success []bool `json:"success"`
	Message string `json:"message"`
}

func InitializeSession(client *httpclient.Client, xkid string) error {
	if strings.TrimSpace(xkid) == "" {
		return fmt.Errorf("xkid is empty")
	}

	initURL := BaseURL + EndpointEnrollmentInit
	if _, err := client.GetStringWithParams(initURL, url.Values{"jx0502zbid": {xkid}}); err != nil {
		return fmt.Errorf("initialize enrollment page: %w", err)
	}

	bottomURL := BaseURL + EndpointSelectBottom
	params := url.Values{
		"jx0502zbid": {xkid},
		"sfylxkstr":  {""},
	}
	if _, err := client.GetStringWithParams(bottomURL, params); err != nil {
		return fmt.Errorf("initialize enrollment bottom frame: %w", err)
	}

	return nil
}

func SearchCourses(client *httpclient.Client, courseType CourseType, options SearchOptions) ([]Course, error) {
	endpoint, err := searchEndpoint(courseType)
	if err != nil {
		return nil, err
	}

	body, err := client.PostFormStringWithParams(
		BaseURL+endpoint,
		buildSearchParams(courseType, options.Keyword, options.Filters),
		buildDataTablePayload(),
	)
	if err != nil {
		return nil, fmt.Errorf("search courses: %w", err)
	}
	if strings.Contains(body, RateLimitIndicator) {
		return nil, fmt.Errorf("search courses rejected: session appears logged out or rate limited")
	}

	var resp searchResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		return nil, fmt.Errorf("parse search response: %w", err)
	}

	return resp.Data, nil
}

func EnrollCourse(client *httpclient.Client, courseType CourseType, lessonID string, enrollID string) (bool, error) {
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

	body, err := client.GetStringWithParams(BaseURL+endpoint, params)
	if err != nil {
		return false, fmt.Errorf("enroll course: %w", err)
	}

	return ParseEnrollResult(body), nil
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

func buildSearchParams(courseType CourseType, keyword string, filters map[string]string) url.Values {
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
		params.Set("szjylb", "1")
		params.Set("kcxz", "")
	}

	for key, value := range filters {
		params.Set(key, value)
	}

	return params
}

func buildDataTablePayload() url.Values {
	payload := url.Values{
		"sEcho":          {"1"},
		"iColumns":       {"14"},
		"sColumns":       {""},
		"iDisplayStart":  {"0"},
		"iDisplayLength": {"10"},
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
