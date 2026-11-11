package httpapi

import (
	"sync"
	"time"

	"message-gateway/internal/message"
)

// Delivery states reported by the status query API. They match the WebSocket
// frame types, plus "queued" for accepted messages that have no update yet.
const (
	statusQueued   = "queued"
	statusRetrying = "retrying"
	statusSent     = "sent"
	statusFailed   = "failed"
)

const defaultStatusCapacity = 4096

// statusEntry is the latest known delivery state for one enqueued message.
type statusEntry struct {
	account  string
	state    string
	attempts int
	err      string
	retryIn  time.Duration
	receipt  message.Receipt
	reason   string // "permanent" or "exhausted"; set when state is statusFailed
}

// statusRegistry tracks the delivery status of enqueued messages so clients
// can query the outcome of an asynchronous send. It is bounded: once the
// registry holds more entries than its capacity, the oldest entries are
// evicted. Entries live in memory only and are lost when the process exits.
type statusRegistry struct {
	cap     int
	mu      sync.Mutex
	order   []message.DeliveryID
	entries map[message.DeliveryID]*statusEntry
}

func newStatusRegistry(capacity int) *statusRegistry {
	if capacity < 1 {
		capacity = defaultStatusCapacity
	}
	return &statusRegistry{
		cap:     capacity,
		entries: make(map[message.DeliveryID]*statusEntry, capacity),
	}
}

// register records a newly accepted delivery as queued. If the id is already
// tracked, register does nothing: a fast worker may report an update before
// the enqueueing handler gets to run, and that update must not be downgraded.
func (r *statusRegistry) register(id message.DeliveryID, account string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if entry, ok := r.entries[id]; ok {
		if entry.account == "" {
			entry.account = account
		}
		return
	}
	r.track(id, &statusEntry{account: account, state: statusQueued})
}

// update records a delivery lifecycle notification. It is called from the
// sender's worker goroutine and only performs map work under a short lock, as
// the DeliveryObserver contract requires. An update for an unknown or already
// evicted id re-tracks it; it is then evicted again like any other entry.
func (r *statusRegistry) update(u message.DeliveryUpdate) {
	entry := &statusEntry{account: u.Account, attempts: u.Attempts}
	switch u.State {
	case message.DeliveryRetrying:
		entry.state = statusRetrying
		entry.err = errText(u.Err)
		entry.retryIn = u.RetryIn
	case message.DeliverySent:
		entry.state = statusSent
		entry.receipt = u.Receipt
	case message.DeliveryFailed:
		entry.state = statusFailed
		entry.err = errText(u.Err)
		entry.reason = failureReason(u.Err)
	default:
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	existing, ok := r.entries[u.ID]
	if !ok {
		r.track(u.ID, entry)
		return
	}
	if entry.account == "" {
		entry.account = existing.account
	}
	r.entries[u.ID] = entry
}

// lookup returns a copy of the status for id, if it is still tracked.
func (r *statusRegistry) lookup(id message.DeliveryID) (statusEntry, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.entries[id]
	if !ok {
		return statusEntry{}, false
	}
	return *entry, true
}

// track inserts a new entry and evicts oldest entries while over capacity.
// Callers must hold r.mu and know the id is not yet present.
func (r *statusRegistry) track(id message.DeliveryID, entry *statusEntry) {
	r.entries[id] = entry
	r.order = append(r.order, id)
	for len(r.order) > r.cap {
		delete(r.entries, r.order[0])
		r.order = r.order[1:]
	}
}
