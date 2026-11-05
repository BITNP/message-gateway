package message_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"message-gateway/internal/message"
)

type fakeSender struct {
	mu    sync.Mutex
	calls []message.Message
	times []time.Time
	sent  chan message.Message
	fn    func(context.Context, message.Message) (message.Receipt, error)
}

func newFakeSender() *fakeSender {
	return &fakeSender{sent: make(chan message.Message, 32)}
}

func (f *fakeSender) Send(ctx context.Context, msg message.Message) (message.Receipt, error) {
	f.mu.Lock()
	f.calls = append(f.calls, msg)
	f.times = append(f.times, time.Now())
	f.mu.Unlock()

	f.sent <- msg
	if f.fn != nil {
		return f.fn(ctx, msg)
	}
	return message.Receipt{MessageID: "ok", SentAt: time.Now()}, nil
}

func (f *fakeSender) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeSender) sentTimes() []time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Time(nil), f.times...)
}

func testConfig() message.Config {
	return message.Config{
		QueueSize:    8,
		SendTimeout:  time.Second,
		MaxAttempts:  3,
		RetryBackoff: time.Millisecond,
		MinGap:       0,
	}
}

func testMessage(sender, text string) message.Message {
	return message.Message{
		Platform: message.PlatformQQ,
		Sender:   sender,
		Target: message.Target{
			Kind: message.TargetUser,
			ID:   "10001",
		},
		Text: text,
	}
}

func mustManager(t *testing.T, cfg message.Config) *message.Manager {
	t.Helper()
	mgr, err := message.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	return mgr
}

func mustRegister(t *testing.T, mgr *message.Manager, key string, sender message.Sender, policy message.GapPolicy) {
	t.Helper()
	if err := mgr.Register(key, sender, policy); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
}

func mustEnqueue(t *testing.T, mgr *message.Manager, msg message.Message) {
	t.Helper()
	if _, err := mgr.Enqueue(context.Background(), msg); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
}

func startManager(t *testing.T, mgr *message.Manager) (context.CancelFunc, <-chan struct{}) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		mgr.Run(ctx)
	}()

	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Errorf("manager did not stop")
		}
	})
	return cancel, done
}

func waitForSend(t *testing.T, ch <-chan message.Message) message.Message {
	t.Helper()
	select {
	case msg := <-ch:
		return msg
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for send")
		return message.Message{}
	}
}

func TestManagerSendsInFIFOOrder(t *testing.T) {
	mgr := mustManager(t, testConfig())
	sender := newFakeSender()
	mustRegister(t, mgr, "qq", sender, message.FixedGap{})
	startManager(t, mgr)

	want := []string{"one", "two", "three"}
	for _, text := range want {
		mustEnqueue(t, mgr, testMessage("qq", text))
	}

	for i, text := range want {
		got := waitForSend(t, sender.sent)
		if got.Text != text {
			t.Fatalf("send %d text = %q, want %q", i, got.Text, text)
		}
	}
}

func TestManagerSendersAreIndependent(t *testing.T) {
	mgr := mustManager(t, testConfig())

	release := make(chan struct{})
	slow := newFakeSender()
	slow.fn = func(ctx context.Context, _ message.Message) (message.Receipt, error) {
		select {
		case <-release:
			return message.Receipt{MessageID: "slow"}, nil
		case <-ctx.Done():
			return message.Receipt{}, ctx.Err()
		}
	}

	fast := newFakeSender()
	mustRegister(t, mgr, "slow", slow, message.FixedGap{})
	mustRegister(t, mgr, "fast", fast, message.FixedGap{})
	startManager(t, mgr)

	mustEnqueue(t, mgr, testMessage("slow", "slow message"))
	mustEnqueue(t, mgr, testMessage("fast", "fast message"))

	// Wait until the slow worker has entered Send and is blocked.
	waitForSend(t, slow.sent)

	// The fast sender must not be blocked by the slow one.
	got := waitForSend(t, fast.sent)
	if got.Sender != "fast" {
		t.Fatalf("send sender = %q, want fast", got.Sender)
	}

	close(release)
}

