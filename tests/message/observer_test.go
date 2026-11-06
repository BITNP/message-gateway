package message_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"message-gateway/internal/message"
)

// updateLog records delivery updates in callback order. The callback runs on
// the sender's worker goroutine, so the log is mutex-protected.
type updateLog struct {
	mu      sync.Mutex
	updates []message.DeliveryUpdate
	ch      chan message.DeliveryUpdate
}

func newUpdateLog() *updateLog {
	return &updateLog{ch: make(chan message.DeliveryUpdate, 32)}
}

func (l *updateLog) observe(update message.DeliveryUpdate) {
	l.mu.Lock()
	l.updates = append(l.updates, update)
	l.mu.Unlock()
	l.ch <- update
}

func (l *updateLog) snapshot() []message.DeliveryUpdate {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]message.DeliveryUpdate(nil), l.updates...)
}

func waitForUpdate(t *testing.T, ch <-chan message.DeliveryUpdate) message.DeliveryUpdate {
	t.Helper()
	select {
	case update := <-ch:
		return update
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for delivery update")
		return message.DeliveryUpdate{}
	}
}

func TestEnqueueObservedReportsSent(t *testing.T) {
	mgr := mustManager(t, testConfig())
	sender := newFakeSender()
	mustRegister(t, mgr, "qq", sender, message.FixedGap{})
	startManager(t, mgr)

	log := newUpdateLog()
	id, err := mgr.EnqueueObserved(
		context.Background(),
		testMessage("qq", "hello"),
		log.observe,
	)
	if err != nil {
		t.Fatalf("EnqueueObserved() error = %v", err)
	}

	update := waitForUpdate(t, log.ch)
	if update.ID != id {
		t.Errorf("update id = %q, want %q", update.ID, id)
	}
	if update.State != message.DeliverySent {
		t.Errorf("state = %q, want %q", update.State, message.DeliverySent)
	}
	if update.Attempts != 1 {
		t.Errorf("attempts = %d, want 1", update.Attempts)
	}
	if update.Receipt.MessageID != "ok" {
		t.Errorf("receipt message id = %q, want ok", update.Receipt.MessageID)
	}
	if update.Err != nil {
		t.Errorf("error = %v, want nil", update.Err)
	}

	// A later delivery is a barrier: updates are emitted in queue order by one
	// worker, so the first delivery cannot report again after this point.
	sentinel := newUpdateLog()
	if _, err := mgr.EnqueueObserved(
		context.Background(),
		testMessage("qq", "sentinel"),
		sentinel.observe,
	); err != nil {
		t.Fatalf("sentinel EnqueueObserved() error = %v", err)
	}
	if got := waitForUpdate(t, sentinel.ch); got.State != message.DeliverySent {
		t.Fatalf("sentinel state = %q, want sent", got.State)
	}

	if updates := log.snapshot(); len(updates) != 1 {
		t.Fatalf("updates after send = %d, want 1", len(updates))
	}
}

func TestEnqueueObservedReportsRetryThenSent(t *testing.T) {
	mgr := mustManager(t, testConfig())
	sender := newFakeSender()

	var attempts int
	sender.fn = func(context.Context, message.Message) (message.Receipt, error) {
		attempts++
		if attempts == 1 {
			return message.Receipt{}, fmt.Errorf("%w: temporary", message.ErrTransient)
		}
		return message.Receipt{MessageID: "ok"}, nil
	}

	mustRegister(t, mgr, "qq", sender, message.FixedGap{})
	startManager(t, mgr)

	log := newUpdateLog()
	if _, err := mgr.EnqueueObserved(
		context.Background(),
		testMessage("qq", "retry me"),
		log.observe,
	); err != nil {
		t.Fatalf("EnqueueObserved() error = %v", err)
	}

	retry := waitForUpdate(t, log.ch)
	if retry.State != message.DeliveryRetrying {
		t.Fatalf("first state = %q, want %q", retry.State, message.DeliveryRetrying)
	}
	if retry.Attempts != 1 {
		t.Errorf("retry attempts = %d, want 1", retry.Attempts)
	}
	if !errors.Is(retry.Err, message.ErrTransient) {
		t.Errorf("retry error = %v, want ErrTransient", retry.Err)
	}
	if retry.RetryIn != time.Millisecond {
		t.Errorf("retry delay = %v, want 1ms", retry.RetryIn)
	}

	sent := waitForUpdate(t, log.ch)
	if sent.State != message.DeliverySent {
		t.Fatalf("second state = %q, want %q", sent.State, message.DeliverySent)
	}
	if sent.Attempts != 2 {
		t.Errorf("sent attempts = %d, want 2", sent.Attempts)
	}
}

