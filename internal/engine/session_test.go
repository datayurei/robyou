package engine

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/datayurei/robyou/enrollment"
	"github.com/datayurei/robyou/httpclient"
	"github.com/datayurei/robyou/internal/ratelimit"
)

const (
	loginPageHTML = `<html><body><form id="fm1">
		<input type="text" name="username"/><input type="password" name="password"/>
		<input type="hidden" name="lt" value="LT-123-abc"/>
		<input type="hidden" name="_eventId" value="submit"/>
	</form></body></html>`

	// The portal before enrollment opens: a normal page, no round link.
	portalClosedHTML = `<html><body><a href="/jsxsd/framework/xsMain.jsp">首页</a>
		<a href="/jsxsd/xsxx/xsxxxx">学生信息</a></body></html>`

	portalOpenHTML = `<html><body>
		<a href="/jsxsd/xsxk/xklc_list?Ves632DSdyV=NEW_XSD_PYGL">学生选课</a></body></html>`

	// The round list with no round in it — enrollment configured but not started.
	roundListEmptyHTML = `<html><body><table id="tbList"><tbody></tbody></table></body></html>`
)

// newTestSession points a session's bootstrap walk at a stub server.
func newTestSession(t *testing.T, handler http.HandlerFunc) (*Session, *httptest.Server) {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	client := httpclient.New(httpclient.WithLimiter(ratelimit.New(ratelimit.Unlimited)))
	session := NewSession(client)
	session.portalURL = server.URL + "/jsxsd/framework/xsrkxz.htmlx"
	session.baseURL = server.URL + "/"

	return session, server
}

func TestBootstrapReportsRoundNotOpen(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{
			name: "portal has no round link",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(portalClosedHTML))
			},
		},
		{
			name: "round list is empty",
			handler: func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "xklc_list") {
					w.Write([]byte(roundListEmptyHTML))
					return
				}
				w.Write([]byte(portalOpenHTML))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			session, _ := newTestSession(t, tt.handler)

			_, err := session.Bootstrap(context.Background())
			if !errors.Is(err, ErrRoundNotOpen) {
				t.Fatalf("Bootstrap() = %v, want ErrRoundNotOpen", err)
			}
			if errors.Is(err, enrollment.ErrSessionExpired) {
				t.Fatal("a closed round must not be reported as an expired session")
			}
			if session.Xkid() != "" {
				t.Fatalf("xkid = %q, want empty", session.Xkid())
			}
		})
	}
}

func TestBootstrapReportsSessionExpired(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{
			name: "portal redirects to login",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(loginPageHTML))
			},
		},
		{
			name: "round list redirects to login",
			handler: func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "xklc_list") {
					w.Write([]byte(loginPageHTML))
					return
				}
				w.Write([]byte(portalOpenHTML))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			session, _ := newTestSession(t, tt.handler)

			_, err := session.Bootstrap(context.Background())
			if !errors.Is(err, enrollment.ErrSessionExpired) {
				t.Fatalf("Bootstrap() = %v, want ErrSessionExpired", err)
			}
			if errors.Is(err, ErrRoundNotOpen) {
				t.Fatal("an expired session must not be reported as a closed round")
			}
		})
	}
}

func TestBootstrapTransportErrorIsNeitherSentinel(t *testing.T) {
	session, server := newTestSession(t, func(w http.ResponseWriter, r *http.Request) {})
	server.Close()

	_, err := session.Bootstrap(context.Background())
	if err == nil {
		t.Fatal("Bootstrap() against a closed server should fail")
	}
	if errors.Is(err, ErrRoundNotOpen) || errors.Is(err, enrollment.ErrSessionExpired) {
		t.Fatalf("transport failure misclassified: %v", err)
	}
}
