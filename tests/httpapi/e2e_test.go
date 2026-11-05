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

func TestGatewaySendReachesNapCat(t *testing.T) {
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

	gateway := httptest.NewServer(httpapi.New(manager, "qq", testAPIKey))
	t.Cleanup(gateway.Close)

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
