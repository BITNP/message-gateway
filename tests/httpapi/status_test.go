package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"message-gateway/internal/httpapi"
	"message-gateway/internal/message"
)

// statusEnqueuer records the observer handed to it so tests can drive
// delivery updates the way a real sender worker would. Ids auto-increment
// unless id is set. preUpdate, when set, is delivered through the observer
// before EnqueueObserved returns, simulating a worker that finishes before
// the handler registers the delivery.
type statusEnqueuer struct {
	mu        sync.Mutex
	seq       int
	id        message.DeliveryID
	err       error
	preUpdate *message.DeliveryUpdate
	observer  message.DeliveryObserver
}

func (f *statusEnqueuer) EnqueueObserved(
	_ context.Context,
	_ message.Message,
	observe message.DeliveryObserver,
) (message.DeliveryID, error) {
	f.mu.Lock()
	if f.err != nil {
		f.mu.Unlock()
		return "", f.err
	}
	f.seq++
	id := f.id
	if id == "" {
		id = message.DeliveryID(fmt.Sprintf("qq-%d", f.seq))
	}
	f.observer = observe
	pre := f.preUpdate
	f.mu.Unlock()

	if pre != nil && observe != nil {
		update := *pre
		update.ID = id
		observe(update)
	}
	return id, nil
}

// deliver runs the recorded observer as the sender worker would.
func (f *statusEnqueuer) deliver(update message.DeliveryUpdate) {
	f.mu.Lock()
	observe := f.observer
	f.mu.Unlock()
	if observe == nil {
		return
	}
	observe(update)
}

func postMessage(t *testing.T, handler http.Handler) string {
	t.Helper()
	recorder := doRequest(t, handler, http.MethodPost, "/api/v1/messages", validBody(), bearerToken())
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("send status = %d, want %d: %s", recorder.Code, http.StatusAccepted, recorder.Body.String())
	}
	var response struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode send response: %v", err)
	}
	return response.ID
}

func fetchStatus(t *testing.T, handler http.Handler, id string) *httptest.ResponseRecorder {
	t.Helper()
	return doRequest(t, handler, http.MethodGet, "/api/v1/messages/"+id, "", bearerToken())
}

type statusResponseBody struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	Attempts  int    `json:"attempts"`
	Reason    string `json:"reason"`
	Error     string `json:"error"`
	RetryInMS int64  `json:"retry_in_ms"`
	Receipt   *struct {
		MessageID string `json:"message_id"`
		SentAt    string `json:"sent_at"`
	} `json:"receipt"`
}

func decodeStatus(t *testing.T, recorder *httptest.ResponseRecorder) statusResponseBody {
	t.Helper()
	var body statusResponseBody
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode status response %q: %v", recorder.Body.String(), err)
	}
	return body
}

func TestStatusReportsQueued(t *testing.T) {
	enqueuer := &statusEnqueuer{}
	handler := newHandler(enqueuer)

	id := postMessage(t, handler)
	recorder := fetchStatus(t, handler, id)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}

	body := decodeStatus(t, recorder)
	if body.ID != id {
		t.Errorf("id = %q, want %q", body.ID, id)
	}
	if body.Status != "queued" {
		t.Errorf("status = %q, want queued", body.Status)
	}

	var raw map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw response: %v", err)
	}
	if _, ok := raw["attempts"]; ok {
		t.Error("queued response must not carry attempts")
	}
}

func TestStatusReportsSent(t *testing.T) {
	enqueuer := &statusEnqueuer{}
	handler := newHandler(enqueuer)
	id := postMessage(t, handler)

	sentAt := time.Date(2026, time.November, 7, 3, 14, 15, 999000000, time.UTC)
	enqueuer.deliver(message.DeliveryUpdate{
		ID:       message.DeliveryID(id),
		State:    message.DeliverySent,
		Attempts: 1,
		Receipt:  message.Receipt{MessageID: "12345", SentAt: sentAt},
	})

	recorder := fetchStatus(t, handler, id)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}

	body := decodeStatus(t, recorder)
	if body.Status != "sent" {
		t.Errorf("status = %q, want sent", body.Status)
	}
	if body.Attempts != 1 {
		t.Errorf("attempts = %d, want 1", body.Attempts)
	}
	if body.Receipt == nil {
		t.Fatal("receipt is missing")
	}
	if body.Receipt.MessageID != "12345" {
		t.Errorf("receipt message id = %q, want 12345", body.Receipt.MessageID)
	}
	if want := sentAt.UTC().Format(time.RFC3339Nano); body.Receipt.SentAt != want {
		t.Errorf("receipt sent_at = %q, want %q", body.Receipt.SentAt, want)
	}
}

