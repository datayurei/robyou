// Package engine drives the enrollment work: it owns the login session and
// runs the configured jobs.
package engine

import (
	"context"
	"errors"
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

// ErrRoundNotOpen means the login worked but no course-selection round is
// available yet: the portal carries no xklc_list link, or that list is empty.
// This is the normal state before enrollment opens, so callers wait and retry
// rather than treating it as a failure.
var ErrRoundNotOpen = errors.New("选课尚未开放 (no course-selection round is open)")

// Session is one authenticated connection to the teaching-management system.
type Session struct {
	client *httpclient.Client
	xkid   string

	// Endpoints are fields rather than constants so tests can point the
	// bootstrap walk at a stub server.
	portalURL string
	baseURL   string
}

// NewSession wraps a client that has not logged in yet.
func NewSession(client *httpclient.Client) *Session {
	return &Session{
		client:    client,
		portalURL: portalURL,
		baseURL:   parser.JWBaseURL,
	}
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
//
// Failures are classified, because they mean very different things: an empty
// or missing round list is ErrRoundNotOpen (wait for enrollment to start),
// being served the login form is enrollment.ErrSessionExpired (log in again),
// and anything else is a transport or server problem.
func (s *Session) Bootstrap(ctx context.Context) (string, error) {
	portal, err := s.client.Get(ctx, s.portalURL)
	if err != nil {
		return "", fmt.Errorf("打开教务首页失败 (open portal): %w", err)
	}
	if parser.IsLoginPage(portal) {
		return "", fmt.Errorf("打开教务首页时被跳转到登录页: %w", enrollment.ErrSessionExpired)
	}

	// roundURL, ok := parser.ExtractXklcWithBase(portal, s.baseURL)
	// if !ok {
	// 	return "", fmt.Errorf("%w: 教务首页没有选课入口链接 (no xklc_list link on the portal)", ErrRoundNotOpen)
	// }
	roundURL := "https://jw.stu.edu.cn/jsxsd/xsxk/xklc_list"

	roundPage, err := s.client.Get(ctx, roundURL)
	if err != nil {
		return "", fmt.Errorf("打开选课轮次列表失败 (open xklc_list): %w", err)
	}
	if parser.IsLoginPage(roundPage) {
		return "", fmt.Errorf("打开选课轮次列表时被跳转到登录页: %w", enrollment.ErrSessionExpired)
	}

	xkid, ok := parser.ExtractXkid(roundPage)
	if !ok {
		return "", fmt.Errorf("%w: 选课轮次列表中没有可选的轮次 (xklc_list has no round)", ErrRoundNotOpen)
	}

	if err := enrollment.InitializeSession(ctx, s.client, xkid); err != nil {
		return "", err
	}

	s.xkid = xkid
	return xkid, nil
}
