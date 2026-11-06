package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"message-gateway/internal/httpapi"
	"message-gateway/internal/message"
)

type queuedFrame struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	ID        string `json:"id"`
}

type retryingFrame struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	ID        string `json:"id"`
	Attempt   int    `json:"attempt"`
	Error     string `json:"error"`
	RetryInMS int64  `json:"retry_in_ms"`
}

type sentFrame struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	ID        string `json:"id"`
	MessageID string `json:"message_id"`
	SentAt    string `json:"sent_at"`
}

type failedFrame struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	ID        string `json:"id"`
	Attempts  int    `json:"attempts"`
	Reason    string `json:"reason"`
	Error     string `json:"error"`
}

type funcSender struct {
	fn func(context.Context, message.Message) (message.Receipt, error)
}

func (s funcSender) Send(ctx context.Context, msg message.Message) (message.Receipt, error) {
	return s.fn(ctx, msg)
}

func sendConfig() message.Config {
	return message.Config{
		QueueSize:    8,
		SendTimeout:  time.Second,
		MaxAttempts:  3,
		RetryBackoff: 5 * time.Millisecond,
		MinGap:       0,
	}
}

func newSendGateway(
	t *testing.T,
	cfg message.Config,
	sender message.Sender,
) (*httptest.Server, *httpapi.Server) {
	t.Helper()

	mgr, err := message.NewManager(cfg)
	if err != nil {
		t.Fatalf("message.NewManager() error = %v", err)
	}

	gap, err := message.NewFixedGap(0, 0)
	if err != nil {
		t.Fatalf("message.NewFixedGap() error = %v", err)
	}
	if err := mgr.Register("qq", sender, gap); err != nil {
		t.Fatalf("manager.Register() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		mgr.Run(ctx)
	}()

	srv, api := newWebSocketServerWith(t, mgr, httpapi.Options{SenderKey: "qq"})
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("manager did not stop")
		}
	})
	return srv, api
}

func sendFramePayload(requestID, text string) string {
	return fmt.Sprintf(
		`{"type":"send","request_id":%q,"platform":"qq",`+
			`"target":{"kind":"user","id":"10001"},"text":%q}`,
		requestID,
		text,
	)
}

func TestWebSocketQueuedBeforeSent(t *testing.T) {
	sendDone := make(chan struct{})
	var once sync.Once

	srv, _ := newSendGateway(t, sendConfig(), funcSender{
		fn: func(context.Context, message.Message) (message.Receipt, error) {
			once.Do(func() { close(sendDone) })
			return message.Receipt{MessageID: "m-1", SentAt: time.Now()}, nil
		},
	})

	conn := dialWebSocket(t, srv, testAPIKey)
	_ = readFrame(t, conn) // ready

	writeFrame(t, conn, sendFramePayload("r-1", "hello"))

	// The sender already completed, so a naive implementation could write
	// "sent" first. "queued" must still come first.
	select {
	case <-sendDone:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the send")
	}

	queued := decodeFrame[queuedFrame](t, readFrame(t, conn))
	if queued.Type != "queued" {
		t.Fatalf("first frame = %q, want queued", queued.Type)
	}
	if queued.RequestID != "r-1" {
		t.Errorf("request id = %q, want r-1", queued.RequestID)
	}
	if queued.ID == "" {
		t.Fatal("delivery id is empty")
	}

	sent := decodeFrame[sentFrame](t, readFrame(t, conn))
	if sent.Type != "sent" {
		t.Fatalf("second frame = %q, want sent", sent.Type)
	}
	if sent.ID != queued.ID {
		t.Errorf("sent id = %q, want %q", sent.ID, queued.ID)
	}
	if sent.RequestID != "r-1" {
		t.Errorf("request id = %q, want r-1", sent.RequestID)
	}
	if sent.MessageID != "m-1" {
		t.Errorf("message id = %q, want m-1", sent.MessageID)
	}
	if sent.SentAt == "" {
		t.Error("sent_at is empty")
	}
}

