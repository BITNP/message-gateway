package httpapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"message-gateway/internal/fakenapcat"
	"message-gateway/internal/httpapi"
	"message-gateway/internal/message"
	"message-gateway/internal/napcat"
)

func newEndToEndGateway(t *testing.T) (*fakenapcat.Server, *httptest.Server) {
	t.Helper()

	fake, err := fakenapcat.NewServer(fakenapcat.Config{
		Friends:        []int64{10001},
		Groups:         []int64{20001},
		Members:        []int64{10001},
		MessageIDStart: 123,
	})
	if err != nil {
		t.Fatalf("fakenapcat.NewServer() error = %v", err)
	}

	napcatServer := httptest.NewServer(fake.Handler())
	t.Cleanup(napcatServer.Close)

	client, err := napcat.NewClient(napcat.Config{
		BaseURL:          napcatServer.URL,
		Timeout:          time.Second,
		DirectoryRefresh: time.Hour,
		DirectoryRetry:   time.Millisecond,
		DirectoryTimeout: time.Second,
		GroupTempSession: false,
	})
	if err != nil {
		t.Fatalf("napcat.NewClient() error = %v", err)
	}

	manager, err := message.NewManager(message.Config{
		QueueSize:    4,
		SendTimeout:  time.Second,
		MaxAttempts:  1,
		RetryBackoff: 0,
		MinGap:       0,
	})
	if err != nil {
		t.Fatalf("message.NewManager() error = %v", err)
	}

	gap, err := message.NewFixedGap(0, 0)
	if err != nil {
		t.Fatalf("message.NewFixedGap() error = %v", err)
	}
	if err := manager.Register("qq", client, gap); err != nil {
		t.Fatalf("manager.Register() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	managerDone := make(chan struct{})
	go func() {
		defer close(managerDone)
		manager.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-managerDone:
		case <-time.After(2 * time.Second):
			t.Error("manager did not stop")
		}
	})

	api := newTestServer(manager, httpapi.Options{
		Accounts: map[string]httpapi.Account{
			"qq": {
				Name:     "qq",
				Driver:   "napcat",
				Platform: message.PlatformQQ,
			},
		},
	})
	gateway := httptest.NewServer(api)
	t.Cleanup(gateway.Close)
	t.Cleanup(api.CloseWebSockets)
	return fake, gateway
}

func TestGatewaySendReachesNapCat(t *testing.T) {
	fake, gateway := newEndToEndGateway(t)

	body := `{"platform":"qq","target":{"kind":"user","id":"10001"},"text":"hello from gateway"}`
	request, err := http.NewRequest(http.MethodPost, gateway.URL+"/api/v1/messages", strings.NewReader(body))
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	request.Header.Set("Authorization", bearerToken())
	request.Header.Set("Content-Type", "application/json")

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("send request: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusAccepted)
	}

	select {
	case call := <-fake.Sent():
		if call.Kind != "private" {
			t.Errorf("kind = %q, want private", call.Kind)
		}
		if call.UserID != 10001 {
			t.Errorf("user id = %d, want 10001", call.UserID)
		}
		if call.GroupID != 0 {
			t.Errorf("group id = %d, want 0", call.GroupID)
		}
		if call.Text != "hello from gateway" {
			t.Errorf("message = %q, want hello from gateway", call.Text)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for NapCat send")
	}
}

func TestOneBotSendReachesNapCat(t *testing.T) {
	fake, gateway := newEndToEndGateway(t)
	request, err := http.NewRequest(http.MethodPost, gateway.URL+"/api/v1/accounts/qq/send_private_msg", strings.NewReader(`{"user_id":10001,"message":"hello from OneBot"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", bearerToken())
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
	select {
	case call := <-fake.Sent():
		if call.Kind != "private" || call.UserID != 10001 || call.Text != "hello from OneBot" {
			t.Fatalf("unexpected send: %+v", call)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for NapCat send")
	}
}

func TestGatewayWebSocketSendReachesNapCat(t *testing.T) {
	fake, gateway := newEndToEndGateway(t)

	conn := dialWebSocket(t, gateway, testAccountToken)
	_ = readFrame(t, conn) // ready

	writeFrame(t, conn, sendFramePayload("r-1", "hello over websocket"))

	queued := decodeFrame[queuedFrame](t, readFrame(t, conn))
	if queued.Type != "queued" {
		t.Fatalf("first frame = %q, want queued", queued.Type)
	}

	sent := decodeFrame[sentFrame](t, readFrame(t, conn))
	if sent.Type != "sent" {
		t.Fatalf("second frame = %q, want sent", sent.Type)
	}
	if sent.ID != queued.ID {
		t.Errorf("sent id = %q, want %q", sent.ID, queued.ID)
	}
	if sent.MessageID != "123" {
		t.Errorf("message id = %q, want 123", sent.MessageID)
	}
	if sent.SentAt == "" {
		t.Error("sent_at is empty")
	}

	select {
	case call := <-fake.Sent():
		if call.Kind != "private" {
			t.Errorf("kind = %q, want private", call.Kind)
		}
		if call.UserID != 10001 {
			t.Errorf("user id = %d, want 10001", call.UserID)
		}
		if call.Text != "hello over websocket" {
			t.Errorf("message = %q, want hello over websocket", call.Text)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for NapCat send")
	}
}
