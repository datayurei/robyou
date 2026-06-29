package parser

import (
	"net/url"
	"regexp"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/datayurei/robyou/httpclient"
)

// func ExtractLtFromLogin(body []byte) (string, bool) {
// 	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
//
// 	if err != nil {
// 		return "", false
// 	}
//
// 	lt, exists := doc.Find(`input[name="lt"]`).Attr("value")
// 	return lt, exists
//
// }

func ExtractLtFromLogin(body string) (string, bool) {

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(body))
	if err != nil {

		return "", false
	}
	lt, exists := doc.Find(`input[name="lt"]`).Attr("value")
	return lt, exists

}

func ExtractXklc(body string) (string, bool) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(body))
	if err != nil {
		return "", false
	}

	selector := `a[href^="/jsxsd/xsxk/xklc_list"]`
	link := doc.Find(selector).First()

	href, exists := link.Attr("href")
	if !exists || strings.TrimSpace(href) == "" {
		return "", false
	}

	baseURL, err := url.Parse("https://jw.stu.edu.cn/")
	if err != nil {
		return "", false
	}

	hrefURL, err := url.Parse(href)
	if err != nil {
		return "", false
	}

	fullURL := baseURL.ResolveReference(hrefURL).String()

	return fullURL, true
}

func ExtractXkid(body string) (string, bool) {
	re := regexp.MustCompile(`[A-F0-9]{32}`)
	xkid := re.FindString(body)
	return xkid, xkid != ""
}

func CheckLoginStatus(client *httpclient.Client) bool {
	resp, _ := client.GetString("https://sso.stu.edu.cn/login")
	return strings.Contains(resp, "您当前使用")
}