func TestWebSocketAcknowledgesBeforeSending(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once

	cfg := sendConfig()
	cfg.SendTimeout = 5 * time.Second

	srv, _ := newSendGateway(t, cfg, funcSender{
		fn: func(ctx context.Context, _ message.Message) (message.Receipt, error) {
			once.Do(func() { close(started) })
			select {
			case <-release:
				return message.Receipt{MessageID: "m-1", SentAt: time.Now()}, nil
			case <-ctx.Done():
				return message.Receipt{}, ctx.Err()
			}
		},
	})

	conn := dialWebSocket(t, srv, testAPIKey)
	_ = readFrame(t, conn) // ready

	writeFrame(t, conn, sendFramePayload("r-1", "hello"))

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the send attempt")
	}

	// "queued" must arrive while the send is still in flight.
	queued := decodeFrame[queuedFrame](t, readFrame(t, conn))
	if queued.Type != "queued" {
		t.Fatalf("first frame = %q, want queued", queued.Type)
	}

	close(release)

	sent := decodeFrame[sentFrame](t, readFrame(t, conn))
	if sent.Type != "sent" {
		t.Fatalf("second frame = %q, want sent", sent.Type)
	}
	if sent.ID != queued.ID {
		t.Errorf("sent id = %q, want %q", sent.ID, queued.ID)
	}
}

func TestWebSocketReportsRetryThenSent(t *testing.T) {
	var attempts atomic.Int32
	srv, _ := newSendGateway(t, sendConfig(), funcSender{
		fn: func(context.Context, message.Message) (message.Receipt, error) {
			if attempts.Add(1) == 1 {
				return message.Receipt{}, fmt.Errorf("%w: temporary", message.ErrTransient)
			}
			return message.Receipt{MessageID: "m-2", SentAt: time.Now()}, nil
		},
	})

	conn := dialWebSocket(t, srv, testAPIKey)
	_ = readFrame(t, conn) // ready

	writeFrame(t, conn, sendFramePayload("r-1", "retry me"))

	queued := decodeFrame[queuedFrame](t, readFrame(t, conn))
	if queued.Type != "queued" {
		t.Fatalf("first frame = %q, want queued", queued.Type)
	}

	retry := decodeFrame[retryingFrame](t, readFrame(t, conn))
	if retry.Type != "retrying" {
		t.Fatalf("second frame = %q, want retrying", retry.Type)
	}
	if retry.ID != queued.ID {
		t.Errorf("retrying id = %q, want %q", retry.ID, queued.ID)
	}
	if retry.Attempt != 1 {
		t.Errorf("attempt = %d, want 1", retry.Attempt)
	}
	if retry.RetryInMS != 5 {
		t.Errorf("retry_in_ms = %d, want 5", retry.RetryInMS)
	}
	if retry.Error == "" {
		t.Error("retrying error is empty")
	}

	sent := decodeFrame[sentFrame](t, readFrame(t, conn))
	if sent.Type != "sent" {
		t.Fatalf("third frame = %q, want sent", sent.Type)
	}
	if sent.MessageID != "m-2" {
		t.Errorf("message id = %q, want m-2", sent.MessageID)
	}
}

func TestWebSocketReportsPermanentFailure(t *testing.T) {
	srv, _ := newSendGateway(t, sendConfig(), funcSender{
		fn: func(context.Context, message.Message) (message.Receipt, error) {
			return message.Receipt{}, fmt.Errorf("%w: bad target", message.ErrPermanent)
		},
	})

	conn := dialWebSocket(t, srv, testAPIKey)
	_ = readFrame(t, conn) // ready

	writeFrame(t, conn, sendFramePayload("r-1", "hello"))

	queued := decodeFrame[queuedFrame](t, readFrame(t, conn))
	failed := decodeFrame[failedFrame](t, readFrame(t, conn))

	if failed.Type != "failed" {
		t.Fatalf("frame type = %q, want failed", failed.Type)
	}
	if failed.ID != queued.ID {
		t.Errorf("failed id = %q, want %q", failed.ID, queued.ID)
	}
	if failed.Attempts != 1 {
		t.Errorf("attempts = %d, want 1", failed.Attempts)
	}
	if failed.Reason != "permanent" {
		t.Errorf("reason = %q, want permanent", failed.Reason)
	}
	if failed.Error == "" {
		t.Error("failed error is empty")
	}
}

