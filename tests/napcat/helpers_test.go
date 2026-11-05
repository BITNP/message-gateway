package napcat_test

import (
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"message-gateway/internal/message"
	"message-gateway/internal/napcat"
)

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func writeOK(w http.ResponseWriter, data any) {
	writeJSON(w, map[string]any{
		"status":  "ok",
		"retcode": 0,
		"data":    data,
	})
}

func writeRetcode(w http.ResponseWriter, retcode int, text string) {
	writeJSON(w, map[string]any{
		"status":  "failed",
		"retcode": retcode,
		"message": text,
	})
}

func newTestClient(t *testing.T, baseURL string, groupTemp bool) *napcat.Client {
	t.Helper()

	client, err := napcat.NewClient(napcat.Config{
		BaseURL:          baseURL,
		Timeout:          time.Second,
		DirectoryRefresh: time.Hour,
		DirectoryRetry:   time.Millisecond,
		DirectoryTimeout: time.Second,
		GroupTempSession: groupTemp,
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	return client
}

func userMessage(id, text string) message.Message {
	return message.Message{
		Platform: message.PlatformQQ,
		Sender:   "qq",
		Target: message.Target{
			Kind: message.TargetUser,
			ID:   id,
		},
		Text: text,
	}
}

func groupMessage(id, text string) message.Message {
	return message.Message{
		Platform: message.PlatformQQ,
		Sender:   "qq",
		Target: message.Target{
			Kind: message.TargetGroup,
			ID:   id,
		},
		Text: text,
	}
}

type privateBody struct {
	UserID  int64  `json:"user_id"`
	GroupID int64  `json:"group_id"`
	Message string `json:"message"`
}

type groupBody struct {
	GroupID int64  `json:"group_id"`
	Message string `json:"message"`
}

type sendCall struct {
	Path    string
	UserID  int64
	GroupID int64
	Message string
}

type sendRecorder struct {
	mu    sync.Mutex
	calls []sendCall
}

func (r *sendRecorder) add(call sendCall) {
	r.mu.Lock()
	r.calls = append(r.calls, call)
	r.mu.Unlock()
}

func (r *sendRecorder) snapshot() []sendCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]sendCall(nil), r.calls...)
}
