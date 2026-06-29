package httpclient

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"time"
)

type Client struct {
	*http.Client
}

func New() *Client {
	jar, _ := cookiejar.New(nil)
	return &Client{
		Client: &http.Client{
			Jar:     jar,
			Timeout: 10 * time.Second,
		},
	}
}

func (c *Client) GetString(url string) (string, error) {
	resp, err := c.Client.Get(url)
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

func (c *Client) PostFormString(url string, data url.Values) (string, error) {
	resp, err := c.Client.PostForm(url, data)
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
