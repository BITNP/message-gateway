package message

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// DeliveryID identifies one enqueued message for logging and diagnostics.
// IDs are unique per process and are not persisted across restarts.
type DeliveryID string

// Config controls the manager's queues and delivery behavior.
type Config struct {
	QueueSize    int
	SendTimeout  time.Duration
	MaxAttempts  int
	RetryBackoff time.Duration
	MinGap       time.Duration
}

func (c Config) validate() error {
	if c.QueueSize <= 0 {
		return errors.New("message: queue size must be positive")
	}
	if c.SendTimeout <= 0 {
		return errors.New("message: send timeout must be positive")
	}
	if c.MaxAttempts < 1 {
		return errors.New("message: max attempts must be at least 1")
	}
	if c.RetryBackoff < 0 {
		return errors.New("message: retry backoff must not be negative")
	}
	if c.MinGap < 0 {
		return errors.New("message: minimum gap must not be negative")
	}
	return nil
}

// Manager owns one FIFO queue and one worker per registered sender.
type Manager struct {
	cfg Config

	mu      sync.RWMutex
	queues  map[string]*senderQueue
	started bool
	closed  bool
	seq     atomic.Uint64
}

// NewManager validates cfg and creates an empty manager.
func NewManager(cfg Config) (*Manager, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &Manager{
		cfg:    cfg,
		queues: make(map[string]*senderQueue),
	}, nil
}

// Register adds a sender and its gap policy. It must be called before Run.
func (m *Manager) Register(senderKey string, sender Sender, policy GapPolicy) error {
	if strings.TrimSpace(senderKey) == "" {
		return errors.New("message: sender key is required")
	}
	if sender == nil {
		return errors.New("message: sender is required")
	}
	if policy == nil {
		return errors.New("message: gap policy is required")
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.started {
		return errors.New("message: cannot register a sender after Run")
	}
	if _, exists := m.queues[senderKey]; exists {
		return fmt.Errorf("message: sender %q already registered", senderKey)
	}
	m.queues[senderKey] = newSenderQueue(senderKey, sender, policy, m.cfg.QueueSize)
	return nil
}

// Enqueue adds msg to the queue for its sender. It never blocks: a full queue
// returns ErrQueueFull and the caller decides whether to retry or drop.
func (m *Manager) Enqueue(ctx context.Context, msg Message) (DeliveryID, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := msg.Validate(); err != nil {
		return "", err
	}

	m.mu.RLock()
	queue := m.queues[msg.Sender]
	closed := m.closed
	m.mu.RUnlock()

	if closed {
		return "", ErrClosed
	}
	if queue == nil {
		return "", fmt.Errorf("%w: %q", ErrUnknownSender, msg.Sender)
	}

	id := DeliveryID(fmt.Sprintf("%s-%d", msg.Sender, m.seq.Add(1)))
	delivery := &delivery{id: id, msg: msg}
	if err := queue.push(ctx, delivery); err != nil {
		return "", err
	}
	return id, nil
}

// Run starts one worker per registered sender and blocks until ctx is
// canceled. Run should be called once. Pending messages are dropped on
// shutdown; in-flight sends are canceled through ctx.
func (m *Manager) Run(ctx context.Context) {
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return
	}
	m.started = true
	queues := make([]*senderQueue, 0, len(m.queues))
	for _, queue := range m.queues {
		queues = append(queues, queue)
	}
	m.mu.Unlock()

	var wg sync.WaitGroup
	for _, queue := range queues {
		wg.Add(1)
		go func(queue *senderQueue) {
			defer wg.Done()
			queue.run(ctx, m)
		}(queue)
	}

	<-ctx.Done()

	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()

	for _, queue := range queues {
		queue.close()
	}
	wg.Wait()
}

func waitTimer(ctx context.Context, delay time.Duration) bool {
	if delay <= 0 {
		return ctx.Err() == nil
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
