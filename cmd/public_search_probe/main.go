package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/datayurei/robyou/enrollment"
	"github.com/datayurei/robyou/httpclient"
	"github.com/datayurei/robyou/parser"
)

const (
	jwURL = "https://jw.stu.edu.cn/"
)

type searchResponse struct {
	Data []map[string]any `json:"aaData"`
}

type secretConfig struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func main() {
	var keyword string
	var secretPath string
	var printCurl bool
	var raw bool

	flag.StringVar(&keyword, "keyword", "", "public course search keyword")
	flag.StringVar(&secretPath, "secret", "secret.json", "credential file path")
	flag.BoolVar(&printCurl, "print-curl", false, "print a replayable curl command with live cookies")
	flag.BoolVar(&raw, "raw", false, "print the full raw response body")
	flag.Parse()

	client := httpclient.New()
	if err := loginWithSecret(client, secretPath); err != nil {
		failf("login: %v", err)
	}

	fmt.Println("login status: ok")
	printCookieSummary(client)

	body, err := client.GetString(enrollment.BaseURL + enrollment.EndpointEnrollmentSession)
	if err != nil {
		failf("open enrollment session page: %v", err)
	}
	xkid, ok := parser.ExtractXkid(body)
	if !ok {
		failf("extract xkid: not found")
	}
	fmt.Printf("xkid: %s\n", xkid)

	if err := enrollment.InitializeSession(client, xkid); err != nil {
		failf("initialize enrollment session: %v", err)
	}
	fmt.Println("enrollment session initialized")

	query := buildPublicSearchQuery(keyword)
	form := buildDataTablePayload()

	requestURL, err := withQuery(enrollment.BaseURL+enrollment.EndpointPublicSearch, query)
	if err != nil {
		failf("build public search URL: %v", err)
	}

	if printCurl {
		fmt.Println("curl:")
		fmt.Println(buildCurlCommand(client, requestURL, form))
	}

	req, err := http.NewRequest(http.MethodPost, requestURL, strings.NewReader(form.Encode()))
	if err != nil {
		failf("build request: %v", err)
	}
	setHeaders(req, xkid)

	resp, err := client.Do(req)
	if err != nil {
		failf("send request: %v", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		failf("read response: %v", err)
	}

	fmt.Printf("status: %s\n", resp.Status)
	fmt.Printf("request url: %s\n", requestURL)
	fmt.Printf("query: %s\n", query.Encode())
	fmt.Printf("form: %s\n", form.Encode())
	fmt.Printf("response bytes: %d\n", len(respBody))

	if raw {
		fmt.Println(string(respBody))
		return
	}

	printResponseSummary(respBody)
}

func buildPublicSearchQuery(keyword string) url.Values {
	return url.Values{
		"kcxx":   {keyword},
		"skls":   {""},
		"skxq":   {""},
		"skjc":   {""},
		"endJc":  {""},
		"sfym":   {"true"},
		"sfct":   {"true"},
		"sfxx":   {"true"},
		"skfs":   {""},
		"kkdw":   {""},
		"kcxz":   {""},
		"szjylb": {""},
	}
}

func loginWithSecret(client *httpclient.Client, secretPath string) error {
	secret, err := loadSecret(secretPath)
	if err != nil {
		return err
	}

	loginURL := "https://sso.stu.edu.cn/login?service=http%3A%2F%2Fjw.stu.edu.cn%2F"
	loginPage, err := client.GetString(loginURL)
	if err != nil {
		return fmt.Errorf("open login page: %w", err)
	}

	lt, ok := parser.ExtractLtFromLogin(loginPage)
	if !ok {
		return fmt.Errorf("extract lt: not found")
	}

	loginData := url.Values{
		"username":  {secret.Username},
		"password":  {secret.Password},
		"lt":        {lt},
		"execution": {"e1s1"},
		"_eventId":  {"submit"},
	}

	if _, err := client.PostFormString(loginURL, loginData); err != nil {
		return fmt.Errorf("submit login form: %w", err)
	}
	if _, err := client.GetString(enrollment.BaseURL); err != nil {
		return fmt.Errorf("open jw home: %w", err)
	}
	if _, err := client.GetString(loginURL); err != nil {
		return fmt.Errorf("follow sso redirect: %w", err)
	}
	if _, err := client.GetString(enrollment.BaseURL + enrollment.EndpointEnrollmentSession); err != nil {
		return fmt.Errorf("open enrollment session page: %w", err)
	}

	if !parser.CheckLoginStatus(client) {
		return fmt.Errorf("login status check failed")
	}

	return nil
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
	for key, value := range columnMappings {
		payload.Set(key, value)
	}

	return payload
}

func withQuery(rawURL string, params url.Values) (string, error) {
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}

	query := parsedURL.Query()
	for key, values := range params {
		query.Del(key)
		for _, value := range values {
			query.Add(key, value)
		}
	}
	parsedURL.RawQuery = query.Encode()

	return parsedURL.String(), nil
}

