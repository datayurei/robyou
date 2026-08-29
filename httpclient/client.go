// Package httpclient is the single outbound HTTP path of the program.
//
// It owns the cookie jar that carries the SSO and jsxsd sessions, the fixed
// browser-like header set, and the rate limiter. Every request goes through
// Client, so pacing cannot be bypassed by accident.
package httpclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/datayurei/robyou/internal/ratelimit"
)

// DefaultTimeout is the per-request timeout.
const DefaultTimeout = 10 * time.Second

// Client is an *http.Client with a cookie jar, default headers and a limiter.
type Client struct {
	*http.Client

	headers http.Header
	jar     *resettableJar

	mu      sync.RWMutex
	limiter *ratelimit.Limiter
}

// resettableJar lets a session be dropped and started over without swapping the
// jar out from under requests that are already in flight. The inner jar is
// concurrency-safe on its own; the lock only guards replacing it.
type resettableJar struct {
	mu  sync.RWMutex
	jar http.CookieJar
}

func newResettableJar() *resettableJar {
	jar, _ := cookiejar.New(nil)
	return &resettableJar{jar: jar}
}

func (j *resettableJar) SetCookies(u *url.URL, cookies []*http.Cookie) {
	j.mu.RLock()
	defer j.mu.RUnlock()
	j.jar.SetCookies(u, cookies)
}

func (j *resettableJar) Cookies(u *url.URL) []*http.Cookie {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return j.jar.Cookies(u)
}

func (j *resettableJar) reset() {
	jar, _ := cookiejar.New(nil)

	j.mu.Lock()
	defer j.mu.Unlock()
	j.jar = jar
}

type cachedCookie struct {
	URL     string `json:"url"`
	Name    string `json:"name"`
	Value   string `json:"value"`
	Path    string `json:"path,omitempty"`
	Expires string `json:"expires,omitempty"`
}

// Option configures a Client at construction time.
type Option func(*Client)

// WithLimiter paces the client's requests. Passing nil leaves it unpaced.
func WithLimiter(limiter *ratelimit.Limiter) Option {
	return func(c *Client) { c.limiter = limiter }
}

// WithTimeout overrides DefaultTimeout.
func WithTimeout(timeout time.Duration) Option {
	return func(c *Client) {
		if timeout > 0 {
			c.Client.Timeout = timeout
		}
	}
}

// New returns a Client with an empty in-memory cookie jar.
func New(options ...Option) *Client {
	jar := newResettableJar()
	client := &Client{
		Client: &http.Client{
			Jar:     jar,
			Timeout: DefaultTimeout,
		},
		headers: defaultHeaders(),
		jar:     jar,
		limiter: ratelimit.New(ratelimit.DefaultRPS),
	}

	for _, option := range options {
		option(client)
	}

	return client
}

// SetLimiter swaps the limiter, for example when the configured rate changes.
func (c *Client) SetLimiter(limiter *ratelimit.Limiter) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.limiter = limiter
}

// Limiter returns the limiter currently pacing this client.
func (c *Client) Limiter() *ratelimit.Limiter {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.limiter
}

// ResetSession drops every cookie, forcing the next login to start clean. It is
// safe to call while other requests are in flight.
func (c *Client) ResetSession() {
	c.jar.reset()
}

// Get performs a rate-limited GET and returns the body as a string.
func (c *Client) Get(ctx context.Context, rawURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	c.applyHeaders(req)

	return c.do(ctx, req)
}

// GetWithParams performs a GET with params merged into the query string.
func (c *Client) GetWithParams(ctx context.Context, rawURL string, params url.Values) (string, error) {
	merged, err := mergeQuery(rawURL, params)
	if err != nil {
		return "", err
	}
	return c.Get(ctx, merged)
}

// PostForm performs a rate-limited form POST.
func (c *Client) PostForm(ctx context.Context, rawURL string, data url.Values) (string, error) {
	return c.PostFormWithParams(ctx, rawURL, nil, data)
}

