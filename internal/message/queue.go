package message

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"
)

type delivery struct {
	id       DeliveryID
	msg      Message
	attempts int
}

// senderQueue is a bounded FIFO queue plus pacing state for one sender.
type senderQueue struct {
	key    string
	sender Sender
	policy GapPolicy
	size   int

	mu       sync.Mutex
	items    []*delivery
	lastSent time.Time
	retryAt  time.Time
	closed   bool
	notify   chan struct{}
}

func newSenderQueue(key string, sender Sender, policy GapPolicy, size int) *senderQueue {
	return &senderQueue{
		key:    key,
		sender: sender,
		policy: policy,
		size:   size,
		notify: make(chan struct{}, 1),
	}
}

func (q *senderQueue) push(ctx context.Context, item *delivery) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return ErrClosed
	}
	if len(q.items) >= q.size {
		q.mu.Unlock()
		return ErrQueueFull
	}
	q.items = append(q.items, item)
	q.mu.Unlock()

	q.signal()
	return nil
}

func (q *senderQueue) head(ctx context.Context) (*delivery, bool) {
	for {
		q.mu.Lock()
		if q.closed {
			q.mu.Unlock()
			return nil, false
		}
		if len(q.items) > 0 {
			item := q.items[0]
			q.mu.Unlock()
			return item, true
		}
		q.mu.Unlock()

		select {
		case <-ctx.Done():
			return nil, false
		case <-q.notify:
		}
	}
}

// waitTime reports how long the worker should wait before sending item.
func (q *senderQueue) waitTime(now time.Time, item *delivery, minGap time.Duration) time.Duration {
	q.mu.Lock()
	defer q.mu.Unlock()

	gap := q.policy.Gap(item.msg)
	if gap < 0 {
		gap = 0
	}
	if gap < minGap {
		gap = minGap
	}

	due := q.retryAt
	if !q.lastSent.IsZero() {
		if next := q.lastSent.Add(gap); next.After(due) {
			due = next
		}
	}
	if due.IsZero() {
		return 0
	}
	if wait := due.Sub(now); wait > 0 {
		return wait
	}
	return 0
}

func (q *senderQueue) complete(now time.Time) {
	q.mu.Lock()
	defer q.mu.Unlock()

	q.popHead()
	q.lastSent = now
	q.retryAt = time.Time{}
}

// fail records a failed attempt and reports whether the head was dropped.
func (q *senderQueue) fail(now time.Time, err error, maxAttempts int, backoff time.Duration) bool {
	q.mu.Lock()
	defer q.mu.Unlock()

	if len(q.items) == 0 {
		return true
	}

	item := q.items[0]
	item.attempts++

	if errors.Is(err, ErrPermanent) || item.attempts >= maxAttempts {
		q.popHead()
		q.retryAt = time.Time{}
		return true
	}

	q.retryAt = now.Add(backoff)
	return false
}

func (q *senderQueue) popHead() {
	q.items[0] = nil
	q.items = q.items[1:]
	if len(q.items) == 0 {
		q.items = nil
	}
}

func (q *senderQueue) signal() {
	select {
	case q.notify <- struct{}{}:
	default:
	}
}

func (q *senderQueue) close() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	q.signal()
}

func (q *senderQueue) run(ctx context.Context, manager *Manager) {
	for {
		item, ok := q.head(ctx)
		if !ok {
			return
		}

		delay := q.waitTime(time.Now(), item, manager.cfg.MinGap)
		if !waitTimer(ctx, delay) {
			return
		}

		sendCtx, cancel := context.WithTimeout(ctx, manager.cfg.SendTimeout)
		_, err := q.sender.Send(sendCtx, item.msg)
		cancel()

		if ctx.Err() != nil {
			return
		}

		if err != nil {
			dropped := q.fail(time.Now(), err, manager.cfg.MaxAttempts, manager.cfg.RetryBackoff)
			if dropped {
				log.Printf(
					"message: dropped %s sender=%s target=%s attempts=%d: %v",
					item.id,
					q.key,
					item.msg.Target.Kind,
					item.attempts,
					err,
				)
			}
			continue
		}

		q.complete(time.Now())
	}
}
