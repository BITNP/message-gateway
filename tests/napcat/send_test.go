package napcat_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"message-gateway/internal/message"
)

func TestSendPrivateToFriendSkipsGroupSession(t *testing.T) {
	recorder := &sendRecorder{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/get_friend_list":
			writeOK(w, []map[string]int64{{"user_id": 1}})
		case "/get_group_list":
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
			writeOK(w, map[string]any{"message_id": 7})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := newTestClient(t, server.URL, true)
	if err := client.SyncDirectory(context.Background()); err != nil {
		t.Fatalf("SyncDirectory() error = %v", err)
	}

	receipt, err := client.Send(context.Background(), userMessage("1", "hello"))
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if receipt.MessageID != "7" {
		t.Fatalf("Receipt.MessageID = %q, want 7", receipt.MessageID)
	}

	calls := recorder.snapshot()
	if len(calls) != 1 {
		t.Fatalf("send calls = %d, want 1", len(calls))
	}
	if calls[0].UserID != 1 || calls[0].GroupID != 0 {
		t.Fatalf("send call = %+v, want user 1 without group id", calls[0])
	}
}

func TestSendPrivateToNonFriendViaGroupTempSession(t *testing.T) {
	recorder := &sendRecorder{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/get_friend_list":
			writeOK(w, []map[string]int64{})
		case "/get_group_list":
			// Deliberately unsorted; the client must try group 10 first.
			writeOK(w, []map[string]int64{
				{"group_id": 20},
				{"group_id": 10},
			})
		case "/get_group_member_list":
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
			if body.GroupID == 10 {
				writeRetcode(w, 100, "temporary session unavailable")
				return
			}
			writeOK(w, map[string]any{"message_id": 8})
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
	if len(calls) != 2 {
		t.Fatalf("send calls = %d, want 2", len(calls))
	}
	if calls[0].GroupID != 10 || calls[1].GroupID != 20 {
		t.Fatalf("group attempts = [%d, %d], want [10, 20]", calls[0].GroupID, calls[1].GroupID)
	}
	if calls[0].UserID != 3 || calls[1].UserID != 3 {
		t.Fatalf("send calls used wrong user: %+v", calls)
	}
}

func TestSendPrivateFallsBackToPlainMessage(t *testing.T) {
	recorder := &sendRecorder{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/get_friend_list":
			writeOK(w, []map[string]int64{})
		case "/get_group_list":
			writeOK(w, []map[string]int64{})
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
			writeRetcode(w, 100, "not a friend")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := newTestClient(t, server.URL, true)
	if err := client.SyncDirectory(context.Background()); err != nil {
		t.Fatalf("SyncDirectory() error = %v", err)
	}

	_, err := client.Send(context.Background(), userMessage("4", "hello"))
	if !errors.Is(err, message.ErrPermanent) {
		t.Fatalf("Send() error = %v, want ErrPermanent", err)
	}

	calls := recorder.snapshot()
	if len(calls) != 1 || calls[0].GroupID != 0 {
		t.Fatalf("send calls = %+v, want one plain private attempt", calls)
	}
}

func TestSendPrivateWhenDirectoryNotLoaded(t *testing.T) {
	recorder := &sendRecorder{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/send_private_msg" {
			http.NotFound(w, r)
			return
		}
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
		http.Error(w, "napcat unavailable", http.StatusInternalServerError)
	}))
	defer server.Close()

	client := newTestClient(t, server.URL, true)
	_, err := client.Send(context.Background(), userMessage("3", "hello"))
	if !errors.Is(err, message.ErrTransient) {
		t.Fatalf("Send() error = %v, want ErrTransient", err)
	}

	calls := recorder.snapshot()
	if len(calls) != 1 || calls[0].GroupID != 0 {
		t.Fatalf("send calls = %+v, want one plain private attempt", calls)
	}
}

func TestSendPrivateGroupTempDisabled(t *testing.T) {
	recorder := &sendRecorder{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/get_friend_list":
			writeOK(w, []map[string]int64{})
		case "/get_group_list":
			writeOK(w, []map[string]int64{{"group_id": 10}})
		case "/get_group_member_list":
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
			writeOK(w, map[string]any{"message_id": 9})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := newTestClient(t, server.URL, false)
	if err := client.SyncDirectory(context.Background()); err != nil {
		t.Fatalf("SyncDirectory() error = %v", err)
	}

	if _, err := client.Send(context.Background(), userMessage("3", "hello")); err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	calls := recorder.snapshot()
	if len(calls) != 1 || calls[0].GroupID != 0 {
		t.Fatalf("send calls = %+v, want one plain private attempt", calls)
	}
}

func TestSendGroup(t *testing.T) {
	recorder := &sendRecorder{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/send_group_msg" {
			http.NotFound(w, r)
			return
		}
		var body groupBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		recorder.add(sendCall{
			Path:    r.URL.Path,
			GroupID: body.GroupID,
			Message: body.Message,
		})
		// NapCat may return message_id as a string; flexID must accept it.
		writeOK(w, map[string]any{"message_id": "10"})
	}))
	defer server.Close()

	client := newTestClient(t, server.URL, true)
	receipt, err := client.Send(context.Background(), groupMessage("99", "hello group"))
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if receipt.MessageID != "10" {
		t.Fatalf("Receipt.MessageID = %q, want 10", receipt.MessageID)
	}

	calls := recorder.snapshot()
	if len(calls) != 1 || calls[0].GroupID != 99 || calls[0].Message != "hello group" {
		t.Fatalf("send calls = %+v, want group 99", calls)
	}
}

func TestSendRejectsInvalidTarget(t *testing.T) {
	recorder := &sendRecorder{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.add(sendCall{Path: r.URL.Path})
		writeOK(w, map[string]any{})
	}))
	defer server.Close()

	client := newTestClient(t, server.URL, true)
	_, err := client.Send(context.Background(), userMessage("not-a-number", "hello"))
	if !errors.Is(err, message.ErrPermanent) {
		t.Fatalf("Send() error = %v, want ErrPermanent", err)
	}
	if calls := recorder.snapshot(); len(calls) != 0 {
		t.Fatalf("send calls = %+v, want none", calls)
	}
}

func TestSendPreservesSegmentsAndExplicitGroup(t *testing.T) {
	var got struct {
		UserID     int64           `json:"user_id"`
		GroupID    int64           `json:"group_id"`
		Message    json.RawMessage `json:"message"`
		AutoEscape bool            `json:"auto_escape"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		writeOK(w, map[string]any{"message_id": 42})
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, true)
	msg := userMessage("3", "")
	msg.Content = json.RawMessage(`[{"type":"text","data":{"text":"hello"}},{"type":"image","data":{"file":"https://example.com/a.png"}}]`)
	msg.GroupID = "10"
	msg.AutoEscape = true
	if _, err := client.Send(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if got.UserID != 3 || got.GroupID != 10 || !got.AutoEscape || string(got.Message) != string(msg.Content) {
		t.Fatalf("unexpected request: %+v", got)
	}
}
