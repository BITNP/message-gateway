package message_test

import (
	"errors"
	"testing"

	"message-gateway/internal/message"
)

func TestMessageValidate(t *testing.T) {
	valid := message.Message{
		Platform: message.PlatformQQ,
		Sender:   "qq",
		Target: message.Target{
			Kind: message.TargetUser,
			ID:   "12345",
		},
		Text: "hello",
	}

	tests := []struct {
		name    string
		mutate  func(*message.Message)
		wantErr bool
	}{
		{
			name:   "valid user target",
			mutate: func(*message.Message) {},
		},
		{
			name: "valid group target",
			mutate: func(m *message.Message) {
				m.Target.Kind = message.TargetGroup
			},
		},
		{
			name: "missing platform",
			mutate: func(m *message.Message) {
				m.Platform = ""
			},
			wantErr: true,
		},
		{
			name: "unsupported platform",
			mutate: func(m *message.Message) {
				m.Platform = "wechat"
			},
			wantErr: true,
		},
		{
			name: "missing sender",
			mutate: func(m *message.Message) {
				m.Sender = " "
			},
			wantErr: true,
		},
		{
			name: "unsupported target kind",
			mutate: func(m *message.Message) {
				m.Target.Kind = "channel"
			},
			wantErr: true,
		},
		{
			name: "missing target id",
			mutate: func(m *message.Message) {
				m.Target.ID = " "
			},
			wantErr: true,
		},
		{
			name: "missing text",
			mutate: func(m *message.Message) {
				m.Text = " "
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := valid
			tt.mutate(&msg)

			err := msg.Validate()
			if tt.wantErr {
				if !errors.Is(err, message.ErrInvalidMessage) {
					t.Fatalf("Validate() error = %v, want ErrInvalidMessage", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Validate() error = %v, want nil", err)
			}
		})
	}
}

func TestPlatformValid(t *testing.T) {
	tests := []struct {
		platform message.Platform
		want     bool
	}{
		{message.PlatformQQ, true},
		{"", false},
		{"wechat", false},
	}

	for _, tt := range tests {
		if got := tt.platform.Valid(); got != tt.want {
			t.Errorf("Platform(%q).Valid() = %v, want %v", tt.platform, got, tt.want)
		}
	}
}

func TestTargetKindValid(t *testing.T) {
	tests := []struct {
		kind message.TargetKind
		want bool
	}{
		{message.TargetUser, true},
		{message.TargetGroup, true},
		{"", false},
		{"channel", false},
	}

	for _, tt := range tests {
		if got := tt.kind.Valid(); got != tt.want {
			t.Errorf("TargetKind(%q).Valid() = %v, want %v", tt.kind, got, tt.want)
		}
	}
}
