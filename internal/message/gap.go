package message

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"time"
	"unicode/utf8"
)

// GapMode selects how the manager calculates the delay between sends.
type GapMode string

const (
	// GapModeFixed uses a configured base delay plus jitter.
	GapModeFixed GapMode = "fixed"
	// GapModeLength scales the delay with the number of runes in the message.
	GapModeLength GapMode = "length"
)

// GapPolicy returns the minimum delay before sending msg.
type GapPolicy interface {
	Gap(msg Message) time.Duration
}

// FixedGap returns a constant base delay plus random jitter.
type FixedGap struct {
	Base   time.Duration
	Jitter time.Duration
}

// NewFixedGap validates and returns a fixed gap policy.
func NewFixedGap(base, jitter time.Duration) (FixedGap, error) {
	if base < 0 {
		return FixedGap{}, errors.New("message: fixed gap base must not be negative")
	}
	if jitter < 0 {
		return FixedGap{}, errors.New("message: fixed gap jitter must not be negative")
	}
	return FixedGap{Base: base, Jitter: jitter}, nil
}

// Gap implements GapPolicy.
func (g FixedGap) Gap(Message) time.Duration {
	return g.Base + jitter(g.Jitter)
}

// LengthGap returns a base delay plus a per-rune delay and random jitter.
type LengthGap struct {
	Base    time.Duration
	PerRune time.Duration
	Jitter  time.Duration
}

// NewLengthGap validates and returns a length-based gap policy.
func NewLengthGap(base, perRune, jitter time.Duration) (LengthGap, error) {
	if base < 0 {
		return LengthGap{}, errors.New("message: length gap base must not be negative")
	}
	if perRune < 0 {
		return LengthGap{}, errors.New("message: length gap per-rune delay must not be negative")
	}
	if jitter < 0 {
		return LengthGap{}, errors.New("message: length gap jitter must not be negative")
	}
	return LengthGap{Base: base, PerRune: perRune, Jitter: jitter}, nil
}

// Gap implements GapPolicy.
func (g LengthGap) Gap(msg Message) time.Duration {
	runes := utf8.RuneCountInString(msg.Text)
	return g.Base + time.Duration(runes)*g.PerRune + jitter(g.Jitter)
}

// NewGapPolicy builds the configured gap policy.
func NewGapPolicy(mode GapMode, base, perRune, jitter time.Duration) (GapPolicy, error) {
	switch mode {
	case GapModeFixed:
		gap, err := NewFixedGap(base, jitter)
		if err != nil {
			return nil, err
		}
		return gap, nil
	case GapModeLength:
		gap, err := NewLengthGap(base, perRune, jitter)
		if err != nil {
			return nil, err
		}
		return gap, nil
	default:
		return nil, fmt.Errorf("message: unsupported gap mode %q", mode)
	}
}

// jitter returns a random duration in [0, max). Non-positive values disable
// jitter, which keeps zero-value policies deterministic.
func jitter(max time.Duration) time.Duration {
	if max <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(max)))
}
