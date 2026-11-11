package httpapi_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"message-gateway/internal/httpapi"
	"message-gateway/internal/message"
	"message-gateway/internal/napcat"
)

type proxiedRequest struct {
	Method   string
	Path     string
	RawQuery string
	Auth     string
	Header   http.Header
	Body     string
}

type proxyRecording struct {
	mu       sync.Mutex
	requests []proxiedRequest
	status   int
	header   http.Header
	body     string
}

func (p *proxyRecording) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)

	p.mu.Lock()
	p.requests = append(p.requests, proxiedRequest{
		Method:   r.Method,
		Path:     r.URL.Path,
		RawQuery: r.URL.RawQuery,
		Auth:     r.Header.Get("Authorization"),
		Header:   r.Header.Clone(),
		Body:     string(body),
	})
	status := p.status
	if status == 0 {
		status = http.StatusOK
	}
	header := p.header.Clone()
	responseBody := p.body
	p.mu.Unlock()

	for name, values := range header {
		for _, value := range values {
			w.Header().Add(name, value)
		}
	}
	w.WriteHeader(status)
	_, _ = io.WriteString(w, responseBody)
}

func (p *proxyRecording) snapshot() []proxiedRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]proxiedRequest(nil), p.requests...)
}

func newProxyAccount(t *testing.T, name, clientToken, upstreamURL string) httpapi.Account {
	t.Helper()

	client, err := napcat.NewClient(napcat.Config{
		BaseURL:      upstreamURL,
		AccessToken:  "upstream-" + name,
		Timeout:      time.Second,
		ProxyTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("napcat.NewClient(%q) error = %v", name, err)
	}

	return httpapi.Account{
		Name:      name,
		Driver:    "napcat",
		Platform:  message.PlatformQQ,
		TokenHash: sha256.Sum256([]byte(clientToken)),
		Proxy:     client,
	}
}

func newAccountAPI(t *testing.T, accounts ...httpapi.Account) *httpapi.Server {
	t.Helper()

	byName := make(map[string]httpapi.Account, len(accounts))
	for _, account := range accounts {
		byName[account.Name] = account
	}
	return newTestServer(&fakeEnqueuer{}, httpapi.Options{
		Accounts: byName,
	})
}

func TestAccountProxyPassThrough(t *testing.T) {
	upstream := &proxyRecording{
		header: http.Header{"X-Upstream": []string{"yes"}},
		body:   `{"status":"ok","retcode":0,"data":{"user_id":123}}`,
	}
	upstreamServer := httptest.NewServer(upstream)
	t.Cleanup(upstreamServer.Close)

	api := newAccountAPI(t, newProxyAccount(t, "qq", "qq-token", upstreamServer.URL))
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/accounts/qq/get_login_info?foo=bar",
		strings.NewReader(`{"x":1}`),
	)
	request.Header.Set("Authorization", "Bearer qq-token")
	request.Header.Set("X-Test", "kept")
	recorder := httptest.NewRecorder()
	api.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("X-Upstream") != "yes" {
		t.Error("upstream response header was not copied")
	}
	if recorder.Body.String() != `{"status":"ok","retcode":0,"data":{"user_id":123}}` {
		t.Errorf("body = %q", recorder.Body.String())
	}

	requests := upstream.snapshot()
	if len(requests) != 1 {
		t.Fatalf("upstream requests = %d, want 1", len(requests))
	}
	got := requests[0]
	if got.Method != http.MethodPost {
		t.Errorf("method = %q, want POST", got.Method)
	}
	if got.Path != "/get_login_info" {
		t.Errorf("path = %q, want /get_login_info", got.Path)
	}
	if got.RawQuery != "foo=bar" {
		t.Errorf("query = %q, want foo=bar", got.RawQuery)
	}
	if got.Auth != "Bearer upstream-qq" {
		t.Errorf("authorization = %q, want the account token", got.Auth)
	}
	if got.Body != `{"x":1}` {
		t.Errorf("body = %q", got.Body)
	}
	if got.Header.Get("X-Test") != "kept" {
		t.Errorf("request header X-Test was not forwarded")
	}
}

