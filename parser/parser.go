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

// IsLoginPage reports whether body is the CAS login form rather than a page
// from the teaching-management system. Landing here means the session is gone:
// CAS serves this form to unauthenticated visitors, so a portal or round-list
// request that returns it has to be told apart from one that simply found no
// open enrollment round.
func IsLoginPage(body string) bool {
	if _, ok := ExtractLtFromLogin(body); ok {
		return true
	}
	return strings.Contains(body, `name="_eventId"`) && strings.Contains(body, `name="password"`)
}

// JWBaseURL is the base the portal's relative links resolve against.
const JWBaseURL = "https://jw.stu.edu.cn/"

// ExtractXklc finds the course-selection round list link on the portal page.
// Before enrollment opens the portal carries no such link, so a false result
// is the normal "not open yet" signal rather than a parse failure.
func ExtractXklc(body string) (string, bool) {
	return ExtractXklcWithBase(body, JWBaseURL)
}

// ExtractXklcWithBase is ExtractXklc against an arbitrary base URL.
func ExtractXklcWithBase(body string, base string) (string, bool) {
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

	baseURL, err := url.Parse(base)
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
