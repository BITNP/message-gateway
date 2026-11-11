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

type readyAccount struct {
	Name   string `json:"name"`
	Driver string `json:"driver"`
}

type readyFrame struct {
	Type     string         `json:"type"`
	Version  int            `json:"version"`
	Accounts []readyAccount `json:"accounts"`
}

type errorFrame struct {
	Type         string `json:"type"`
	RequestID    string `json:"request_id"`
	Code         string `json:"code"`
	Message      string `json:"message"`
	RetryAfterMS int64  `json:"retry_after_ms"`
}

func webSocketEndpoint(srv *httptest.Server) string {
	return "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/v1/ws"
}

func newWebSocketServer(t *testing.T, opts httpapi.Options) (*httptest.Server, *httpapi.Server) {
	t.Helper()
	return newWebSocketServerWith(t, &fakeEnqueuer{}, opts)
}

func newWebSocketServerWith(
	t *testing.T,
	enqueuer httpapi.Enqueuer,
	opts httpapi.Options,
) (*httptest.Server, *httpapi.Server) {
	t.Helper()

	if len(opts.Accounts) == 0 {
		opts.Accounts = map[string]httpapi.Account{
			"qq": {
				Name:     "qq",
				Driver:   "napcat",
				Platform: message.PlatformQQ,
			},
		}
	}
	api := newTestServer(enqueuer, opts)
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	t.Cleanup(api.CloseWebSockets)
	return srv, api
}

func dialWebSocket(t *testing.T, srv *httptest.Server, token string) *websocket.Conn {
	t.Helper()

	conn, _, err := dialWebSocketResponse(srv, token)
	if err != nil {
		t.Fatalf("websocket.Dial() error = %v", err)
	}
	t.Cleanup(func() {
		_ = conn.CloseNow()
	})
	return conn
}

func dialWebSocketResponse(
	srv *httptest.Server,
	token string,
) (*websocket.Conn, *http.Response, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var header http.Header
	if token != "" {
		header = http.Header{"Authorization": []string{"Bearer " + token}}
	}

	return websocket.Dial(
		ctx,
		webSocketEndpoint(srv),
		&websocket.DialOptions{HTTPHeader: header},
	)
}

func readFrame(t *testing.T, conn *websocket.Conn) []byte {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	typ, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if typ != websocket.MessageText {
		t.Fatalf("frame type = %v, want text", typ)
	}
	return data
}

func writeFrame(t *testing.T, conn *websocket.Conn, payload string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := conn.Write(ctx, websocket.MessageText, []byte(payload)); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
}

func decodeFrame[T any](t *testing.T, data []byte) T {
	t.Helper()

	var value T
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatalf("decode frame %q: %v", data, err)
	}
	return value
}

func TestWebSocketRequiresAuth(t *testing.T) {
	srv, _ := newWebSocketServer(t, httpapi.Options{})

	tests := []struct {
		name  string
		token string
		want  int
	}{
		{name: "missing", token: "", want: http.StatusUnauthorized},
		{name: "wrong", token: "wrong", want: http.StatusForbidden},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn, resp, err := dialWebSocketResponse(srv, tt.token)
			if err == nil {
				_ = conn.CloseNow()
				t.Fatal("Dial() error = nil, want authentication failure")
			}
			if resp == nil {
				t.Fatalf("Dial() response = nil, want HTTP response: %v", err)
			}
			if resp.StatusCode != tt.want {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tt.want)
			}
		})
	}
}

func TestWebSocketRejectsNonUpgradeRequest(t *testing.T) {
	handler := newHandler(&fakeEnqueuer{})
	recorder := doRequest(t, handler, http.MethodGet, "/api/v1/ws", "", bearerToken())

	if recorder.Code != http.StatusUpgradeRequired {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUpgradeRequired)
	}
}

func TestWebSocketReadyFrame(t *testing.T) {
	srv, _ := newWebSocketServer(t, httpapi.Options{
		Accounts: map[string]httpapi.Account{
			"qq-main": {
				Name:     "qq-main",
				Driver:   "napcat",
				Platform: message.PlatformQQ,
			},
		},
	})
	conn := dialWebSocket(t, srv, testAccountToken)

	ready := decodeFrame[readyFrame](t, readFrame(t, conn))
	if ready.Type != "ready" {
		t.Errorf("type = %q, want ready", ready.Type)
	}
	if ready.Version != 1 {
		t.Errorf("version = %d, want 1", ready.Version)
	}
	if len(ready.Accounts) != 1 {
		t.Fatalf("accounts length = %d, want 1", len(ready.Accounts))
	}
	if ready.Accounts[0].Name != "qq-main" || ready.Accounts[0].Driver != "napcat" {
		t.Errorf("account = %+v", ready.Accounts[0])
	}
}