func TestWebSocketReportsExhaustedRetries(t *testing.T) {
	cfg := sendConfig()
	cfg.MaxAttempts = 2

	srv, _ := newSendGateway(t, cfg, funcSender{
		fn: func(context.Context, message.Message) (message.Receipt, error) {
			return message.Receipt{}, fmt.Errorf("%w: temporary", message.ErrTransient)
		},
	})

	conn := dialWebSocket(t, srv, testAPIKey)
	_ = readFrame(t, conn) // ready

	writeFrame(t, conn, sendFramePayload("r-1", "never"))

	_ = decodeFrame[queuedFrame](t, readFrame(t, conn))
	retry := decodeFrame[retryingFrame](t, readFrame(t, conn))
	if retry.Attempt != 1 {
		t.Errorf("attempt = %d, want 1", retry.Attempt)
	}

	failed := decodeFrame[failedFrame](t, readFrame(t, conn))
	if failed.Attempts != 2 {
		t.Errorf("attempts = %d, want 2", failed.Attempts)
	}
	if failed.Reason != "exhausted" {
		t.Errorf("reason = %q, want exhausted", failed.Reason)
	}
	if failed.RequestID != "r-1" {
		t.Errorf("request id = %q, want r-1", failed.RequestID)
	}
}

func TestWebSocketSendMapsMessage(t *testing.T) {
	enqueuer := &fakeEnqueuer{id: "qq-9"}
	srv, _ := newWebSocketServerWith(t, enqueuer, httpapi.Options{SenderKey: "qq-main"})

	conn := dialWebSocket(t, srv, testAPIKey)
	_ = readFrame(t, conn) // ready

	writeFrame(t, conn, `{"type":"send","request_id":"r-1","platform":" QQ ",`+
		`"target":{"kind":" GROUP ","id":" 20002 "},"text":"group hello"}`)

	queued := decodeFrame[queuedFrame](t, readFrame(t, conn))
	if queued.ID != "qq-9" {
		t.Errorf("id = %q, want qq-9", queued.ID)
	}

	if enqueuer.callCount() != 1 {
		t.Fatalf("enqueuer calls = %d, want 1", enqueuer.callCount())
	}
	msg := enqueuer.lastMessage()
	if msg.Platform != message.PlatformQQ {
		t.Errorf("platform = %q, want qq", msg.Platform)
	}
	if msg.Sender != "qq-main" {
		t.Errorf("sender = %q, want qq-main", msg.Sender)
	}
	if msg.Target.Kind != message.TargetGroup {
		t.Errorf("target kind = %q, want group", msg.Target.Kind)
	}
	if msg.Target.ID != "20002" {
		t.Errorf("target id = %q, want 20002", msg.Target.ID)
	}
	if msg.Text != "group hello" {
		t.Errorf("text = %q, want group hello", msg.Text)
	}
}

