package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"message-gateway/internal/message"
)

func jsonSend(t *testing.T, handler http.Handler, action, body, token string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/accounts/qq/"+action, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func TestOneBotSendRejectsInvalidRequests(t *testing.T) {
	for _, tc := range []struct{ body string }{
		{`{`}, {`{}`}, {`{"user_id":0,"message":"hello"}`},
		{`{"user_id":1,"message":" "}`}, {`{"user_id":1,"message":{}}`},
		{`{"user_id":1,"message":"hello","auto_escape":"invalid"}`},
	} {
		e := &fakeEnqueuer{}
		w := jsonSend(t, newHandler(e), "send_private_msg_async", tc.body, testAccountToken)
		if w.Code != http.StatusBadRequest || e.callCount() != 0 {
			t.Errorf("body %s: status %d, enqueues %d", tc.body, w.Code, e.callCount())
		}
	}
}

func TestOneBotSendAuthAndQueueErrors(t *testing.T) {
	body := `{"user_id":1,"message":"hello"}`
	for _, tc := range []struct {
		name, token string
		err         error
		want        int
	}{
		{"missing", "", nil, 401}, {"wrong token", "wrong", nil, 403},
		{"full", testAccountToken, message.ErrQueueFull, 429},
		{"closed", testAccountToken, message.ErrClosed, 503},
		{"other error", testAccountToken, errors.New("failure"), 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := jsonSend(t, newHandler(&fakeEnqueuer{err: tc.err}), "send_private_msg_async", body, tc.token)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d: %s", w.Code, tc.want, w.Body)
			}
			if tc.want == 429 && w.Header().Get("Retry-After") != "1" {
				t.Error("missing Retry-After")
			}
		})
	}
}

func TestOneBotSendGenericGroupAndGet(t *testing.T) {
	e := &fakeEnqueuer{}
	h := newHandler(e)
	w := doRequest(t, h, http.MethodGet, "/api/v1/accounts/qq/send_msg_async?message_type=group&group_id=42&message=hello", "", bearerToken())
	if w.Code != 202 || e.lastMessage().Target != (message.Target{Kind: message.TargetGroup, ID: "42"}) {
		t.Fatalf("status %d, message %+v", w.Code, e.lastMessage())
	}
}

type completingEnqueuer struct{ got message.Message }

func (e *completingEnqueuer) EnqueueObserved(_ context.Context, msg message.Message, observe message.DeliveryObserver) (message.DeliveryID, error) {
	e.got = msg
	observe(message.DeliveryUpdate{ID: "qq-1", Account: "qq", State: message.DeliverySent, Attempts: 1, Receipt: message.Receipt{MessageID: "123"}})
	return "qq-1", nil
}

func TestOneBotSendAndStatus(t *testing.T) {
	e := &completingEnqueuer{}
	handler := newHandler(e)
	r := httptest.NewRequest(http.MethodPost, "/api/v1/accounts/qq/send_private_msg", strings.NewReader(`{"user_id":123,"group_id":456,"message":[{"type":"text","data":{"text":"hello"}},{"type":"image","data":{"file":"url"}}],"auto_escape":true}`))
	r.Header.Set("Authorization", bearerToken())
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("send: %d %s", w.Code, w.Body)
	}
	if e.got.GroupID != "456" || !e.got.AutoEscape || !strings.Contains(string(e.got.Content), `"image"`) {
		t.Fatalf("message: %+v", e.got)
	}
	var response struct {
		Data struct {
			MessageID  int64  `json:"message_id"`
			DeliveryID string `json:"delivery_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.Data.MessageID != 123 || response.Data.DeliveryID != "qq-1" {
		t.Fatalf("response: %s (%v)", w.Body, err)
	}
	w = doRequest(t, handler, http.MethodGet, "/api/v1/accounts/qq/message-status/qq-1", "", bearerToken())
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"sent"`) {
		t.Fatalf("status: %d %s", w.Code, w.Body)
	}
}

func TestOneBotAsyncForm(t *testing.T) {
	e := &completingEnqueuer{}
	handler := newHandler(e)
	r := httptest.NewRequest(http.MethodPost, "/api/v1/accounts/qq/send_group_msg_async", strings.NewReader("group_id=42&message=hello"))
	r.Header.Set("Authorization", bearerToken())
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 202 || !strings.Contains(w.Body.String(), `"delivery_id":"qq-1"`) || e.got.Target.ID != "42" {
		t.Fatalf("response: %d %s message=%+v", w.Code, w.Body, e.got)
	}
}