func TestAccountProxyAccountTokenInQuery(t *testing.T) {
	upstream := &proxyRecording{body: `{"status":"ok","retcode":0}`}
	upstreamServer := httptest.NewServer(upstream)
	t.Cleanup(upstreamServer.Close)

	api := newAccountAPI(t, newProxyAccount(t, "qq", "qq-token", upstreamServer.URL))
	recorder := doRequest(
		t,
		api,
		http.MethodGet,
		"/api/v1/accounts/qq/get_group_list?group_id=20001&access_token=qq-token",
		"",
		"",
	)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}

	requests := upstream.snapshot()
	if len(requests) != 1 {
		t.Fatalf("upstream requests = %d, want 1", len(requests))
	}
	if requests[0].RawQuery != "group_id=20001" {
		t.Errorf("query = %q, want group_id=20001", requests[0].RawQuery)
	}
	if requests[0].Auth != "Bearer upstream-qq" {
		t.Errorf("authorization = %q, want the account token", requests[0].Auth)
	}
}

func TestAccountProxyAuth(t *testing.T) {
	upstream := &proxyRecording{body: `{"status":"ok","retcode":0}`}
	upstreamServer := httptest.NewServer(upstream)
	t.Cleanup(upstreamServer.Close)

	api := newAccountAPI(
		t,
		newProxyAccount(t, "qq", "qq-token", upstreamServer.URL),
		newProxyAccount(t, "qq-backup", "backup-token", upstreamServer.URL),
	)

	tests := []struct {
		name          string
		authorization string
		want          int
	}{
		{name: "missing", authorization: "", want: http.StatusUnauthorized},
		{name: "invalid", authorization: "Bearer nope", want: http.StatusForbidden},
		{name: "wrong account", authorization: "Bearer backup-token", want: http.StatusForbidden},
		{name: "account token", authorization: "Bearer qq-token", want: http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := doRequest(
				t,
				api,
				http.MethodPost,
				"/api/v1/accounts/qq/get_login_info",
				`{}`,
				tt.authorization,
			)
			if recorder.Code != tt.want {
				t.Fatalf("status = %d, want %d: %s", recorder.Code, tt.want, recorder.Body.String())
			}
			if tt.want == http.StatusUnauthorized && recorder.Header().Get("WWW-Authenticate") == "" {
				t.Error("WWW-Authenticate header is missing")
			}
		})
	}
}

func TestAccountProxyUnknownAccount(t *testing.T) {
	api := newAccountAPI(t, newProxyAccount(t, "qq", "qq-token", "http://napcat:3001"))
	recorder := doRequest(
		t,
		api,
		http.MethodPost,
		"/api/v1/accounts/missing/get_login_info",
		`{}`,
		"Bearer qq-token",
	)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", recorder.Code, recorder.Body.String())
	}
}

func TestAccountProxyNormalizesPath(t *testing.T) {
	upstream := &proxyRecording{body: `{"status":"ok","retcode":0}`}
	upstreamServer := httptest.NewServer(upstream)
	t.Cleanup(upstreamServer.Close)

	api := newAccountAPI(t, newProxyAccount(t, "qq", "qq-token", upstreamServer.URL))

	recorder := doRequest(
		t,
		api,
		http.MethodPost,
		"/api/v1/accounts/qq//get_login_info/",
		`{}`,
		"Bearer qq-token",
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	requests := upstream.snapshot()
	if len(requests) != 1 || requests[0].Path != "/get_login_info" {
		t.Fatalf("upstream requests = %+v, want /get_login_info", requests)
	}

	recorder = doRequest(
		t,
		api,
		http.MethodPost,
		"/api/v1/accounts/qq/../admin",
		`{}`,
		"Bearer qq-token",
	)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("dot segment status = %d, want 400: %s", recorder.Code, recorder.Body.String())
	}
}

func TestAccountProxySendIsReserved(t *testing.T) {
	upstream := &proxyRecording{body: `{"status":"ok","retcode":0}`}
	upstreamServer := httptest.NewServer(upstream)
	t.Cleanup(upstreamServer.Close)

	api := newAccountAPI(t, newProxyAccount(t, "qq", "qq-token", upstreamServer.URL))
	for _, action := range []string{"send_private_msg", "send_msg_async", "send_group_msg_rate_limited"} {
		recorder := doRequest(
			t,
			api,
			http.MethodPost,
			"/api/v1/accounts/qq/"+action,
			`{"user_id":10001,"message":"hello"}`,
			"Bearer qq-token",
		)
		if recorder.Code != http.StatusNotImplemented {
			t.Errorf("%s status = %d, want 501", action, recorder.Code)
		}
	}
	if requests := upstream.snapshot(); len(requests) != 0 {
		t.Fatalf("upstream received %d requests, want 0", len(requests))
	}
}

type accountIDEnqueuer struct {
	mu  sync.Mutex
	seq int
}

