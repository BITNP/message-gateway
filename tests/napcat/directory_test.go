package napcat_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"message-gateway/internal/napcat"
)

func TestSyncDirectorySkipsFailedGroup(t *testing.T) {
	recorder := &sendRecorder{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/get_friend_list":
			writeOK(w, []map[string]int64{})
		case "/get_group_list":
			writeOK(w, []map[string]int64{
				{"group_id": 10},
				{"group_id": 20},
			})
		case "/get_group_member_list":
			var body struct {
				GroupID int64 `json:"group_id"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if body.GroupID == 10 {
				http.Error(w, "group 10 unavailable", http.StatusInternalServerError)
				return
			}
			writeOK(w, []map[string]int64{{"user_id": 3}})
		case "/send_private_msg":
			var body privateBody
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			recorder.add(sendCall{
				Path:    r.URL.Path,
				UserID:  body.UserID,
				GroupID: body.GroupID,
				Message: body.Message,
			})
			writeOK(w, map[string]any{"message_id": 1})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := newTestClient(t, server.URL, true)
	if err := client.SyncDirectory(context.Background()); err != nil {
		t.Fatalf("SyncDirectory() error = %v", err)
	}

	if _, err := client.Send(context.Background(), userMessage("3", "hello")); err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	calls := recorder.snapshot()
	if len(calls) != 1 {
		t.Fatalf("send calls = %d, want 1", len(calls))
	}
	if calls[0].GroupID != 20 {
		t.Fatalf("send group id = %d, want 20", calls[0].GroupID)
	}
}

func TestSyncDirectoryRetainsOldDirectoryOnFailure(t *testing.T) {
	var failGroupList atomic.Bool
	recorder := &sendRecorder{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/get_friend_list":
			writeOK(w, []map[string]int64{{"user_id": 1}})
		case "/get_group_list":
			if failGroupList.Load() {
				http.Error(w, "group list unavailable", http.StatusInternalServerError)
				return
			}
			writeOK(w, []map[string]int64{{"group_id": 10}})
		case "/get_group_member_list":
			writeOK(w, []map[string]int64{
				{"user_id": 1},
				{"user_id": 3},
			})
		case "/send_private_msg":
			var body privateBody
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			recorder.add(sendCall{
				Path:    r.URL.Path,
				UserID:  body.UserID,
				GroupID: body.GroupID,
				Message: body.Message,
			})
			writeOK(w, map[string]any{"message_id": 1})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := newTestClient(t, server.URL, true)
	if err := client.SyncDirectory(context.Background()); err != nil {
		t.Fatalf("first SyncDirectory() error = %v", err)
	}

	failGroupList.Store(true)
	if err := client.SyncDirectory(context.Background()); err == nil {
		t.Fatal("second SyncDirectory() error = nil, want error")
	}

	// User 3 is not a friend, but the old directory still knows that user 3
	// shares group 10 with the bot.
	if _, err := client.Send(context.Background(), userMessage("3", "hello")); err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	calls := recorder.snapshot()
	if len(calls) != 1 || calls[0].GroupID != 10 {
		t.Fatalf("send calls = %+v, want one group-10 attempt", calls)
	}
}

func TestRunRetriesAndRefreshesDirectory(t *testing.T) {
	var friendCalls atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/get_friend_list":
			if friendCalls.Add(1) == 1 {
				http.Error(w, "not ready", http.StatusInternalServerError)
				return
			}
			writeOK(w, []map[string]int64{})
		case "/get_group_list":
			writeOK(w, []map[string]int64{})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := napcat.NewClient(napcat.Config{
		BaseURL:          server.URL,
		Timeout:          time.Second,
		DirectoryRefresh: 10 * time.Millisecond,
		DirectoryRetry:   5 * time.Millisecond,
		DirectoryTimeout: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		client.Run(ctx)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for friendCalls.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := friendCalls.Load(); got < 3 {
		t.Fatalf("friend list calls = %d, want at least 3", got)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run() did not stop after cancel")
	}
}
