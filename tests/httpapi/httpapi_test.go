package httpapi_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"message-gateway/internal/httpapi"
	"message-gateway/internal/message"
)

const testAccountToken = "test-key"

type fakeEnqueuer struct {
	mu       sync.Mutex
	messages []message.Message
	id       message.DeliveryID
	err      error
}

func (f *fakeEnqueuer) EnqueueObserved(
	_ context.Context,
	msg message.Message,
	_ message.DeliveryObserver,
) (message.DeliveryID, error) {
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

func testTokenHash(token string) [sha256.Size]byte {
	return sha256.Sum256([]byte(token))
}

// newTestServer fills in the shared test account token for accounts that do
// not specify one. Tests that exercise account scoping set TokenHash directly.
func newTestServer(enqueuer httpapi.Enqueuer, opts httpapi.Options) *httpapi.Server {
	for name, account := range opts.Accounts {
		if account.TokenHash == ([32]byte{}) {
			account.TokenHash = testTokenHash(testAccountToken)
			opts.Accounts[name] = account
		}
	}
	return httpapi.New(enqueuer, opts)
}

func newHandler(enqueuer httpapi.Enqueuer) *httpapi.Server {
	return newTestServer(enqueuer, httpapi.Options{
		Accounts: map[string]httpapi.Account{
			"qq": {
				Name:      "qq",
				Driver:    "napcat",
				Platform:  message.PlatformQQ,
				TokenHash: testTokenHash(testAccountToken),
			},
		},
	})
}

func newMultiAccountHandler(enqueuer httpapi.Enqueuer) *httpapi.Server {
	return newTestServer(enqueuer, httpapi.Options{
		Accounts: map[string]httpapi.Account{
			"qq": {
				Name:      "qq",
				Driver:    "napcat",
				Platform:  message.PlatformQQ,
				TokenHash: testTokenHash(testAccountToken),
			},
			"qq-backup": {
				Name:      "qq-backup",
				Driver:    "napcat",
				Platform:  message.PlatformQQ,
				TokenHash: testTokenHash(testAccountToken),
			},
		},
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
	return "Bearer " + testAccountToken
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

func TestLegacyRoutesRemoved(t *testing.T) {
	handler := newHandler(&fakeEnqueuer{})
	for _, path := range []string{"/api/v1/messages", "/api/v1/messages/qq-1", "/api/v1/ws"} {
		r := doRequest(t, handler, http.MethodGet, path, "", bearerToken())
		if r.Code != http.StatusNotFound {
			t.Errorf("%s: got %d, want 404", path, r.Code)
		}
	}
}
