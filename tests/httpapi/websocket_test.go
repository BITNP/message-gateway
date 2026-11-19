package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"message-gateway/internal/httpapi"
	"message-gateway/internal/message"
)

type testEvents struct{ events chan []byte }

func (e *testEvents) Subscribe() (<-chan []byte, func(), bool) { return e.events, func() {}, true }

func TestWebSocketRolesAndEcho(t *testing.T) {
	events := &testEvents{events: make(chan []byte, 2)}
	proxy := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/get_login_info" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","retcode":0,"data":{"user_id":9007199254740993},"extension":true}`))
	})
	handler := newTestServer(&fakeEnqueuer{}, httpapi.Options{Accounts: map[string]httpapi.Account{"qq": {Platform: message.PlatformQQ, Proxy: proxy, Events: events}}})
	server := httptest.NewServer(handler)
	defer server.Close()
	defer handler.CloseWebSockets()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	connect := func(path string) *websocket.Conn {
		t.Helper()
		conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+path, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": []string{"Bearer " + testAccountToken}}})
		if err != nil {
			t.Fatal(err)
		}
		return conn
	}
	conn := connect("/api/v1/accounts/qq/ws")
	defer conn.CloseNow()
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"action":"get_login_info","echo":{"id":9007199254740993}}`)); err != nil {
		t.Fatal(err)
	}
	_, response, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(response, &got); err != nil {
		t.Fatal(err)
	}
	if string(got["echo"]) != `{"id":9007199254740993}` || string(got["data"]) != `{"user_id":9007199254740993}` || string(got["extension"]) != "true" {
		t.Fatalf("response: %s", response)
	}
	events.events <- []byte(`{"post_type":"message","self_id":9007199254740993}`)
	_, event, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if string(event) != `{"post_type":"message","self_id":9007199254740993}` {
		t.Fatalf("event: %s", event)
	}
	close(events.events)
	if _, _, err = conn.Read(ctx); err == nil {
		t.Fatal("expected closed subscription")
	}
}

func TestWebSocketAuthenticationAndUnavailableEvents(t *testing.T) {
	handler := newHandler(&fakeEnqueuer{})
	server := httptest.NewServer(handler)
	defer server.Close()
	defer handler.CloseWebSockets()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	base := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/v1/accounts/qq/ws"
	_, resp, err := websocket.Dial(ctx, base+"/api", nil)
	if err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("missing token: response %v err %v", resp, err)
	}
	_, resp, err = websocket.Dial(ctx, base+"/api?access_token="+testAccountToken, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": []string{"Bearer incorrect"}}})
	if err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("invalid header with valid query: response %v err %v", resp, err)
	}
	_, resp, err = websocket.Dial(ctx, base+"?access_token="+testAccountToken, nil)
	if err == nil || resp == nil || resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("unavailable events: response %v err %v", resp, err)
	}
}

func TestWebSocketActionCanCompleteWhileSendWaits(t *testing.T) {
	proxy := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok","retcode":0,"data":{}}`))
	})
	handler := newTestServer(&fakeEnqueuer{}, httpapi.Options{Accounts: map[string]httpapi.Account{"qq": {Platform: message.PlatformQQ, Proxy: proxy}}})
	server := httptest.NewServer(handler)
	defer server.Close()
	defer handler.CloseWebSockets()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/api/v1/accounts/qq/ws/api?access_token="+testAccountToken, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	for _, request := range []string{
		`{"action":"send_private_msg","params":{"user_id":1,"message":"hello"},"echo":"send"}`,
		`{"action":"get_login_info","echo":"info"}`,
	} {
		if err := conn.Write(ctx, websocket.MessageText, []byte(request)); err != nil {
			t.Fatal(err)
		}
	}
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if string(result["echo"]) != `"info"` {
		t.Fatalf("response = %s", data)
	}
}

func TestWebSocketSendsShareQueueInOrder(t *testing.T) {
	queue := &fakeEnqueuer{}
	handler := newHandler(queue)
	server := httptest.NewServer(handler)
	defer server.Close()
	defer handler.CloseWebSockets()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/api/v1/accounts/qq/ws/api?access_token="+testAccountToken, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	for _, request := range []string{
		`{"action":"send_private_msg_async","params":{"user_id":1,"message":"first"},"echo":1}`,
		`{"action":"send_private_msg_async","params":{"user_id":1,"message":"second"},"echo":2}`,
	} {
		if err := conn.Write(ctx, websocket.MessageText, []byte(request)); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		if _, _, err := conn.Read(ctx); err != nil {
			t.Fatal(err)
		}
	}
	resp, err := http.Post(server.URL+"/api/v1/accounts/qq/http/send_private_msg_async?access_token="+testAccountToken, "application/json", strings.NewReader(`{"user_id":1,"message":"third"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if len(queue.messages) != 3 {
		t.Fatalf("messages: %d", len(queue.messages))
	}
	for i, want := range []string{`"first"`, `"second"`, `"third"`} {
		if string(queue.messages[i].Content) != want {
			t.Fatalf("message %d: %s", i, queue.messages[i].Content)
		}
	}
}

func TestWebSocketAccountLimitAndShutdown(t *testing.T) {
	queue := &fakeEnqueuer{}
	handler := newTestServer(queue, httpapi.Options{Accounts: map[string]httpapi.Account{"qq": {Platform: message.PlatformQQ}}, WebSocketLimits: httpapi.WebSocketLimits{ActionsPerAccount: 1}})
	server := httptest.NewServer(handler)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/api/v1/accounts/qq/ws/api?access_token="+testAccountToken, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"action":"send_private_msg","params":{"user_id":1,"message":"wait"}}`)); err != nil {
		t.Fatal(err)
	}
	for queue.callCount() == 0 {
		select {
		case <-ctx.Done():
			t.Fatal("send did not enter queue")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"action":"get_login_info","echo":{"id":1}}`)); err != nil {
		t.Fatal(err)
	}
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if string(result["retcode"]) != "1429" || string(result["echo"]) != `{"id":1}` {
		t.Fatalf("response = %s", data)
	}
	handler.CloseWebSockets()
	if err := handler.WaitWebSockets(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}
