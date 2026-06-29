package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/datayurei/robyou/enrollment"
	"github.com/datayurei/robyou/httpclient"
	"github.com/datayurei/robyou/parser"
)

type loginInfo struct {
	username string
	password string
}

type secretConfig struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type enrollConfig struct {
	IntervalSeconds float64        `json:"interval_seconds"`
	Courses         []courseTarget `json:"courses"`
}

type courseTarget struct {
	Name                string            `json:"name"`
	Type                string            `json:"type"`
	Keyword             string            `json:"keyword"`
	Enabled             bool              `json:"enabled"`
	Filters             map[string]string `json:"filters,omitempty"`
	FuzzyFilterKeywords []string          `json:"fuzzy_filter_keywords,omitempty"`
	ExactFilterKeywords []string          `json:"exact_filter_keywords,omitempty"`
	RequestDelaySeconds float64           `json:"request_delay_seconds,omitempty"`
	// By default, one successful enrollment completes only this target.
	// Set this when the same target should keep polling for additional sections.
	ContinueAfterSuccessful bool `json:"continue_after_successful,omitempty"`
}

// build a global http client
var globalClient *httpclient.Client

var cookieCacheURLs = []string{
	"https://sso.stu.edu.cn/",
	"https://jw.stu.edu.cn/",
}

func main() {
	globalClient = httpclient.New()

	config, err := loadEnrollConfig("enroll_config.json")
	if err != nil {
		fmt.Println(err)
		return
	}

	if err := globalClient.LoadCookies("cookies.json", cookieCacheURLs); err != nil {
		fmt.Println("load cookies:", err)
	}

	xkURL, ok := getXklc(globalClient)
	if ok {
		fmt.Println("Login success with cached cookies")
	} else {
		info, err := loadLoginInfo("secret.json")
		if err != nil {
			fmt.Println(err)
			return
		}

		// login with account and passwrod
		if _, err := login(globalClient, info); err != nil {
			fmt.Println(err)
			return
		}

		xkURL, ok = getXklc(globalClient)
		if !ok {
			fmt.Println("failed to find course selection link")
			return
		}
		if err := globalClient.SaveCookies("cookies.json", cookieCacheURLs); err != nil {
			fmt.Println("save cookies:", err)
		}
		fmt.Println("Login success")
	}
	// shareCookiesAcrossSubdomains(globalClient)

	// fmt.Println(xkURL)
	// fmt.Println(parser.CheckLoginStatus(globalClient))

	xkid, err := initEnrollment(globalClient, xkURL)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println("xkid:", xkid)

	runEnrollmentLoop(globalClient, config)

}

// login function will login the client by loginInfo on sso.stu.edu.cn
func login(client *httpclient.Client, info loginInfo) (bool, error) {
	loginURL := "https://sso.stu.edu.cn/login?service=http%3A%2F%2Fjw.stu.edu.cn%2F"

	html, _ := client.GetString(loginURL)

	lt, _ := parser.ExtractLtFromLogin(html)

	loginData := url.Values{
		"username":  {info.username},
		"password":  {info.password},
		"lt":        {lt},
		"execution": {"e1s1"},
		"_eventId":  {"submit"},
	}

	client.PostFormString(loginURL, loginData)

	isLogin := parser.CheckLoginStatus(client)
	if !isLogin {
		return false, fmt.Errorf("auth failed, check your account and password")
	}

	return true, nil

}

// get courses selecton round
func getXklc(client *httpclient.Client) (string, bool) {
	resp, _ := client.GetString("https://jw.stu.edu.cn/jsxsd/framework/xsrkxz.htmlx")
	URL, _ := parser.ExtractXklc(resp)
	return URL, URL != ""
}

func initEnrollment(client *httpclient.Client, xklcURL string) (string, error) {
	resp, err := client.GetString(xklcURL)
	if err != nil {
		return "", fmt.Errorf("open course selection round: %w", err)
	}

	xkid, ok := parser.ExtractXkid(resp)
	if !ok {
		return "", fmt.Errorf("xkid not found in course selection round page")
	}

	if err := enrollment.InitializeSession(client, xkid); err != nil {
		return "", err
	}

	return xkid, nil
}