func TestEnqueueObservedReportsPermanentFailure(t *testing.T) {
	cfg := testConfig()
	cfg.MaxAttempts = 3

	mgr := mustManager(t, cfg)
	sender := newFakeSender()
	sender.fn = func(context.Context, message.Message) (message.Receipt, error) {
		return message.Receipt{}, fmt.Errorf("%w: bad target", message.ErrPermanent)
	}
	mustRegister(t, mgr, "qq", sender, message.FixedGap{})
	startManager(t, mgr)

	log := newUpdateLog()
	if _, err := mgr.EnqueueObserved(
		context.Background(),
		testMessage("qq", "bad"),
		log.observe,
	); err != nil {
		t.Fatalf("EnqueueObserved() error = %v", err)
	}

	update := waitForUpdate(t, log.ch)
	if update.State != message.DeliveryFailed {
		t.Fatalf("state = %q, want %q", update.State, message.DeliveryFailed)
	}
	if update.Attempts != 1 {
		t.Errorf("attempts = %d, want 1: permanent errors must not retry", update.Attempts)
	}
	if !errors.Is(update.Err, message.ErrPermanent) {
		t.Errorf("error = %v, want ErrPermanent", update.Err)
	}
}

func TestEnqueueObservedReportsExhaustedRetries(t *testing.T) {
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

	log := newUpdateLog()
	id, err := mgr.EnqueueObserved(
		context.Background(),
		testMessage("qq", "never"),
		log.observe,
	)
	if err != nil {
		t.Fatalf("EnqueueObserved() error = %v", err)
	}

	retry := waitForUpdate(t, log.ch)
	if retry.State != message.DeliveryRetrying || retry.Attempts != 1 {
		t.Fatalf("retry update = %+v, want retrying attempt 1", retry)
	}

	failed := waitForUpdate(t, log.ch)
	if failed.ID != id {
		t.Errorf("failed id = %q, want %q", failed.ID, id)
	}
	if failed.State != message.DeliveryFailed {
		t.Fatalf("state = %q, want %q", failed.State, message.DeliveryFailed)
	}
	if failed.Attempts != 2 {
		t.Errorf("attempts = %d, want 2", failed.Attempts)
	}

	// Barrier: the queue advanced past this delivery.
	sentinel := newUpdateLog()
	if _, err := mgr.EnqueueObserved(
		context.Background(),
		testMessage("qq", "sentinel"),
		sentinel.observe,
	); err != nil {
		t.Fatalf("sentinel EnqueueObserved() error = %v", err)
	}
	waitForUpdate(t, sentinel.ch)

	if updates := log.snapshot(); len(updates) != 2 {
		t.Fatalf("updates = %d, want 2 (one retry, one failure)", len(updates))
	}
}

func TestEnqueueObservedReportsEachDeliveryOnce(t *testing.T) {
	mgr := mustManager(t, testConfig())
	sender := newFakeSender()
	mustRegister(t, mgr, "qq", sender, message.FixedGap{})
	startManager(t, mgr)

	log := newUpdateLog()
	const count = 4
	want := make(map[message.DeliveryID]bool, count)
	for i := 0; i < count; i++ {
		id, err := mgr.EnqueueObserved(
			context.Background(),
			testMessage("qq", fmt.Sprintf("message %d", i)),
			log.observe,
		)
		if err != nil {
			t.Fatalf("EnqueueObserved(%d) error = %v", i, err)
		}
		want[id] = true
	}

	seen := make(map[message.DeliveryID]int, count)
	for i := 0; i < count; i++ {
		update := waitForUpdate(t, log.ch)
		if update.State != message.DeliverySent {
			t.Fatalf("update %d state = %q, want sent", i, update.State)
		}
		seen[update.ID]++
	}

	for id := range want {
		if seen[id] != 1 {
			t.Errorf("delivery %q reported %d times, want 1", id, seen[id])
		}
	}
	if len(seen) != count {
		t.Errorf("reported ids = %d, want %d", len(seen), count)
	}
	if updates := log.snapshot(); len(updates) != count {
		t.Errorf("updates = %d, want %d", len(updates), count)
	}
}

