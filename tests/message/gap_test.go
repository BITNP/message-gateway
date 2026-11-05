package message_test

import (
	"testing"
	"time"

	"message-gateway/internal/message"
)

func TestFixedGap(t *testing.T) {
	gap, err := message.NewFixedGap(3*time.Second, 0)
	if err != nil {
		t.Fatalf("NewFixedGap() error = %v", err)
	}

	if got := gap.Gap(message.Message{Text: "hello"}); got != 3*time.Second {
		t.Errorf("Gap() = %v, want %v", got, 3*time.Second)
	}
}

func TestFixedGapJitterRange(t *testing.T) {
	const (
		base   = 2 * time.Second
		jitter = time.Second
	)

	gap, err := message.NewFixedGap(base, jitter)
	if err != nil {
		t.Fatalf("NewFixedGap() error = %v", err)
	}

	for i := 0; i < 200; i++ {
		got := gap.Gap(message.Message{Text: "hello"})
		if got < base || got >= base+jitter {
			t.Fatalf("Gap() = %v, want in [%v, %v)", got, base, base+jitter)
		}
	}
}

func TestLengthGapCountsRunes(t *testing.T) {
	gap, err := message.NewLengthGap(time.Second, 100*time.Millisecond, 0)
	if err != nil {
		t.Fatalf("NewLengthGap() error = %v", err)
	}

	// "你好" is two runes even though it is six bytes.
	got := gap.Gap(message.Message{Text: "你好"})
	want := time.Second + 2*100*time.Millisecond
	if got != want {
		t.Errorf("Gap() = %v, want %v", got, want)
	}
}

func TestLengthGapEmptyText(t *testing.T) {
	gap, err := message.NewLengthGap(time.Second, 100*time.Millisecond, 0)
	if err != nil {
		t.Fatalf("NewLengthGap() error = %v", err)
	}

	if got := gap.Gap(message.Message{}); got != time.Second {
		t.Errorf("Gap() = %v, want %v", got, time.Second)
	}
}

func TestNewGapPolicy(t *testing.T) {
	tests := []struct {
		name    string
		mode    message.GapMode
		base    time.Duration
		perRune time.Duration
		jitter  time.Duration
		text    string
		want    time.Duration
		wantErr bool
	}{
		{
			name: "fixed",
			mode: message.GapModeFixed,
			base: 3 * time.Second,
			text: "hello",
			want: 3 * time.Second,
		},
		{
			name:    "length",
			mode:    message.GapModeLength,
			base:    time.Second,
			perRune: 50 * time.Millisecond,
			text:    "hello",
			want:    time.Second + 5*50*time.Millisecond,
		},
		{
			name:    "unknown mode",
			mode:    "random",
			wantErr: true,
		},
		{
			name:    "negative fixed base",
			mode:    message.GapModeFixed,
			base:    -time.Second,
			wantErr: true,
		},
		{
			name:    "negative length per-rune",
			mode:    message.GapModeLength,
			perRune: -time.Millisecond,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gap, err := message.NewGapPolicy(tt.mode, tt.base, tt.perRune, tt.jitter)
			if tt.wantErr {
				if err == nil {
					t.Fatal("NewGapPolicy() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("NewGapPolicy() error = %v", err)
			}

			if got := gap.Gap(message.Message{Text: tt.text}); got != tt.want {
				t.Errorf("Gap() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGapConstructorsRejectNegativeDurations(t *testing.T) {
	tests := []struct {
		name  string
		build func() error
	}{
		{
			name: "fixed base",
			build: func() error {
				_, err := message.NewFixedGap(-time.Second, 0)
				return err
			},
		},
		{
			name: "fixed jitter",
			build: func() error {
				_, err := message.NewFixedGap(0, -time.Second)
				return err
			},
		},
		{
			name: "length base",
			build: func() error {
				_, err := message.NewLengthGap(-time.Second, 0, 0)
				return err
			},
		},
		{
			name: "length per-rune",
			build: func() error {
				_, err := message.NewLengthGap(0, -time.Second, 0)
				return err
			},
		},
		{
			name: "length jitter",
			build: func() error {
				_, err := message.NewLengthGap(0, 0, -time.Second)
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.build(); err == nil {
				t.Fatal("expected an error for a negative duration")
			}
		})
	}
}