func (e *accountIDEnqueuer) EnqueueObserved(
	_ context.Context,
	msg message.Message,
	_ message.DeliveryObserver,
) (message.DeliveryID, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.seq++
	return message.DeliveryID(fmt.Sprintf("%s-%d", msg.Sender, e.seq)), nil
}

func newStatusAccountAPI(t *testing.T) *httpapi.Server {
	t.Helper()

	return newTestServer(&accountIDEnqueuer{}, httpapi.Options{
		Accounts: map[string]httpapi.Account{
			"qq": {
				Name:      "qq",
				Driver:    "napcat",
				Platform:  message.PlatformQQ,
				TokenHash: sha256.Sum256([]byte("qq-token")),
			},
			"qq-backup": {
				Name:      "qq-backup",
				Driver:    "napcat",
				Platform:  message.PlatformQQ,
				TokenHash: sha256.Sum256([]byte("backup-token")),
			},
		},
	})
}

func postLegacyMessage(t *testing.T, handler http.Handler, platform, token string) string {
	t.Helper()

	body := fmt.Sprintf(
		`{"platform":%q,"target":{"kind":"user","id":"10001"},"text":"hello"}`,
		platform,
	)
	recorder := doRequest(t, handler, http.MethodPost, "/api/v1/messages", body, "Bearer "+token)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("legacy send status = %d, want 202: %s", recorder.Code, recorder.Body.String())
	}

	var response struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode legacy send response: %v", err)
	}
	return response.ID
}

func TestAccountStatusScoping(t *testing.T) {
	api := newStatusAccountAPI(t)

	qqID := postLegacyMessage(t, api, "qq", "qq-token")
	backupID := postLegacyMessage(t, api, "qq-backup", "backup-token")

	tests := []struct {
		name          string
		path          string
		authorization string
		want          int
	}{
		{
			name:          "own account token",
			path:          "/api/v1/accounts/qq/message-status/" + qqID,
			authorization: "Bearer qq-token",
			want:          http.StatusOK,
		},
		{
			name:          "missing token",
			path:          "/api/v1/accounts/qq/message-status/" + qqID,
			authorization: "",
			want:          http.StatusUnauthorized,
		},
		{
			name:          "other account token",
			path:          "/api/v1/accounts/qq/message-status/" + qqID,
			authorization: "Bearer backup-token",
			want:          http.StatusForbidden,
		},
		{
			name:          "other account delivery",
			path:          "/api/v1/accounts/qq/message-status/" + backupID,
			authorization: "Bearer qq-token",
			want:          http.StatusNotFound,
		},
		{
			name:          "unknown delivery",
			path:          "/api/v1/accounts/qq/message-status/qq-999",
			authorization: "Bearer qq-token",
			want:          http.StatusNotFound,
		},
		{
			name:          "backup account delivery",
			path:          "/api/v1/accounts/qq-backup/message-status/" + backupID,
			authorization: "Bearer backup-token",
			want:          http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := doRequest(t, api, http.MethodGet, tt.path, "", tt.authorization)
			if recorder.Code != tt.want {
				t.Fatalf("status = %d, want %d: %s", recorder.Code, tt.want, recorder.Body.String())
			}
			if recorder.Code == http.StatusOK {
				var body struct {
					ID     string `json:"id"`
					Status string `json:"status"`
				}
				if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
					t.Fatalf("decode status: %v", err)
				}
				if body.Status != "queued" {
					t.Errorf("status = %q, want queued", body.Status)
				}
			}
		})
	}
}

func TestAccountStatusRejectsNonGET(t *testing.T) {
	api := newStatusAccountAPI(t)
	id := postLegacyMessage(t, api, "qq", "qq-token")

	recorder := doRequest(
		t,
		api,
		http.MethodPost,
		"/api/v1/accounts/qq/message-status/"+id,
		"",
		"Bearer qq-token",
	)
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405: %s", recorder.Code, recorder.Body.String())
	}
}

func TestAccountStatusQueryToken(t *testing.T) {
	api := newStatusAccountAPI(t)
	id := postLegacyMessage(t, api, "qq", "qq-token")

	recorder := doRequest(
		t,
		api,
		http.MethodGet,
		"/api/v1/accounts/qq/message-status/"+id+"?access_token=qq-token",
		"",
		"",
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
}

func TestLegacyStatusStillWorks(t *testing.T) {
	api := newStatusAccountAPI(t)
	id := postLegacyMessage(t, api, "qq", "qq-token")

	recorder := doRequest(
		t,
		api,
		http.MethodGet,
		"/api/v1/messages/"+strings.TrimSpace(id),
		"",
		"Bearer qq-token",
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("legacy status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
}