func isFiltered(course enrollment.Course, fuzzyKeywords []string, exactKeywords []string) bool {
	courseName := strings.ToLower(strings.TrimSpace(course.Name))
	teacher := strings.ToLower(strings.TrimSpace(course.Teacher))

	for _, keyword := range exactKeywords {
		keyword = strings.ToLower(strings.TrimSpace(keyword))
		if keyword != "" && (keyword == courseName || keyword == teacher) {
			return true
		}
	}

	for _, keyword := range fuzzyKeywords {
		keyword = strings.ToLower(strings.TrimSpace(keyword))
		if keyword != "" && (strings.Contains(courseName, keyword) || strings.Contains(teacher, keyword)) {
			return true
		}
	}

	return false
}

func runEnrollmentLoop(client *httpclient.Client, config enrollConfig) {
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(stop)

	interval := durationSeconds(config.IntervalSeconds, 3*time.Second)
	completedTargets := make(map[int]bool)
	fmt.Printf("start polling %d course target(s), interval: %s\n", len(config.Courses), interval)

	round := 1
	for {
		select {
		case <-stop:
			fmt.Println("stop requested")
			return
		default:
		}

		fmt.Printf("round %d\n", round)
		for targetIndex, target := range config.Courses {
			select {
			case <-stop:
				fmt.Println("stop requested")
				return
			default:
			}

			if !target.Enabled || completedTargets[targetIndex] {
				continue
			}

			courseType, err := parseCourseType(target.Type)
			if err != nil {
				fmt.Printf("skip target %q: %v\n", target.Name, err)
				continue
			}

			label := target.Name
			if label == "" {
				label = target.Keyword
			}
			fmt.Printf("search target: %s [%s] keyword=%q\n", label, courseType, target.Keyword)

			courses, err := enrollment.SearchCourses(client, courseType, enrollment.SearchOptions{
				Keyword: target.Keyword,
				Filters: target.Filters,
			})
			if err != nil {
				fmt.Println("search failed:", err)
				continue
			}
			if len(courses) == 0 {
				fmt.Println("no course found")
				continue
			}

			for _, course := range courses {
				select {
				case <-stop:
					fmt.Println("stop requested")
					return
				default:
				}

				courseInfo := formatCourseInfo(course)
				if isFiltered(course, target.FuzzyFilterKeywords, target.ExactFilterKeywords) {
					fmt.Println("skip:", courseInfo)
					continue
				}
				if course.LessonID == "" || course.EnrollID == "" {
					fmt.Println("missing ids:", courseInfo)
					continue
				}

				fmt.Println("try:", courseInfo)
				ok, err := enrollment.EnrollCourse(client, courseType, course.LessonID, course.EnrollID)
				if err != nil {
					fmt.Println("enroll failed:", err)
				} else if ok {
					fmt.Println("enroll success:", courseInfo)
					if !target.ContinueAfterSuccessful {
						completedTargets[targetIndex] = true
						fmt.Printf("target %q completed, continuing other targets\n", label)
						break
					}
				} else {
					fmt.Println("enroll rejected:", courseInfo)
				}

				requestDelay := durationSeconds(target.RequestDelaySeconds, 500*time.Millisecond)
				if !sleepOrStop(requestDelay, stop) {
					return
				}
			}
		}

		if allEnabledTargetsCompleted(config.Courses, completedTargets) {
			fmt.Println("all enabled course targets completed")
			return
		}

		round++
		if !sleepOrStop(interval, stop) {
			return
		}
	}
}

func allEnabledTargetsCompleted(targets []courseTarget, completed map[int]bool) bool {
	for i, target := range targets {
		if target.Enabled && !completed[i] {
			return false
		}
	}
	return true
}

