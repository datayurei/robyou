package httpclient

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strings"
	"time"
)

type Client struct {
	*http.Client
	headers http.Header
}

type cachedCookie struct {
	URL     string `json:"url"`
	Name    string `json:"name"`
	Value   string `json:"value"`
	Path    string `json:"path,omitempty"`
	Expires string `json:"expires,omitempty"`
}

func New() *Client {
	jar, _ := cookiejar.New(nil)
	return &Client{
		Client: &http.Client{
			Jar:     jar,
			Timeout: 10 * time.Second,
		},
		headers: defaultHeaders(),
	}
}

func (c *Client) GetString(url string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}

	c.applyHeaders(req)
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

func (c *Client) GetStringWithParams(rawURL string, params url.Values) (string, error) {
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

	return c.GetString(reqURL.String())
}

func (c *Client) PostFormString(url string, data url.Values) (string, error) {
	return c.PostFormStringWithParams(url, nil, data)
}

func (c *Client) PostFormStringWithParams(rawURL string, params url.Values, data url.Values) (string, error) {
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

	req, err := http.NewRequest(http.MethodPost, reqURL.String(), strings.NewReader(data.Encode()))
	if err != nil {
		return "", err
	}
	c.applyHeaders(req)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")

	resp, err := c.Client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	str := string(body)

	return str, nil

}

func (c *Client) LoadCookies(path string, rawURLs []string) error {
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
		rawURL := item.URL
		if rawURL == "" {
			continue
		}
		parsedURL, err := url.Parse(rawURL)
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

	return os.WriteFile(path, data, 0600)
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
