package message_test

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"message-gateway/internal/message"
	"message-gateway/internal/napcat"
)

type integrationSender struct {
	inner message.Sender

	mu       sync.Mutex
	receipt  message.Receipt
	err      error
	finished chan struct{}
}

func newIntegrationSender(inner message.Sender) *integrationSender {
	return &integrationSender{
		inner:    inner,
		finished: make(chan struct{}, 1),
	}
}

func (s *integrationSender) Send(ctx context.Context, msg message.Message) (message.Receipt, error) {
	receipt, err := s.inner.Send(ctx, msg)

	s.mu.Lock()
	s.receipt = receipt
	s.err = err
	s.mu.Unlock()

	select {
	case s.finished <- struct{}{}:
	default:
	}
	return receipt, err
}

func (s *integrationSender) result() (message.Receipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.receipt, s.err
}

func TestManagerNapCatIntegration(t *testing.T) {
	targetID := os.Getenv("NAPCAT_INTEGRATION_TARGET")
	if targetID == "" {
		t.Skip("set NAPCAT_INTEGRATION_TARGET to run the NapCat integration test")
	}

	baseURL := os.Getenv("NAPCAT_INTEGRATION_BASE_URL")
	if baseURL == "" {
		baseURL = "http://127.0.0.1:3001"
	}
	token := os.Getenv("NAPCAT_INTEGRATION_TOKEN")
	text := os.Getenv("NAPCAT_INTEGRATION_TEXT")
	if text == "" {
		text = "Clinic backend internal sender integration test."
	}

	if _, err := strconv.ParseInt(targetID, 10, 64); err != nil {
		t.Fatalf("NAPCAT_INTEGRATION_TARGET = %q, want a numeric QQ id", targetID)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client, err := napcat.NewClient(napcat.Config{
		BaseURL:          baseURL,
		AccessToken:      token,
		Timeout:          15 * time.Second,
		DirectoryRefresh: time.Hour,
		DirectoryRetry:   time.Second,
		DirectoryTimeout: 30 * time.Second,
		GroupTempSession: true,
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	info, err := client.Check(ctx)
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	t.Logf("NapCat account: %d (%s)", info.UserID, info.Nickname)

	if err := client.SyncDirectory(ctx); err != nil {
		t.Fatalf("SyncDirectory() error = %v", err)
	}

	recorder := newIntegrationSender(client)
	mgr, err := message.NewManager(message.Config{
		QueueSize:    4,
		SendTimeout:  30 * time.Second,
		MaxAttempts:  1,
		RetryBackoff: 0,
		MinGap:       0,
	})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	if err := mgr.Register("qq", recorder, message.FixedGap{}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		mgr.Run(ctx)
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("manager did not stop")
		}
	}()

	deliveryID, err := mgr.Enqueue(ctx, message.Message{
		Platform: message.PlatformQQ,
		Sender:   "qq",
		Target: message.Target{
			Kind: message.TargetUser,
			ID:   targetID,
		},
		Text: text,
	})
	if err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	t.Logf("queued delivery %s", deliveryID)

	select {
	case <-recorder.finished:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for NapCat send: %v", ctx.Err())
	}

	receipt, sendErr := recorder.result()
	if sendErr != nil {
		t.Fatalf("NapCat send error = %v", sendErr)
	}
	if receipt.MessageID == "" {
		t.Fatal("NapCat returned an empty message id")
	}
	t.Logf("sent message id %s", receipt.MessageID)
	fmt.Printf("sent QQ message to %s, message id %s\n", targetID, receipt.MessageID)
}