// PostFormWithParams performs a form POST with params in the query string and
// data in the body, which is what the search endpoints expect.
func (c *Client) PostFormWithParams(ctx context.Context, rawURL string, params url.Values, data url.Values) (string, error) {
	merged, err := mergeQuery(rawURL, params)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, merged, strings.NewReader(data.Encode()))
	if err != nil {
		return "", err
	}
	c.applyHeaders(req)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")

	return c.do(ctx, req)
}

// do waits for a rate-limit slot, sends the request and reads the body.
func (c *Client) do(ctx context.Context, req *http.Request) (string, error) {
	if limiter := c.Limiter(); limiter != nil {
		if err := limiter.Wait(ctx); err != nil {
			return "", err
		}
	}

	resp, err := c.Client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	return string(body), nil
}

// LoadCookies restores cookies previously written by SaveCookies.
func (c *Client) LoadCookies(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}

	var cached []cachedCookie
	if err := json.Unmarshal(data, &cached); err != nil {
		return err
	}

	for _, item := range cached {
		if item.URL == "" {
			continue
		}
		parsedURL, err := url.Parse(item.URL)
		if err != nil {
			continue
		}
		cookie := &http.Cookie{
			Name:  item.Name,
			Value: item.Value,
			Path:  item.Path,
		}
		if item.Expires != "" {
			if expires, err := time.Parse(time.RFC3339, item.Expires); err == nil {
				cookie.Expires = expires
			}
		}
		c.Client.Jar.SetCookies(parsedURL, []*http.Cookie{cookie})
	}

	return nil
}

// SaveCookies writes the jar's cookies for the given URLs to path.
func (c *Client) SaveCookies(path string, rawURLs []string) error {
	var cached []cachedCookie
	for _, rawURL := range rawURLs {
		parsedURL, err := url.Parse(rawURL)
		if err != nil {
			return err
		}
		for _, cookie := range c.Client.Jar.Cookies(parsedURL) {
			item := cachedCookie{
				URL:   rawURL,
				Name:  cookie.Name,
				Value: cookie.Value,
				Path:  cookie.Path,
			}
			if !cookie.Expires.IsZero() {
				item.Expires = cookie.Expires.Format(time.RFC3339)
			}
			cached = append(cached, item)
		}
	}

	data, err := json.MarshalIndent(cached, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	return os.WriteFile(path, data, 0o600)
}

func mergeQuery(rawURL string, params url.Values) (string, error) {
	if len(params) == 0 {
		return rawURL, nil
	}

	reqURL, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	query := reqURL.Query()
	for key, values := range params {
		query.Del(key)
		for _, value := range values {
			query.Add(key, value)
		}
	}
	reqURL.RawQuery = query.Encode()

	return reqURL.String(), nil
}

func (c *Client) applyHeaders(req *http.Request) {
	for key, values := range c.headers {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
}

func defaultHeaders() http.Header {
	headers := http.Header{}
	headers.Set("Cache-Control", "max-age=0")
	headers.Set("Sec-Ch-Ua", `"Chromium";v="137", "Not/A)Brand";v="24"`)
	headers.Set("Sec-Ch-Ua-Mobile", "?0")
	headers.Set("Sec-Ch-Ua-Platform", `"Windows"`)
	headers.Set("Accept-Language", "zh-CN,zh;q=0.9")
	headers.Set("Upgrade-Insecure-Requests", "1")
	headers.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/137.0.0.0 Safari/537.36")
	headers.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7")
	headers.Set("Sec-Fetch-Site", "same-origin")
	headers.Set("Sec-Fetch-Mode", "navigate")
	headers.Set("Sec-Fetch-User", "?1")
	headers.Set("Sec-Fetch-Dest", "document")
	headers.Set("Priority", "u=0, i")
	headers.Set("Connection", "keep-alive")
	return headers
}