func TestWebSocketSendRejectsInvalidMessages(t *testing.T) {
	tests := []struct {
		name    string
		payload string
	}{
		{
			name:    "missing platform",
			payload: `{"type":"send","request_id":"r-1","target":{"kind":"user","id":"10001"},"text":"hi"}`,
		},
		{
			name:    "blank text",
			payload: `{"type":"send","request_id":"r-1","platform":"qq","target":{"kind":"user","id":"10001"},"text":"   "}`,
		},
		{
			name:    "bad target kind",
			payload: `{"type":"send","request_id":"r-1","platform":"qq","target":{"kind":"channel","id":"10001"},"text":"hi"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			enqueuer := &fakeEnqueuer{}
			srv, _ := newWebSocketServerWith(t, enqueuer, httpapi.Options{})
			conn := dialWebSocket(t, srv, testAPIKey)
			_ = readFrame(t, conn) // ready

			writeFrame(t, conn, tt.payload)

			frame := decodeFrame[errorFrame](t, readFrame(t, conn))
			if frame.Type != "error" {
				t.Fatalf("frame type = %q, want error", frame.Type)
			}
			if frame.Code != "bad_request" {
				t.Errorf("code = %q, want bad_request", frame.Code)
			}
			if frame.RequestID != "r-1" {
				t.Errorf("request id = %q, want r-1", frame.RequestID)
			}
			if enqueuer.callCount() != 0 {
				t.Errorf("enqueuer calls = %d, want 0", enqueuer.callCount())
			}
		})
	}
}

func TestWebSocketSendMapsEnqueueErrors(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		wantCode    string
		wantRetryMS int64
	}{
		{
			name:        "queue full",
			err:         message.ErrQueueFull,
			wantCode:    "queue_full",
			wantRetryMS: 1000,
		},
		{
			name:     "closed",
			err:      message.ErrClosed,
			wantCode: "service_unavailable",
		},
		{
			name:     "unexpected",
			err:      errors.New("boom"),
			wantCode: "internal",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			enqueuer := &fakeEnqueuer{err: tt.err}
			srv, _ := newWebSocketServerWith(t, enqueuer, httpapi.Options{})
			conn := dialWebSocket(t, srv, testAPIKey)
			_ = readFrame(t, conn) // ready

			writeFrame(t, conn, sendFramePayload("r-1", "hello"))

			frame := decodeFrame[errorFrame](t, readFrame(t, conn))
			if frame.Code != tt.wantCode {
				t.Errorf("code = %q, want %q", frame.Code, tt.wantCode)
			}
			if frame.RetryAfterMS != tt.wantRetryMS {
				t.Errorf("retry_after_ms = %d, want %d", frame.RetryAfterMS, tt.wantRetryMS)
			}
			if frame.RequestID != "r-1" {
				t.Errorf("request id = %q, want r-1", frame.RequestID)
			}
		})
	}
}

func TestWebSocketPipelinesRequests(t *testing.T) {
	var sent int64
	srv, _ := newSendGateway(t, sendConfig(), funcSender{
		fn: func(context.Context, message.Message) (message.Receipt, error) {
			id := atomic.AddInt64(&sent, 1)
			return message.Receipt{
				MessageID: fmt.Sprintf("m-%d", id),
				SentAt:    time.Now(),
			}, nil
		},
	})

	conn := dialWebSocket(t, srv, testAPIKey)
	_ = readFrame(t, conn) // ready

	const count = 3
	for i := 1; i <= count; i++ {
		writeFrame(t, conn, sendFramePayload(fmt.Sprintf("r-%d", i), fmt.Sprintf("message %d", i)))
	}

	queued := make(map[string]bool, count)
	terminated := make(map[string]bool, count)

	for i := 0; i < count*2; i++ {
		data := readFrame(t, conn)

		var envelope struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(data, &envelope); err != nil {
			t.Fatalf("decode frame %q: %v", data, err)
		}

		switch envelope.Type {
		case "queued":
			frame := decodeFrame[queuedFrame](t, data)
			if queued[frame.ID] {
				t.Errorf("duplicate queued for %q", frame.ID)
			}
			queued[frame.ID] = true
		case "sent":
			frame := decodeFrame[sentFrame](t, data)
			if !queued[frame.ID] {
				t.Errorf("sent before queued for %q", frame.ID)
			}
			terminated[frame.ID] = true
		default:
			t.Fatalf("unexpected frame %q", data)
		}
	}

	if len(queued) != count {
		t.Errorf("queued deliveries = %d, want %d", len(queued), count)
	}
	if len(terminated) != count {
		t.Errorf("terminated deliveries = %d, want %d", len(terminated), count)
	}
}

func TestWebSocketDeliveriesAreScopedToTheConnection(t *testing.T) {
	srv, _ := newSendGateway(t, sendConfig(), funcSender{
		fn: func(context.Context, message.Message) (message.Receipt, error) {
			return message.Receipt{MessageID: "m-1", SentAt: time.Now()}, nil
		},
	})

	first := dialWebSocket(t, srv, testAPIKey)
	_ = readFrame(t, first) // ready

	second := dialWebSocket(t, srv, testAPIKey)
	_ = readFrame(t, second) // ready

	writeFrame(t, first, sendFramePayload("r-1", "hello"))
	_ = readFrame(t, first) // queued
	_ = readFrame(t, first) // sent

	// If delivery events leaked across connections, the foreign frames would
	// arrive before this reply.
	writeFrame(t, second, `{"type":"nope","request_id":"r-b"}`)

	frame := decodeFrame[errorFrame](t, readFrame(t, second))
	if frame.RequestID != "r-b" {
		t.Fatalf("second connection received a foreign frame: %+v", frame)
	}
}