func setHeaders(req *http.Request, xkid string) {
	req.Header.Set("Cache-Control", "max-age=0")
	req.Header.Set("Sec-Ch-Ua", `"Chromium";v="137", "Not/A)Brand";v="24"`)
	req.Header.Set("Sec-Ch-Ua-Mobile", "?0")
	req.Header.Set("Sec-Ch-Ua-Platform", `"Windows"`)
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	req.Header.Set("Upgrade-Insecure-Requests", "1")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/137.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-User", "?1")
	req.Header.Set("Sec-Fetch-Dest", "document")
	req.Header.Set("Priority", "u=0, i")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Origin", enrollment.BaseURL)
	req.Header.Set("Referer", fmt.Sprintf("%s%s?jx0502zbid=%s", enrollment.BaseURL, enrollment.EndpointEnrollmentInit, xkid))
}

func buildCurlCommand(client *httpclient.Client, requestURL string, form url.Values) string {
	cookieHeader := cookieHeader(client)
	return fmt.Sprintf(
		"curl '%s' -X POST -H 'Content-Type: application/x-www-form-urlencoded; charset=UTF-8' -H 'Cookie: %s' --data '%s'",
		requestURL,
		cookieHeader,
		form.Encode(),
	)
}

func cookieHeader(client *httpclient.Client) string {
	parsedURL, err := url.Parse(jwURL)
	if err != nil || client.Jar == nil {
		return ""
	}

	cookies := client.Jar.Cookies(parsedURL)
	parts := make([]string, 0, len(cookies))
	for _, cookie := range cookies {
		parts = append(parts, cookie.Name+"="+cookie.Value)
	}
	return strings.Join(parts, "; ")
}

func printCookieSummary(client *httpclient.Client) {
	for _, rawURL := range []string{jwURL} {
		parsedURL, err := url.Parse(rawURL)
		if err != nil || client.Jar == nil {
			continue
		}

		cookies := client.Jar.Cookies(parsedURL)
		names := make([]string, 0, len(cookies))
		for _, cookie := range cookies {
			names = append(names, cookie.Name)
		}
		fmt.Printf("cookies for %s: %s\n", rawURL, strings.Join(names, ", "))
	}
}

func printResponseSummary(body []byte) {
	var resp searchResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		fmt.Printf("response preview:\n%s\n", truncate(string(body), 1200))
		return
	}

	fmt.Printf("parsed courses: %d\n", len(resp.Data))
	if len(resp.Data) == 0 {
		return
	}

	first := resp.Data[0]
	fmt.Printf(
		"first course: %s | %s | teacher=%v | remaining=%v\n",
		toString(first["kcmc"]),
		toString(first["kch"]),
		first["skls"],
		first["syrs"],
	)
}

func loadSecret(path string) (secretConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return secretConfig{}, err
	}

	var secret secretConfig
	if err := json.Unmarshal(data, &secret); err != nil {
		return secretConfig{}, err
	}
	if strings.TrimSpace(secret.Username) == "" || strings.TrimSpace(secret.Password) == "" {
		return secretConfig{}, fmt.Errorf("username or password is empty")
	}

	return secret, nil
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "\n...[truncated]"
}

func toString(value any) string {
	if value == nil {
		return ""
	}
	return fmt.Sprint(value)
}

func failf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
