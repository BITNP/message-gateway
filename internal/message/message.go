// Package message defines the platform-neutral message types and sender
// contracts used by the outbound message manager.
package message

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Platform identifies a messaging platform.
type Platform string

const (
	// PlatformQQ is the QQ messaging platform.
	PlatformQQ Platform = "qq"
)

// Valid reports whether the platform is supported.
func (p Platform) Valid() bool {
	return p == PlatformQQ
}

// TargetKind identifies the kind of chat a message is addressed to.
type TargetKind string

const (
	// TargetUser addresses a one-to-one chat.
	TargetUser TargetKind = "user"
	// TargetGroup addresses a group chat.
	TargetGroup TargetKind = "group"
)

// Valid reports whether the target kind is supported.
func (k TargetKind) Valid() bool {
	switch k {
	case TargetUser, TargetGroup:
		return true
	default:
		return false
	}
}

// Target identifies a chat on a platform. ID is a string so platform-specific
// identifiers do not lose precision when they pass through JSON.
type Target struct {
	Kind TargetKind
	ID   string
}

// Message is an outbound message.
type Message struct {
	Platform Platform
	Sender   string
	Target   Target
	Text     string
}

// Receipt describes a successful send.
type Receipt struct {
	MessageID string
	SentAt    time.Time
}

// Message-related errors. Sender implementations wrap transport failures with
// ErrTransient or ErrPermanent so the manager can decide whether to retry.
var (
	ErrInvalidMessage = errors.New("message: invalid message")
	ErrQueueFull      = errors.New("message: queue full")
	ErrClosed         = errors.New("message: manager closed")
	ErrUnknownSender  = errors.New("message: unknown sender")
	ErrTransient      = errors.New("message: transient send failure")
	ErrPermanent      = errors.New("message: permanent send failure")
)

// Validate checks that the message has the fields required by every sender.
// Platform-specific validation, such as numeric QQ IDs, belongs to the sender.
func (m Message) Validate() error {
	if !m.Platform.Valid() {
		return fmt.Errorf("%w: unsupported platform %q", ErrInvalidMessage, m.Platform)
	}
	if strings.TrimSpace(m.Sender) == "" {
		return fmt.Errorf("%w: sender is required", ErrInvalidMessage)
	}
	if !m.Target.Kind.Valid() {
		return fmt.Errorf("%w: unsupported target kind %q", ErrInvalidMessage, m.Target.Kind)
	}
	if strings.TrimSpace(m.Target.ID) == "" {
		return fmt.Errorf("%w: target id is required", ErrInvalidMessage)
	}
	if strings.TrimSpace(m.Text) == "" {
		return fmt.Errorf("%w: text is required", ErrInvalidMessage)
	}
	return nil
}
