package message_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"message-gateway/internal/message"
)

func TestManagerEnforcesConfiguredGap(t *testing.T) {
	mgr := mustManager(t, testConfig())
	sender := newFakeSender()

	gap, err := message.NewFixedGap(40*time.Millisecond, 0)
	if err != nil {
		t.Fatalf("NewFixedGap() error = %v", err)
	}
	mustRegister(t, mgr, "qq", sender, gap)
	startManager(t, mgr)

	for _, text := range []string{"one", "two", "three"} {
		mustEnqueue(t, mgr, testMessage("qq", text))
	}
	for i := 0; i < 3; i++ {
		waitForSend(t, sender.sent)
	}

	times := sender.sentTimes()
	if len(times) != 3 {
		t.Fatalf("sent %d messages, want 3", len(times))
	}
	for i := 1; i < len(times); i++ {
		if got := times[i].Sub(times[i-1]); got < 30*time.Millisecond {
			t.Errorf("gap %d = %v, want at least 30ms", i, got)
		}
	}
}

func TestManagerUsesMessageLengthForGap(t *testing.T) {
	mgr := mustManager(t, testConfig())
	sender := newFakeSender()

	gap, err := message.NewLengthGap(0, 20*time.Millisecond, 0)
	if err != nil {
		t.Fatalf("NewLengthGap() error = %v", err)
	}
	mustRegister(t, mgr, "qq", sender, gap)
	startManager(t, mgr)

	mustEnqueue(t, mgr, testMessage("qq", "a"))
	mustEnqueue(t, mgr, testMessage("qq", "12345"))

	waitForSend(t, sender.sent)
	waitForSend(t, sender.sent)

	times := sender.sentTimes()
	if len(times) != 2 {
		t.Fatalf("sent %d messages, want 2", len(times))
	}
	if got := times[1].Sub(times[0]); got < 80*time.Millisecond {
		t.Errorf("gap = %v, want at least 80ms for a 5-rune message", got)
	}
}

func TestManagerWaitsRetryBackoff(t *testing.T) {
	cfg := testConfig()
	cfg.MaxAttempts = 2
	cfg.RetryBackoff = 40 * time.Millisecond

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
	mustEnqueue(t, mgr, testMessage("qq", "retry"))

	waitForSend(t, sender.sent)
	waitForSend(t, sender.sent)

	times := sender.sentTimes()
	if len(times) != 2 {
		t.Fatalf("sent %d attempts, want 2", len(times))
	}
	if got := times[1].Sub(times[0]); got < 30*time.Millisecond {
		t.Errorf("retry gap = %v, want at least 30ms", got)
	}
}