func TestManagerQueueFull(t *testing.T) {
	cfg := testConfig()
	cfg.QueueSize = 1

	mgr := mustManager(t, cfg)
	sender := newFakeSender()
	mustRegister(t, mgr, "qq", sender, message.FixedGap{})

	if _, err := mgr.Enqueue(context.Background(), testMessage("qq", "first")); err != nil {
		t.Fatalf("first Enqueue() error = %v", err)
	}

	_, err := mgr.Enqueue(context.Background(), testMessage("qq", "second"))
	if !errors.Is(err, message.ErrQueueFull) {
		t.Fatalf("second Enqueue() error = %v, want ErrQueueFull", err)
	}
}

func TestManagerEnqueueErrors(t *testing.T) {
	t.Run("unknown sender", func(t *testing.T) {
		mgr := mustManager(t, testConfig())
		_, err := mgr.Enqueue(context.Background(), testMessage("qq", "hello"))
		if !errors.Is(err, message.ErrUnknownSender) {
			t.Fatalf("Enqueue() error = %v, want ErrUnknownSender", err)
		}
	})

	t.Run("invalid message", func(t *testing.T) {
		mgr := mustManager(t, testConfig())
		mustRegister(t, mgr, "qq", newFakeSender(), message.FixedGap{})

		msg := testMessage("qq", "")
		_, err := mgr.Enqueue(context.Background(), msg)
		if !errors.Is(err, message.ErrInvalidMessage) {
			t.Fatalf("Enqueue() error = %v, want ErrInvalidMessage", err)
		}
	})

	t.Run("canceled context", func(t *testing.T) {
		mgr := mustManager(t, testConfig())
		mustRegister(t, mgr, "qq", newFakeSender(), message.FixedGap{})

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := mgr.Enqueue(ctx, testMessage("qq", "hello"))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Enqueue() error = %v, want context.Canceled", err)
		}
	})
}

func TestNewManagerRejectsInvalidConfig(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*message.Config)
	}{
		{
			name: "zero queue size",
			mutate: func(c *message.Config) {
				c.QueueSize = 0
			},
		},
		{
			name: "zero send timeout",
			mutate: func(c *message.Config) {
				c.SendTimeout = 0
			},
		},
		{
			name: "zero max attempts",
			mutate: func(c *message.Config) {
				c.MaxAttempts = 0
			},
		},
		{
			name: "negative retry backoff",
			mutate: func(c *message.Config) {
				c.RetryBackoff = -time.Second
			},
		},
		{
			name: "negative minimum gap",
			mutate: func(c *message.Config) {
				c.MinGap = -time.Second
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testConfig()
			tt.mutate(&cfg)

			if _, err := message.NewManager(cfg); err == nil {
				t.Fatal("NewManager() error = nil, want error")
			}
		})
	}
}

func TestManagerRegisterValidation(t *testing.T) {
	mgr := mustManager(t, testConfig())
	sender := newFakeSender()

	if err := mgr.Register("", sender, message.FixedGap{}); err == nil {
		t.Fatal("Register(empty key) error = nil, want error")
	}
	if err := mgr.Register("qq", nil, message.FixedGap{}); err == nil {
		t.Fatal("Register(nil sender) error = nil, want error")
	}
	if err := mgr.Register("qq", sender, nil); err == nil {
		t.Fatal("Register(nil policy) error = nil, want error")
	}

	mustRegister(t, mgr, "qq", sender, message.FixedGap{})
	if err := mgr.Register("qq", sender, message.FixedGap{}); err == nil {
		t.Fatal("Register(duplicate) error = nil, want error")
	}
}

func TestManagerRejectsRegisterAfterRun(t *testing.T) {
	mgr := mustManager(t, testConfig())

	started := make(chan struct{}, 1)
	sender := newFakeSender()
	sender.fn = func(ctx context.Context, _ message.Message) (message.Receipt, error) {
		started <- struct{}{}
		<-ctx.Done()
		return message.Receipt{}, ctx.Err()
	}

	mustRegister(t, mgr, "qq", sender, message.FixedGap{})
	startManager(t, mgr)
	mustEnqueue(t, mgr, testMessage("qq", "start"))

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for send to start")
	}

	if err := mgr.Register("other", newFakeSender(), message.FixedGap{}); err == nil {
		t.Fatal("Register() after Run error = nil, want error")
	}
}