func formatCourseInfo(course enrollment.Course) string {
	return fmt.Sprintf(
		"%s - %s (%s) [已选:%s/剩余:%s]",
		course.Name,
		course.Teacher,
		enrollment.CleanHTMLBreaks(course.Time),
		course.Enrolled,
		course.Remaining,
	)
}

func sleepOrStop(duration time.Duration, stop <-chan os.Signal) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()

	select {
	case <-stop:
		fmt.Println("stop requested")
		return false
	case <-timer.C:
		return true
	}
}

func durationSeconds(seconds float64, fallback time.Duration) time.Duration {
	if seconds <= 0 {
		return fallback
	}
	return time.Duration(seconds * float64(time.Second))
}

func parseCourseType(value string) (enrollment.CourseType, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "inplan":
		return enrollment.CourseTypeInPlan, nil
	case "public":
		return enrollment.CourseTypePublic, nil
	default:
		return "", fmt.Errorf("course type must be inplan or public, got %q", value)
	}
}

func loadEnrollConfig(path string) (enrollConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			template := enrollConfig{
				IntervalSeconds: 3,
				Courses: []courseTarget{
					{
						Name:                    "示例计划内课程",
						Type:                    "inplan",
						Keyword:                 "课程关键词",
						Enabled:                 true,
						FuzzyFilterKeywords:     []string{"不想要的教师或课程片段"},
						ExactFilterKeywords:     []string{"精确排除的教师或课程名"},
						RequestDelaySeconds:     0.5,
						ContinueAfterSuccessful: false,
					},
					{
						Name:                    "示例公选课",
						Type:                    "public",
						Keyword:                 "公选课关键词",
						Enabled:                 false,
						RequestDelaySeconds:     0.5,
						ContinueAfterSuccessful: false,
					},
				},
			}
			content, marshalErr := json.MarshalIndent(template, "", "  ")
			if marshalErr != nil {
				return enrollConfig{}, fmt.Errorf("build enroll config template: %w", marshalErr)
			}
			content = append(content, '\n')
			if writeErr := os.WriteFile(path, content, 0600); writeErr != nil {
				return enrollConfig{}, fmt.Errorf("create %s: %w", path, writeErr)
			}
			return enrollConfig{}, fmt.Errorf("%s created, please edit course targets before running", path)
		}
		return enrollConfig{}, fmt.Errorf("read %s: %w", path, err)
	}

	var config enrollConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return enrollConfig{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if config.IntervalSeconds <= 0 {
		config.IntervalSeconds = 3
	}

	enabledCount := 0
	for i, target := range config.Courses {
		if _, err := parseCourseType(target.Type); err != nil {
			return enrollConfig{}, fmt.Errorf("courses[%d]: %w", i, err)
		}
		if target.Enabled {
			enabledCount++
		}
	}
	if len(config.Courses) == 0 {
		return enrollConfig{}, fmt.Errorf("%s has no course targets", path)
	}
	if enabledCount == 0 {
		return enrollConfig{}, fmt.Errorf("%s has no enabled course targets", path)
	}

	return config, nil
}

func loadLoginInfo(path string) (loginInfo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			template := secretConfig{
				Username: "",
				Password: "",
			}
			content, marshalErr := json.MarshalIndent(template, "", "  ")
			if marshalErr != nil {
				return loginInfo{}, fmt.Errorf("build secret template: %w", marshalErr)
			}
			content = append(content, '\n')
			if writeErr := os.WriteFile(path, content, 0600); writeErr != nil {
				return loginInfo{}, fmt.Errorf("create %s: %w", path, writeErr)
			}
			return loginInfo{}, fmt.Errorf("%s created, please fill username and password", path)
		}
		return loginInfo{}, fmt.Errorf("read %s: %w", path, err)
	}

	var config secretConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return loginInfo{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if strings.TrimSpace(config.Username) == "" || strings.TrimSpace(config.Password) == "" {
		return loginInfo{}, fmt.Errorf("%s is missing username or password", path)
	}

	return loginInfo{
		username: config.Username,
		password: config.Password,
	}, nil
}