func TestStatusReportsRetrying(t *testing.T) {
	enqueuer := &statusEnqueuer{}
	handler := newHandler(enqueuer)
	id := postMessage(t, handler)

	enqueuer.deliver(message.DeliveryUpdate{
		ID:       message.DeliveryID(id),
		State:    message.DeliveryRetrying,
		Attempts: 1,
		Err:      fmt.Errorf("%w: send_group_msg request: connection reset", message.ErrTransient),
		RetryIn:  2 * time.Second,
	})

	recorder := fetchStatus(t, handler, id)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}

	body := decodeStatus(t, recorder)
	if body.Status != "retrying" {
		t.Errorf("status = %q, want retrying", body.Status)
	}
	if body.Attempts != 1 {
		t.Errorf("attempts = %d, want 1", body.Attempts)
	}
	if body.RetryInMS != 2000 {
		t.Errorf("retry_in_ms = %d, want 2000", body.RetryInMS)
	}
	if !strings.Contains(body.Error, "transient send failure") {
		t.Errorf("error = %q, want it to mention the transient failure", body.Error)
	}
}

func TestStatusReportsFailed(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantReason string
	}{
		{
			name:       "permanent",
			err:        fmt.Errorf("%w: send_group_msg failed with retcode 100", message.ErrPermanent),
			wantReason: "permanent",
		},
		{
			name:       "exhausted",
			err:        fmt.Errorf("%w: send_group_msg request: connection reset", message.ErrTransient),
			wantReason: "exhausted",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			enqueuer := &statusEnqueuer{}
			handler := newHandler(enqueuer)
			id := postMessage(t, handler)

			enqueuer.deliver(message.DeliveryUpdate{
				ID:       message.DeliveryID(id),
				State:    message.DeliveryFailed,
				Attempts: 3,
				Err:      tt.err,
			})

			recorder := fetchStatus(t, handler, id)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
			}

			body := decodeStatus(t, recorder)
			if body.Status != "failed" {
				t.Errorf("status = %q, want failed", body.Status)
			}
			if body.Reason != tt.wantReason {
				t.Errorf("reason = %q, want %q", body.Reason, tt.wantReason)
			}
			if body.Attempts != 3 {
				t.Errorf("attempts = %d, want 3", body.Attempts)
			}
			if body.Error == "" {
				t.Error("error is empty, want the send error text")
			}
		})
	}
}

func TestStatusUnknownID(t *testing.T) {
	handler := newHandler(&statusEnqueuer{})

	recorder := fetchStatus(t, handler, "qq-999")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNotFound)
	}
	if !strings.Contains(recorder.Body.String(), "unknown delivery id") {
		t.Errorf("body = %q, want it to mention the unknown id", recorder.Body.String())
	}
}

func TestStatusRequiresAuth(t *testing.T) {
	handler := newHandler(&statusEnqueuer{})

	recorder := doRequest(t, handler, http.MethodGet, "/api/v1/messages/qq-1", "", "")
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
	if recorder.Header().Get("WWW-Authenticate") == "" {
		t.Error("WWW-Authenticate header is missing")
	}
}

func TestStatusEvictsOldest(t *testing.T) {
	enqueuer := &statusEnqueuer{}
	handler := httpapi.New(enqueuer, httpapi.Options{
		SenderKey:      "qq",
		APIKey:         testAPIKey,
		StatusCapacity: 2,
	})

	first := postMessage(t, handler)
	second := postMessage(t, handler)
	third := postMessage(t, handler)

	if code := fetchStatus(t, handler, first).Code; code != http.StatusNotFound {
		t.Errorf("oldest entry status = %d, want %d", code, http.StatusNotFound)
	}
	for _, id := range []string{second, third} {
		if code := fetchStatus(t, handler, id).Code; code != http.StatusOK {
			t.Errorf("entry %s status = %d, want %d", id, code, http.StatusOK)
		}
	}
}

func TestStatusUpdateBeforeRegisterKeepsTerminalState(t *testing.T) {
	sentAt := time.Date(2026, time.November, 7, 3, 14, 15, 0, time.UTC)
	enqueuer := &statusEnqueuer{
		preUpdate: &message.DeliveryUpdate{
			State:    message.DeliverySent,
			Attempts: 1,
			Receipt:  message.Receipt{MessageID: "42", SentAt: sentAt},
		},
	}
	handler := newHandler(enqueuer)

	// The worker update runs before the handler registers the delivery, so
	// register must not downgrade the entry back to queued.
	id := postMessage(t, handler)

	body := decodeStatus(t, fetchStatus(t, handler, id))
	if body.Status != "sent" {
		t.Errorf("status = %q, want sent", body.Status)
	}
	if body.Receipt == nil || body.Receipt.MessageID != "42" {
		t.Errorf("receipt = %+v, want message id 42", body.Receipt)
	}
}

func TestStatusEnqueueRejectionRegistersNothing(t *testing.T) {
	enqueuer := &statusEnqueuer{err: message.ErrQueueFull}
	handler := newHandler(enqueuer)

	recorder := doRequest(t, handler, http.MethodPost, "/api/v1/messages", validBody(), bearerToken())
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("send status = %d, want %d", recorder.Code, http.StatusTooManyRequests)
	}

	if code := fetchStatus(t, handler, "qq-1").Code; code != http.StatusNotFound {
		t.Errorf("status = %d, want %d (rejected sends have no delivery id)", code, http.StatusNotFound)
	}
}
