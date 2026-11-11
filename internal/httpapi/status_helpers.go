package httpapi

import (
	"errors"
	"message-gateway/internal/message"
	"time"
)

func failureReason(err error) string {
	if errors.Is(err, message.ErrClosed) {
		return "shutdown"
	}
	if errors.Is(err, message.ErrPermanent) {
		return "permanent"
	}
	return "exhausted"
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}
