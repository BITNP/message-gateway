package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"message-gateway/internal/httpapi"
	"message-gateway/internal/message"
)

const testAPIKey = "test-key"

type fakeEnqueuer struct {
	mu       sync.Mutex
	messages []message.Message
	id       message.DeliveryID
	err      error
}

func (f *fakeEnqueuer) Enqueue(_ context.Context, msg message.Message) (message.DeliveryID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.messages = append(f.messages, msg)
	if f.err != nil {
		return "", f.err
	}
	if f.id == "" {
		return "qq-1", nil
	}
	return f.id, nil
}

func (f *fakeEnqueuer) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.messages)
}

func (f *fakeEnqueuer) lastMessage() message.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.messages[len(f.messages)-1]
}

func newHandler(enqueuer httpapi.Enqueuer) *httpapi.Server {
	return httpapi.New(enqueuer, httpapi.Options{
		SenderKey: "qq",
		APIKey:    testAPIKey,
	})
}

func doRequest(
	t *testing.T,
	handler http.Handler,
	method string,
	path string,
	body string,
	authorization string,
) *httptest.ResponseRecorder {
	t.Helper()

	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func bearerToken() string {
	return "Bearer " + testAPIKey
}

func validBody() string {
	return `{"platform":"qq","target":{"kind":"user","id":"10001"},"text":"hello"}`
}

func TestHealthz(t *testing.T) {
	handler := newHandler(&fakeEnqueuer{})
	recorder := doRequest(t, handler, http.MethodGet, "/healthz", "", "")

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}

	var response map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response["status"] != "ok" {
		t.Errorf("status = %q, want ok", response["status"])
	}
}

func TestSendRequiresAuth(t *testing.T) {
	tests := []struct {
		name          string
		authorization string
	}{
		{"missing", ""},
		{"wrong scheme", "Basic " + testAPIKey},
		{"wrong token", "Bearer wrong"},
		{"missing token", "Bearer"},
		{"extra fields", "Bearer " + testAPIKey + " extra"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			enqueuer := &fakeEnqueuer{}
			handler := newHandler(enqueuer)

			recorder := doRequest(t, handler, http.MethodPost, "/api/v1/messages", validBody(), tt.authorization)
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnauthorized)
			}
			if got := recorder.Header().Get("WWW-Authenticate"); got == "" {
				t.Error("WWW-Authenticate header is missing")
			}
			if enqueuer.callCount() != 0 {
				t.Error("enqueuer was called for an unauthorized request")
			}
		})
	}
}

func TestSendAcceptsCaseInsensitiveBearerScheme(t *testing.T) {
	enqueuer := &fakeEnqueuer{}
	handler := newHandler(enqueuer)

	recorder := doRequest(t, handler, http.MethodPost, "/api/v1/messages", validBody(), "bearer "+testAPIKey)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusAccepted)
	}
}

func TestSendEnqueuesMessage(t *testing.T) {
	enqueuer := &fakeEnqueuer{id: "qq-7"}
	handler := newHandler(enqueuer)

	recorder := doRequest(t, handler, http.MethodPost, "/api/v1/messages", validBody(), bearerToken())
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusAccepted, recorder.Body.String())
	}

	var response struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.ID != "qq-7" {
		t.Errorf("id = %q, want qq-7", response.ID)
	}
	if response.Status != "queued" {
		t.Errorf("status = %q, want queued", response.Status)
	}

	if enqueuer.callCount() != 1 {
		t.Fatalf("enqueuer calls = %d, want 1", enqueuer.callCount())
	}
	msg := enqueuer.lastMessage()
	if msg.Platform != message.PlatformQQ {
		t.Errorf("platform = %q, want qq", msg.Platform)
	}
	if msg.Sender != "qq" {
		t.Errorf("sender = %q, want qq", msg.Sender)
	}
	if msg.Target.Kind != message.TargetUser {
		t.Errorf("target kind = %q, want user", msg.Target.Kind)
	}
	if msg.Target.ID != "10001" {
		t.Errorf("target id = %q, want 10001", msg.Target.ID)
	}
	if msg.Text != "hello" {
		t.Errorf("text = %q, want hello", msg.Text)
	}
}

func TestSendGroup(t *testing.T) {
	enqueuer := &fakeEnqueuer{}
	handler := newHandler(enqueuer)

	body := `{"platform":"qq","target":{"kind":"group","id":"20002"},"text":"group hello"}`
	recorder := doRequest(t, handler, http.MethodPost, "/api/v1/messages", body, bearerToken())
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusAccepted)
	}

	msg := enqueuer.lastMessage()
	if msg.Target.Kind != message.TargetGroup {
		t.Errorf("target kind = %q, want group", msg.Target.Kind)
	}
	if msg.Target.ID != "20002" {
		t.Errorf("target id = %q, want 20002", msg.Target.ID)
	}
}

func TestSendRejectsInvalidRequests(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"empty body", ""},
		{"malformed json", "{"},
		{"json array", "[]"},
		{"json null", "null"},
		{"missing platform", `{"target":{"kind":"user","id":"10001"},"text":"hello"}`},
		{"unsupported platform", `{"platform":"telegram","target":{"kind":"user","id":"10001"},"text":"hello"}`},
		{"missing target", `{"platform":"qq","text":"hello"}`},
		{"unsupported target kind", `{"platform":"qq","target":{"kind":"channel","id":"10001"},"text":"hello"}`},
		{"missing target id", `{"platform":"qq","target":{"kind":"user"},"text":"hello"}`},
		{"blank text", `{"platform":"qq","target":{"kind":"user","id":"10001"},"text":"   "}`},
		{"trailing json", validBody() + ` {}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			enqueuer := &fakeEnqueuer{}
			handler := newHandler(enqueuer)

			recorder := doRequest(t, handler, http.MethodPost, "/api/v1/messages", tt.body, bearerToken())
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
			}
			if enqueuer.callCount() != 0 {
				t.Error("enqueuer was called for an invalid request")
			}
		})
	}
}

func TestSendRejectsOversizedBody(t *testing.T) {
	enqueuer := &fakeEnqueuer{}
	handler := newHandler(enqueuer)

	body := `{"platform":"qq","target":{"kind":"user","id":"10001"},"text":"` +
		strings.Repeat("a", 1<<20+1) + `"}`
	recorder := doRequest(t, handler, http.MethodPost, "/api/v1/messages", body, bearerToken())
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusRequestEntityTooLarge)
	}
	if enqueuer.callCount() != 0 {
		t.Error("enqueuer was called for an oversized request")
	}
}

func TestSendMapsEnqueueErrors(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
	}{
		{"queue full", message.ErrQueueFull, http.StatusTooManyRequests},
		{"closed", message.ErrClosed, http.StatusServiceUnavailable},
		{"invalid message", message.ErrInvalidMessage, http.StatusBadRequest},
		{"unexpected", errors.New("boom"), http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			enqueuer := &fakeEnqueuer{err: tt.err}
			handler := newHandler(enqueuer)

			recorder := doRequest(t, handler, http.MethodPost, "/api/v1/messages", validBody(), bearerToken())
			if recorder.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", recorder.Code, tt.wantStatus)
			}
			if tt.wantStatus == http.StatusTooManyRequests {
				if got := recorder.Header().Get("Retry-After"); got != "1" {
					t.Errorf("Retry-After = %q, want 1", got)
				}
			}
		})
	}
}

func TestWrongMethod(t *testing.T) {
	handler := newHandler(&fakeEnqueuer{})
	recorder := doRequest(t, handler, http.MethodGet, "/api/v1/messages", "", bearerToken())
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusMethodNotAllowed)
	}
}
