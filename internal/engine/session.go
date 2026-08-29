// Package engine drives the enrollment work: it owns the login session and
// runs the configured jobs.
package engine

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/datayurei/robyou/enrollment"
	"github.com/datayurei/robyou/httpclient"
	"github.com/datayurei/robyou/parser"
)

const (
	ssoLoginURL = "https://sso.stu.edu.cn/login?service=http%3A%2F%2Fjw.stu.edu.cn%2F"
	portalURL   = "https://jw.stu.edu.cn/jsxsd/framework/xsrkxz.htmlx"
)

// Session is one authenticated connection to the teaching-management system.
type Session struct {
	client *httpclient.Client
	xkid   string
}

// NewSession wraps a client that has not logged in yet.
func NewSession(client *httpclient.Client) *Session {
	return &Session{client: client}
}

// Client exposes the underlying HTTP client.
func (s *Session) Client() *httpclient.Client { return s.client }

// Xkid is the course-selection round ID discovered by Bootstrap.
func (s *Session) Xkid() string { return s.xkid }

// Login authenticates against CAS SSO. The cookie jar is reset first so a
// retry never carries a half-finished session forward.
func (s *Session) Login(ctx context.Context, username, password string) error {
	if strings.TrimSpace(username) == "" || strings.TrimSpace(password) == "" {
		return fmt.Errorf("用户名或密码为空 (username or password is empty)")
	}

	s.client.ResetSession()

	page, err := s.client.Get(ctx, ssoLoginURL)
	if err != nil {
		return fmt.Errorf("打开登录页失败 (open login page): %w", err)
	}

	lt, ok := parser.ExtractLtFromLogin(page)
	if !ok {
		return fmt.Errorf("登录票据 lt 解析失败 (lt not found on login page)")
	}

	form := url.Values{
		"username":  {username},
		"password":  {password},
		"lt":        {lt},
		"execution": {"e1s1"},
		"_eventId":  {"submit"},
	}
	if _, err := s.client.PostForm(ctx, ssoLoginURL, form); err != nil {
		return fmt.Errorf("提交登录表单失败 (submit login form): %w", err)
	}

	if !parser.CheckLoginStatus(ctx, s.client) {
		return fmt.Errorf("登录失败，请检查账号和密码 (auth failed, check your account and password)")
	}

	return nil
}

// Alive reports whether the SSO session is still valid.
func (s *Session) Alive(ctx context.Context) bool {
	return parser.CheckLoginStatus(ctx, s.client)
}

// Bootstrap walks the portal to the active round and initializes the
// enrollment workspace, which the search and enroll endpoints require.
func (s *Session) Bootstrap(ctx context.Context) (string, error) {
	portal, err := s.client.Get(ctx, portalURL)
	if err != nil {
		return "", fmt.Errorf("打开教务首页失败 (open portal): %w", err)
	}

	roundURL, ok := parser.ExtractXklc(portal)
	if !ok {
		return "", fmt.Errorf("未找到选课入口，可能当前没有开放的选课轮次 (no course-selection round link found)")
	}

	roundPage, err := s.client.Get(ctx, roundURL)
	if err != nil {
		return "", fmt.Errorf("打开选课轮次失败 (open course-selection round): %w", err)
	}

	xkid, ok := parser.ExtractXkid(roundPage)
	if !ok {
		return "", fmt.Errorf("选课轮次页面中未找到 xkid (xkid not found)")
	}

	if err := enrollment.InitializeSession(ctx, s.client, xkid); err != nil {
		return "", err
	}

	s.xkid = xkid
	return xkid, nil
}