func TestWebSocketRejectsUnknownFrames(t *testing.T) {
	srv, _ := newWebSocketServer(t, httpapi.Options{})
	conn := dialWebSocket(t, srv, testAccountToken)
	_ = readFrame(t, conn) // ready

	writeFrame(t, conn, `{"type":"nope","request_id":"r-1"}`)
	frame := decodeFrame[errorFrame](t, readFrame(t, conn))
	if frame.Type != "error" {
		t.Errorf("type = %q, want error", frame.Type)
	}
	if frame.Code != "bad_request" {
		t.Errorf("code = %q, want bad_request", frame.Code)
	}
	if frame.RequestID != "r-1" {
		t.Errorf("request id = %q, want r-1", frame.RequestID)
	}

	// The connection stays usable after a rejected frame.
	writeFrame(t, conn, `{"type":"nope","request_id":"r-2"}`)
	frame = decodeFrame[errorFrame](t, readFrame(t, conn))
	if frame.RequestID != "r-2" {
		t.Errorf("request id = %q, want r-2", frame.RequestID)
	}

	writeFrame(t, conn, `not json`)
	frame = decodeFrame[errorFrame](t, readFrame(t, conn))
	if frame.Code != "bad_request" {
		t.Errorf("code = %q, want bad_request", frame.Code)
	}
}

func TestWebSocketRejectsBinaryFrames(t *testing.T) {
	srv, _ := newWebSocketServer(t, httpapi.Options{})
	conn := dialWebSocket(t, srv, testAccountToken)
	_ = readFrame(t, conn) // ready

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := conn.Write(ctx, websocket.MessageBinary, []byte{1, 2, 3}); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	_, _, err := conn.Read(ctx)
	if status := websocket.CloseStatus(err); status != websocket.StatusUnsupportedData {
		t.Fatalf("close status = %v, want %v (error %v)",
			status, websocket.StatusUnsupportedData, err)
	}
}

func TestWebSocketMaxConnections(t *testing.T) {
	srv, _ := newWebSocketServer(t, httpapi.Options{MaxConnections: 1})

	first := dialWebSocket(t, srv, testAccountToken)
	_ = readFrame(t, first) // ready

	conn, resp, err := dialWebSocketResponse(srv, testAccountToken)
	if err == nil {
		_ = conn.CloseNow()
		t.Fatal("second Dial() error = nil, want rejection")
	}
	if resp == nil || resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("second Dial() status = %v, want %d", resp, http.StatusServiceUnavailable)
	}

	// The existing connection is unaffected.
	writeFrame(t, first, `{"type":"nope","request_id":"r-1"}`)
	frame := decodeFrame[errorFrame](t, readFrame(t, first))
	if frame.Code != "bad_request" {
		t.Errorf("code = %q, want bad_request", frame.Code)
	}
}

func TestWebSocketKeepaliveSurvivesServerTimeouts(t *testing.T) {
	api := newTestServer(&fakeEnqueuer{}, httpapi.Options{
		Accounts: map[string]httpapi.Account{
			"qq": {
				Name:     "qq",
				Driver:   "napcat",
				Platform: message.PlatformQQ,
			},
		},
		PingInterval: 20 * time.Millisecond,
	})
	srv := httptest.NewUnstartedServer(api)
	srv.Config.ReadTimeout = 100 * time.Millisecond
	srv.Config.WriteTimeout = 100 * time.Millisecond
	srv.Start()
	t.Cleanup(srv.Close)
	t.Cleanup(api.CloseWebSockets)

	conn := dialWebSocket(t, srv, testAccountToken)
	_ = readFrame(t, conn) // ready

	// The reader also answers server pings; a stalled reader would make the
	// ping fail.
	frames := make(chan []byte, 8)
	go func() {
		for {
			_, data, err := conn.Read(context.Background())
			if err != nil {
				return
			}
			select {
			case frames <- data:
			default:
			}
		}
	}()

	// Idle across several ping intervals and the server's request timeouts.
	time.Sleep(400 * time.Millisecond)

	writeFrame(t, conn, `{"type":"nope","request_id":"r-1"}`)

	select {
	case data := <-frames:
		frame := decodeFrame[errorFrame](t, data)
		if frame.RequestID != "r-1" {
			t.Errorf("request id = %q, want r-1", frame.RequestID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a frame after the idle period")
	}
}

func TestWebSocketCloseWebSockets(t *testing.T) {
	srv, api := newWebSocketServer(t, httpapi.Options{})
	conn := dialWebSocket(t, srv, testAccountToken)
	_ = readFrame(t, conn) // ready

	readErr := make(chan error, 1)
	go func() {
		_, _, err := conn.Read(context.Background())
		readErr <- err
	}()

	api.CloseWebSockets()

	select {
	case err := <-readErr:
		if status := websocket.CloseStatus(err); status != websocket.StatusGoingAway {
			t.Fatalf("close status = %v, want %v (error %v)",
				status, websocket.StatusGoingAway, err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for close")
	}
}
