package httpclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/datayurei/robyou/internal/ratelimit"
)

func TestRequestsGoThroughTheLimiter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer server.Close()

	client := New(WithLimiter(ratelimit.New(20))) // 50ms apart
	start := time.Now()
	for i := 0; i < 3; i++ {
		if _, err := client.Get(context.Background(), server.URL); err != nil {
			t.Fatalf("Get() = %v", err)
		}
	}

	// Three requests take two gaps of 50ms.
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond {
		t.Fatalf("three paced requests took %v, want at least 100ms", elapsed)
	}
}

func TestCancelledContextSkipsTheRequest(t *testing.T) {
	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
	}))
	defer server.Close()

	client := New(WithLimiter(ratelimit.New(0.5)))
	if _, err := client.Get(context.Background(), server.URL); err != nil {
		t.Fatalf("first Get() = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Get(ctx, server.URL); err == nil {
		t.Fatal("a cancelled request should fail")
	}
	if hits != 1 {
		t.Fatalf("server saw %d requests, want 1", hits)
	}
}

func TestPostFormWithParamsSplitsQueryAndBody(t *testing.T) {
	var gotQuery, gotBody, gotContentType string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		gotContentType = r.Header.Get("Content-Type")
		r.ParseForm()
		gotBody = r.PostForm.Encode()
	}))
	defer server.Close()

	client := New(WithLimiter(ratelimit.New(100)))
	_, err := client.PostFormWithParams(
		context.Background(),
		server.URL+"?keep=1",
		url.Values{"kcxx": {"高等数学"}},
		url.Values{"sEcho": {"1"}},
	)
	if err != nil {
		t.Fatalf("PostFormWithParams() = %v", err)
	}

	if gotBody != "sEcho=1" {
		t.Fatalf("body = %q, want sEcho=1", gotBody)
	}
	// url.Values.Encode sorts by key, so kcxx comes before keep.
	if gotQuery != "kcxx=%E9%AB%98%E7%AD%89%E6%95%B0%E5%AD%A6&keep=1" {
		t.Fatalf("query = %q, existing params should be kept and new ones added", gotQuery)
	}
	if gotContentType != "application/x-www-form-urlencoded; charset=UTF-8" {
		t.Fatalf("content type = %q", gotContentType)
	}
}

func TestResetSessionIsSafeDuringRequests(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "JSESSIONID", Value: "abc", Path: "/"})
	}))
	defer server.Close()

	client := New(WithLimiter(ratelimit.New(ratelimit.Unlimited)))
	if _, err := client.Get(context.Background(), server.URL); err != nil {
		t.Fatalf("Get() = %v", err)
	}

	serverURL, _ := url.Parse(server.URL)
	if len(client.Jar.Cookies(serverURL)) != 1 {
		t.Fatal("expected the session cookie to be stored")
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				client.ResetSession()
				return
			}
			client.Get(context.Background(), server.URL)
		}(i)
	}
	wg.Wait()
}
