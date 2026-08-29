package parser

import (
	"context"
	"net/url"
	"regexp"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/datayurei/robyou/httpclient"
)

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

// CheckLoginStatus reports whether the SSO session is still alive. The login
// page renders a "您当前使用" banner only for an authenticated visitor.
func CheckLoginStatus(ctx context.Context, client *httpclient.Client) bool {
	resp, err := client.Get(ctx, "https://sso.stu.edu.cn/login")
	if err != nil {
		return false
	}
	return strings.Contains(resp, "您当前使用")
}
