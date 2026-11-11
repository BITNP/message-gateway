package napcat_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"message-gateway/internal/napcat"
)

type proxiedRequest struct {
	Method   string
	Path     string
	RawQuery string
	Auth     string
	Body     string
}

func TestClientProxyForwardsRequest(t *testing.T) {
	requests := make(chan proxiedRequest, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests <- proxiedRequest{
			Method:   r.Method,
			Path:     r.URL.Path,
			RawQuery: r.URL.RawQuery,
			Auth:     r.Header.Get("Authorization"),
			Body:     string(body),
		}
		w.Header().Set("X-Upstream", "yes")
		w.WriteHeader(http.StatusTeapot)
		_, _ = io.WriteString(w, `{"status":"teapot"}`)
	}))
	defer upstream.Close()

	client, err := napcat.NewClient(napcat.Config{
		BaseURL:      upstream.URL + "/base",
		AccessToken:  "upstream-token",
		ProxyTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	request := httptest.NewRequest(
		http.MethodPost,
		"/send_msg?foo=bar&access_token=client-token",
		strings.NewReader(`{"message":"hi"}`),
	)
	request.Header.Set("Authorization", "Bearer client-token")
	request.Header.Set("X-Test", "kept")

	recorder := httptest.NewRecorder()
	client.ServeHTTP(recorder, request)

	got := <-requests
	if got.Method != http.MethodPost {
		t.Errorf("method = %q, want POST", got.Method)
	}
	if got.Path != "/base/send_msg" {
		t.Errorf("path = %q, want /base/send_msg", got.Path)
	}
	if got.RawQuery != "foo=bar" {
		t.Errorf("query = %q, want foo=bar", got.RawQuery)
	}
	if got.Auth != "Bearer upstream-token" {
		t.Errorf("authorization = %q, want upstream account token", got.Auth)
	}
	if got.Body != `{"message":"hi"}` {
		t.Errorf("body = %q", got.Body)
	}

	if recorder.Code != http.StatusTeapot {
		t.Errorf("status = %d, want %d", recorder.Code, http.StatusTeapot)
	}
	if recorder.Header().Get("X-Upstream") != "yes" {
		t.Errorf("upstream header was not copied")
	}
	if recorder.Body.String() != `{"status":"teapot"}` {
		t.Errorf("body = %q", recorder.Body.String())
	}
}

func TestClientProxyRejectsUpgrade(t *testing.T) {
	client, err := napcat.NewClient(napcat.Config{
		BaseURL:      "http://napcat:3001",
		ProxyTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	request := httptest.NewRequest(http.MethodGet, "/ws", nil)
	request.Header.Set("Upgrade", "websocket")

	recorder := httptest.NewRecorder()
	client.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNotImplemented)
	}
}

func TestClientProxyUpstreamFailure(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	baseURL := upstream.URL
	upstream.Close()

	client, err := napcat.NewClient(napcat.Config{
		BaseURL:      baseURL,
		ProxyTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	request := httptest.NewRequest(http.MethodPost, "/get_login_info", nil)
	recorder := httptest.NewRecorder()
	client.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadGateway)
	}
	var body struct {
		Status  string `json:"status"`
		Retcode int    `json:"retcode"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body %q: %v", recorder.Body.String(), err)
	}
	if body.Status != "failed" || body.Retcode != 1500 {
		t.Errorf("body = %+v, want failed/1500", body)
	}
}
