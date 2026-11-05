package napcat

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"message-gateway/internal/message"
)

var _ message.Sender = (*Client)(nil)

type privateSendRequest struct {
	UserID  int64  `json:"user_id"`
	GroupID int64  `json:"group_id,omitempty"`
	Message string `json:"message"`
}

type groupSendRequest struct {
	GroupID int64  `json:"group_id"`
	Message string `json:"message"`
}

// Send delivers msg through NapCat.
func (c *Client) Send(ctx context.Context, msg message.Message) (message.Receipt, error) {
	if err := msg.Validate(); err != nil {
		return message.Receipt{}, err
	}
	if msg.Platform != message.PlatformQQ {
		return message.Receipt{}, fmt.Errorf(
			"%w: napcat does not support platform %q",
			message.ErrPermanent,
			msg.Platform,
		)
	}

	switch msg.Target.Kind {
	case message.TargetGroup:
		return c.sendGroup(ctx, msg)
	case message.TargetUser:
		return c.sendPrivate(ctx, msg)
	default:
		return message.Receipt{}, fmt.Errorf(
			"%w: unsupported target kind %q",
			message.ErrPermanent,
			msg.Target.Kind,
		)
	}
}

func (c *Client) sendGroup(ctx context.Context, msg message.Message) (message.Receipt, error) {
	groupID, err := parseTargetID(msg.Target.ID)
	if err != nil {
		return message.Receipt{}, err
	}

	var data struct {
		MessageID flexID `json:"message_id"`
	}
	params := groupSendRequest{GroupID: groupID, Message: msg.Text}
	if err := c.call(ctx, "send_group_msg", params, &data); err != nil {
		return message.Receipt{}, err
	}
	return message.Receipt{MessageID: string(data.MessageID), SentAt: time.Now()}, nil
}

func (c *Client) sendPrivate(ctx context.Context, msg message.Message) (message.Receipt, error) {
	userID, err := parseTargetID(msg.Target.ID)
	if err != nil {
		return message.Receipt{}, err
	}

	if c.directory.Loaded() && c.directory.IsFriend(userID) {
		return c.sendPrivateMsg(ctx, userID, 0, msg.Text)
	}

	var groupErrs []error
	if c.cfg.GroupTempSession {
		for _, groupID := range c.directory.Groups(userID) {
			if err := ctx.Err(); err != nil {
				return message.Receipt{}, err
			}
			receipt, err := c.sendPrivateMsg(ctx, userID, groupID, msg.Text)
			if err == nil {
				return receipt, nil
			}
			groupErrs = append(groupErrs, err)
		}
	}

	receipt, fallbackErr := c.sendPrivateMsg(ctx, userID, 0, msg.Text)
	if fallbackErr == nil {
		return receipt, nil
	}

	if !c.directory.Loaded() {
		// The directory may finish loading later, so retry this message then.
		return message.Receipt{}, transientError(fallbackErr)
	}
	for _, groupErr := range groupErrs {
		if errors.Is(groupErr, message.ErrTransient) {
			return message.Receipt{}, transientError(fallbackErr)
		}
	}
	if errors.Is(fallbackErr, message.ErrPermanent) {
		return message.Receipt{}, permanentError(fallbackErr)
	}
	return message.Receipt{}, transientError(fallbackErr)
}

func (c *Client) sendPrivateMsg(
	ctx context.Context,
	userID int64,
	groupID int64,
	text string,
) (message.Receipt, error) {
	var data struct {
		MessageID flexID `json:"message_id"`
	}
	params := privateSendRequest{
		UserID:  userID,
		GroupID: groupID,
		Message: text,
	}
	if err := c.call(ctx, "send_private_msg", params, &data); err != nil {
		return message.Receipt{}, err
	}
	return message.Receipt{MessageID: string(data.MessageID), SentAt: time.Now()}, nil
}

func parseTargetID(raw string) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("%w: invalid target id %q", message.ErrPermanent, raw)
	}
	return id, nil
}

func transientError(err error) error {
	return fmt.Errorf("%w: %v", message.ErrTransient, err)
}

func permanentError(err error) error {
	return fmt.Errorf("%w: %v", message.ErrPermanent, err)
}
