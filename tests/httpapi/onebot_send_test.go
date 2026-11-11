package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"message-gateway/internal/message"
)

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
			MessageID  string `json:"message_id"`
			DeliveryID string `json:"delivery_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.Data.MessageID != "123" || response.Data.DeliveryID != "qq-1" {
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
