package napcat_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"message-gateway/internal/napcat"
)

func TestEventsFanoutAndDisconnect(t *testing.T) {
	peers := make(chan *websocket.Conn, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("incorrect upstream authorization")
			w.WriteHeader(401)
			return
		}
		if r.URL.Path == "/get_login_info" {
			fmt.Fprint(w, `{"status":"ok","retcode":0,"data":{"user_id":123,"nickname":"bot"}}`)
			return
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		peers <- c
		<-r.Context().Done()
	}))
	defer server.Close()
	client, err := napcat.NewClient(napcat.Config{BaseURL: server.URL, AccessToken: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	events, err := napcat.NewEvents("ws"+strings.TrimPrefix(server.URL, "http")+"/event", "secret", client, napcat.EventConfig{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { events.Run(ctx); close(done) }()
	var peer *websocket.Conn
	select {
	case peer = <-peers:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream did not connect")
	}
	if _, _, ok := events.Subscribe(); ok {
		t.Fatal("ready before upstream identity was verified")
	}
	if err := peer.Write(ctx, websocket.MessageText, []byte(`{"self_id":123,"post_type":"meta_event","meta_event_type":"lifecycle","sub_type":"connect"}`)); err != nil {
		t.Fatal(err)
	}
	var a, b <-chan []byte
	var stopA, stopB func()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		var ok bool
		a, stopA, ok = events.Subscribe()
		if ok {
			b, stopB, ok = events.Subscribe()
			if ok {
				break
			}
			stopA()
		}
		time.Sleep(time.Millisecond)
	}
	if a == nil || b == nil {
		t.Fatal("event stream never ready")
	}
	defer stopA()
	defer stopB()
	for _, ch := range []<-chan []byte{a, b} {
		select {
		case raw := <-ch:
			if !strings.Contains(string(raw), `"sub_type":"connect"`) {
				t.Fatalf("unexpected lifecycle: %s", raw)
			}
		case <-time.After(time.Second):
			t.Fatal("missing lifecycle")
		}
	}
	payload := `{"time":1,"self_id":123,"post_type":"message","id":9007199254740993,"extra":{"x":true}}`
	if err := peer.Write(ctx, websocket.MessageText, []byte(payload)); err != nil {
		t.Fatal(err)
	}
	for _, ch := range []<-chan []byte{a, b} {
		select {
		case raw := <-ch:
			if string(raw) != payload {
				t.Fatalf("event changed: %s", raw)
			}
		case <-time.After(time.Second):
			t.Fatal("missing event")
		}
	}
	// A changed account identity invalidates existing subscribers.
	if err := peer.Write(ctx, websocket.MessageText, []byte(`{"self_id":456,"post_type":"message"}`)); err != nil {
		t.Fatal(err)
	}
	for _, ch := range []<-chan []byte{a, b} {
		select {
		case _, ok := <-ch:
			if ok {
				t.Fatal("expected closed subscription")
			}
		case <-time.After(time.Second):
			t.Fatal("subscription remained open")
		}
	}
	select {
	case peer = <-peers:
		peer.CloseNow()
	case <-time.After(3 * time.Second):
		t.Fatal("upstream did not reconnect")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not stop")
	}
}

func TestEventsRejectInvalidAddress(t *testing.T) {
	client, _ := napcat.NewClient(napcat.Config{BaseURL: "http://localhost"})
	for _, address := range []string{"https://example.com/event", "ws://user:pass@example.com/", "ws://example.com/?token=secret", "ws://example.com/#fragment"} {
		if _, err := napcat.NewEvents(address, "", client, napcat.EventConfig{}); err == nil {
			t.Errorf("accepted %q", address)
		}
	}
}
