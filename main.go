package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
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

// build a global http client
var globalClient *httpclient.Client

func main() {
	globalClient = httpclient.New()

	info, err := loadLoginInfo("secret.json")
	if err != nil {
		fmt.Println(err)
		return
	}
	courseType := enrollment.CourseTypeInPlan
	keyword := ""
	filterKeywords := []string{}

	// login with account and passwrod
	if _, err := login(globalClient, info); err != nil {
		fmt.Println(err)
		return
	}
	// shareCookiesAcrossSubdomains(globalClient)

	xkURL, ok := getXklc(globalClient)
	if !ok {
		fmt.Println("failed to find course selection link")
		return
	}

	fmt.Println(xkURL)
	fmt.Println(parser.CheckLoginStatus(globalClient))

	xkid, err := initEnrollment(globalClient, xkURL)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println("xkid:", xkid)

	courses, err := enrollment.SearchCourses(globalClient, courseType, enrollment.SearchOptions{
		Keyword: keyword,
	})
	if err != nil {
		fmt.Println(err)
		return
	}

	for _, course := range courses {
		courseInfo := fmt.Sprintf(
			"%s - %s (%s) [已选:%s/剩余:%s]",
			course.Name,
			course.Teacher,
			enrollment.CleanHTMLBreaks(course.Time),
			course.Enrolled,
			course.Remaining,
		)

		if isFiltered(course, filterKeywords, nil) {
			fmt.Println("skip:", courseInfo)
			continue
		}
		if course.LessonID == "" || course.EnrollID == "" {
			fmt.Println("missing ids:", courseInfo)
			continue
		}

		fmt.Println("try:", courseInfo)
		ok, err := enrollment.EnrollCourse(globalClient, courseType, course.LessonID, course.EnrollID)
		if err != nil {
			fmt.Println("enroll failed:", err)
			continue
		}
		if ok {
			fmt.Println("enroll success:", courseInfo)
		} else {
			fmt.Println("enroll rejected:", courseInfo)
		}

		time.Sleep(500 * time.Millisecond)
	}

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
