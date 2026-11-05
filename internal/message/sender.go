package message

import "context"

// Sender delivers one message to a platform. Implementations should wrap
// transport failures with ErrTransient or ErrPermanent so the manager can
// decide whether to retry.
type Sender interface {
	Send(ctx context.Context, msg Message) (Receipt, error)
}
