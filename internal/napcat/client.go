// Package napcat implements the OneBot 11 HTTP transport used to send QQ
// messages through NapCat.
package napcat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"message-gateway/internal/message"
)

const maxResponseBytes = 16 << 20

// permanentRetcodes are OneBot error codes that cannot succeed by retrying.
var permanentRetcodes = map[int]struct{}{
	100:  {},
	102:  {},
	103:  {},
	104:  {},
	1400: {},
	1401: {},
	1403: {},
	1404: {},
}

// Config controls the NapCat HTTP client.
type Config struct {
	BaseURL     string
	AccessToken string
	Timeout     time.Duration

	DirectoryRefresh time.Duration
	DirectoryRetry   time.Duration
	DirectoryTimeout time.Duration

	GroupTempSession bool
}

// LoginInfo describes the QQ account currently logged into NapCat.
type LoginInfo struct {
	UserID   int64
	Nickname string
}

// Client sends messages through NapCat's OneBot 11 HTTP API.
type Client struct {
	baseURL     string
	accessToken string
	httpClient  *http.Client
	directory   *memberDirectory
	cfg         Config
}

// NewClient validates cfg and creates a NapCat client.
func NewClient(cfg Config) (*Client, error) {
	baseURL := strings.TrimSpace(cfg.BaseURL)
	if baseURL == "" {
		return nil, errors.New("napcat: base URL is required")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("napcat: parse base URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("napcat: base URL scheme must be http or https")
	}
	if parsed.Host == "" {
		return nil, errors.New("napcat: base URL host is required")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("napcat: base URL must not contain a query or fragment")
	}

	if cfg.Timeout < 0 {
		return nil, errors.New("napcat: timeout must not be negative")
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 10 * time.Second
	}
	if cfg.DirectoryRefresh < 0 {
		return nil, errors.New("napcat: directory refresh must not be negative")
	}
	if cfg.DirectoryRefresh == 0 {
		cfg.DirectoryRefresh = 30 * time.Minute
	}
	if cfg.DirectoryRetry < 0 {
		return nil, errors.New("napcat: directory retry must not be negative")
	}
	if cfg.DirectoryRetry == 0 {
		cfg.DirectoryRetry = 5 * time.Second
	}
	if cfg.DirectoryTimeout < 0 {
		return nil, errors.New("napcat: directory timeout must not be negative")
	}
	if cfg.DirectoryTimeout == 0 {
		cfg.DirectoryTimeout = 60 * time.Second
	}

	return &Client{
		baseURL:     strings.TrimRight(baseURL, "/"),
		accessToken: strings.TrimSpace(cfg.AccessToken),
		httpClient:  &http.Client{Timeout: cfg.Timeout},
		directory:   newMemberDirectory(),
		cfg:         cfg,
	}, nil
}

// Check returns the account NapCat is currently logged into.
func (c *Client) Check(ctx context.Context) (LoginInfo, error) {
	var data struct {
		UserID   int64  `json:"user_id"`
		Nickname string `json:"nickname"`
	}
	if err := c.call(ctx, "get_login_info", nil, &data); err != nil {
		return LoginInfo{}, err
	}
	return LoginInfo{UserID: data.UserID, Nickname: data.Nickname}, nil
}

type apiResponse struct {
	Status  string          `json:"status"`
	Retcode int             `json:"retcode"`
	Data    json.RawMessage `json:"data"`
	Message string          `json:"message"`
	Wording string          `json:"wording"`
}

func (r apiResponse) err(action string) error {
	kind := message.ErrTransient
	if _, ok := permanentRetcodes[r.Retcode]; ok {
		kind = message.ErrPermanent
	}

	detail := r.Message
	if r.Wording != "" {
		if detail != "" {
			detail += ": "
		}
		detail += r.Wording
	}
	if detail == "" {
		return fmt.Errorf("%w: %s failed with retcode %d", kind, action, r.Retcode)
	}
	return fmt.Errorf("%w: %s failed with retcode %d: %s", kind, action, r.Retcode, detail)
}

func (c *Client) call(ctx context.Context, action string, params any, out any) error {
	var body []byte
	if params == nil {
		body = []byte("{}")
	} else {
		encoded, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("%w: marshal %s params: %v", message.ErrPermanent, action, err)
		}
		body = encoded
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/"+action, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("%w: build %s request: %v", message.ErrPermanent, action, err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.accessToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.accessToken)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("%w: %s request: %v", message.ErrTransient, action, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= http.StatusInternalServerError {
			return fmt.Errorf("%w: %s returned HTTP %d", message.ErrTransient, action, resp.StatusCode)
		}
		return fmt.Errorf("%w: %s returned HTTP %d", message.ErrPermanent, action, resp.StatusCode)
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("%w: read %s response: %v", message.ErrTransient, action, err)
	}

	var api apiResponse
	if err := json.Unmarshal(raw, &api); err != nil {
		return fmt.Errorf("%w: decode %s response: %v", message.ErrTransient, action, err)
	}
	if api.Status != "ok" || api.Retcode != 0 {
		return api.err(action)
	}

	if out == nil || len(api.Data) == 0 || string(api.Data) == "null" {
		return nil
	}
	if err := json.Unmarshal(api.Data, out); err != nil {
		return fmt.Errorf("%w: decode %s data: %v", message.ErrTransient, action, err)
	}
	return nil
}

// flexID accepts either a JSON number or a JSON string.
type flexID string

func (f *flexID) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		*f = ""
		return nil
	}

	var value string
	if err := json.Unmarshal(data, &value); err == nil {
		*f = flexID(value)
		return nil
	}

	var number json.Number
	if err := json.Unmarshal(data, &number); err == nil {
		*f = flexID(number.String())
		return nil
	}

	return fmt.Errorf("invalid message id %s", data)
}