func TestEnqueueObservedNotCalledOnEnqueueError(t *testing.T) {
	rejected := func(t *testing.T) message.DeliveryObserver {
		return func(update message.DeliveryUpdate) {
			t.Errorf("observer called for rejected delivery: %+v", update)
		}
	}

	t.Run("queue full", func(t *testing.T) {
		cfg := testConfig()
		cfg.QueueSize = 1

		mgr := mustManager(t, cfg)
		mustRegister(t, mgr, "qq", newFakeSender(), message.FixedGap{})

		if _, err := mgr.Enqueue(context.Background(), testMessage("qq", "first")); err != nil {
			t.Fatalf("first Enqueue() error = %v", err)
		}

		id, err := mgr.EnqueueObserved(
			context.Background(),
			testMessage("qq", "second"),
			rejected(t),
		)
		if !errors.Is(err, message.ErrQueueFull) {
			t.Fatalf("EnqueueObserved() error = %v, want ErrQueueFull", err)
		}
		if id != "" {
			t.Errorf("id = %q, want empty", id)
		}
	})

	t.Run("invalid message", func(t *testing.T) {
		mgr := mustManager(t, testConfig())
		mustRegister(t, mgr, "qq", newFakeSender(), message.FixedGap{})

		_, err := mgr.EnqueueObserved(
			context.Background(),
			testMessage("qq", "   "),
			rejected(t),
		)
		if !errors.Is(err, message.ErrInvalidMessage) {
			t.Fatalf("EnqueueObserved() error = %v, want ErrInvalidMessage", err)
		}
	})

	t.Run("unknown sender", func(t *testing.T) {
		mgr := mustManager(t, testConfig())

		_, err := mgr.EnqueueObserved(
			context.Background(),
			testMessage("qq", "hello"),
			rejected(t),
		)
		if !errors.Is(err, message.ErrUnknownSender) {
			t.Fatalf("EnqueueObserved() error = %v, want ErrUnknownSender", err)
		}
	})

	t.Run("closed manager", func(t *testing.T) {
		mgr := mustManager(t, testConfig())
		mustRegister(t, mgr, "qq", newFakeSender(), message.FixedGap{})
		cancel, done := startManager(t, mgr)
		cancel()
		<-done

		_, err := mgr.EnqueueObserved(
			context.Background(),
			testMessage("qq", "hello"),
			rejected(t),
		)
		if !errors.Is(err, message.ErrClosed) {
			t.Fatalf("EnqueueObserved() error = %v, want ErrClosed", err)
		}
	})
}

func TestEnqueueObservedNotCalledOnShutdown(t *testing.T) {
	mgr := mustManager(t, testConfig())
	sender := newFakeSender()

	started := make(chan struct{})
	sender.fn = func(ctx context.Context, _ message.Message) (message.Receipt, error) {
		close(started)
		<-ctx.Done()
		return message.Receipt{}, ctx.Err()
	}

	mustRegister(t, mgr, "qq", sender, message.FixedGap{})
	cancel, done := startManager(t, mgr)

	log := newUpdateLog()
	if _, err := mgr.EnqueueObserved(
		context.Background(),
		testMessage("qq", "in flight"),
		log.observe,
	); err != nil {
		t.Fatalf("EnqueueObserved() error = %v", err)
	}

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

	// Run has returned, so no worker can still be emitting updates.
	if updates := log.snapshot(); len(updates) != 0 {
		t.Fatalf("updates after shutdown = %d, want 0", len(updates))
	}
}

func TestEnqueueObservedNilObserver(t *testing.T) {
	mgr := mustManager(t, testConfig())
	sender := newFakeSender()
	mustRegister(t, mgr, "qq", sender, message.FixedGap{})
	startManager(t, mgr)

	if _, err := mgr.EnqueueObserved(
		context.Background(),
		testMessage("qq", "no observer"),
		nil,
	); err != nil {
		t.Fatalf("EnqueueObserved() error = %v", err)
	}

	if got := waitForSend(t, sender.sent); got.Text != "no observer" {
		t.Fatalf("sent text = %q, want no observer", got.Text)
	}
}
