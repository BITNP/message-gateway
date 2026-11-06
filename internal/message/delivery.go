package message

// DeliveryState reports how far an observed delivery has progressed.
type DeliveryState string

const (
	// DeliveryRetrying means one send attempt failed and another is scheduled.
	DeliveryRetrying DeliveryState = "retrying"
	// DeliverySent means the platform accepted the message.
	DeliverySent DeliveryState = "sent"
	// DeliveryFailed means the message will not be attempted again.
	DeliveryFailed DeliveryState = "failed"
)

// DeliveryUpdate is one lifecycle notification for an observed delivery.
//
// Observers receive zero or more DeliveryRetrying updates followed by exactly
// one terminal update, DeliverySent or DeliveryFailed. They are never called
// for messages rejected at enqueue time, and messages still queued when the
// manager shuts down are dropped without an update.
type DeliveryUpdate struct {
	// ID is the delivery id returned by EnqueueObserved.
	ID DeliveryID
	// State is DeliveryRetrying, DeliverySent, or DeliveryFailed.
	State DeliveryState
	// Attempts is the number of send attempts that completed for this update.
	Attempts int
	// Receipt is set when State is DeliverySent.
	Receipt Receipt
	// Err is the send error when State is DeliveryRetrying or DeliveryFailed.
	Err error
}

// DeliveryObserver receives delivery updates for one enqueued message.
//
// It is called from the sender's worker goroutine, so it must return quickly:
// blocking delays every later message for that sender. It must not call back
// into the manager and must not panic, since a panic stops the sender's worker.
// Updates for one delivery arrive in order; updates for different senders may
// run concurrently.
type DeliveryObserver func(DeliveryUpdate)

func notifyDelivery(item *delivery, update DeliveryUpdate) {
	if item.observe == nil {
		return
	}
	update.ID = item.id
	item.observe(update)
}