func TestManagerRetriesTransientFailure(t *testing.T) {
	cfg := testConfig()
	cfg.MaxAttempts = 3
	cfg.RetryBackoff = time.Millisecond

	mgr := mustManager(t, cfg)
	sender := newFakeSender()

	var attempts atomic.Int32
	sender.fn = func(context.Context, message.Message) (message.Receipt, error) {
		if attempts.Add(1) == 1 {
			return message.Receipt{}, fmt.Errorf("%w: temporary", message.ErrTransient)
		}
		return message.Receipt{MessageID: "ok"}, nil
	}

	mustRegister(t, mgr, "qq", sender, message.FixedGap{})
	startManager(t, mgr)
	mustEnqueue(t, mgr, testMessage("qq", "retry me"))

	waitForSend(t, sender.sent)
	waitForSend(t, sender.sent)

	if got := attempts.Load(); got != 2 {
		t.Fatalf("attempts = %d, want 2", got)
	}

	mustEnqueue(t, mgr, testMessage("qq", "next"))
	waitForSend(t, sender.sent)

	if got := attempts.Load(); got != 3 {
		t.Fatalf("attempts after next message = %d, want 3", got)
	}
}

func TestManagerDropsPermanentFailure(t *testing.T) {
	mgr := mustManager(t, testConfig())
	sender := newFakeSender()
	sender.fn = func(_ context.Context, msg message.Message) (message.Receipt, error) {
		if msg.Text == "bad" {
			return message.Receipt{}, fmt.Errorf("%w: invalid target", message.ErrPermanent)
		}
		return message.Receipt{MessageID: "ok"}, nil
	}

	mustRegister(t, mgr, "qq", sender, message.FixedGap{})
	startManager(t, mgr)

	mustEnqueue(t, mgr, testMessage("qq", "bad"))
	mustEnqueue(t, mgr, testMessage("qq", "good"))

	if got := waitForSend(t, sender.sent); got.Text != "bad" {
		t.Fatalf("first send = %q, want bad", got.Text)
	}
	if got := waitForSend(t, sender.sent); got.Text != "good" {
		t.Fatalf("second send = %q, want good", got.Text)
	}
	if got := sender.callCount(); got != 2 {
		t.Fatalf("call count = %d, want 2", got)
	}
}

func TestManagerDropsAfterMaxAttempts(t *testing.T) {
	cfg := testConfig()
	cfg.MaxAttempts = 2
	cfg.RetryBackoff = time.Millisecond

	mgr := mustManager(t, cfg)
	sender := newFakeSender()
	sender.fn = func(context.Context, message.Message) (message.Receipt, error) {
		return message.Receipt{}, fmt.Errorf("%w: temporary", message.ErrTransient)
	}

	mustRegister(t, mgr, "qq", sender, message.FixedGap{})
	startManager(t, mgr)
	mustEnqueue(t, mgr, testMessage("qq", "first"))

	waitForSend(t, sender.sent)
	waitForSend(t, sender.sent)

	mustEnqueue(t, mgr, testMessage("qq", "second"))
	if got := waitForSend(t, sender.sent); got.Text != "second" {
		t.Fatalf("send after drop = %q, want second", got.Text)
	}
}

func TestManagerShutdown(t *testing.T) {
	mgr := mustManager(t, testConfig())
	sender := newFakeSender()

	started := make(chan struct{}, 1)
	sender.fn = func(ctx context.Context, _ message.Message) (message.Receipt, error) {
		started <- struct{}{}
		<-ctx.Done()
		return message.Receipt{}, ctx.Err()
	}

	mustRegister(t, mgr, "qq", sender, message.FixedGap{})
	cancel, done := startManager(t, mgr)
	mustEnqueue(t, mgr, testMessage("qq", "in flight"))

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for send to start")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("manager did not stop after cancel")
	}

	if _, err := mgr.Enqueue(context.Background(), testMessage("qq", "after")); !errors.Is(err, message.ErrClosed) {
		t.Fatalf("Enqueue() after shutdown error = %v, want ErrClosed", err)
	}
}
