package napcat

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// EventConfig sets per-account event resource limits and reconnect timing.
type EventConfig struct {
	ReconnectMin time.Duration
	ReconnectMax time.Duration
	BufferSize   int
	MaxBytes     int64
}

// Events receives a single account's upstream events and fans them out to live subscribers.
// No events are retained for disconnected subscribers.
type Events struct {
	address, token string
	client         *Client
	cfg            EventConfig
	mu             sync.Mutex
	ready          bool
	selfID         int64
	subscribers    map[chan []byte]struct{}
}

// NewEvents constructs an event receiver. An empty address disables event support.
func NewEvents(address, token string, client *Client, cfg EventConfig) (*Events, error) {
	if address == "" {
		return nil, nil
	}
	u, err := url.Parse(address)
	if err != nil || (u.Scheme != "ws" && u.Scheme != "wss") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("napcat: invalid event address")
	}
	if client == nil {
		return nil, errors.New("napcat: event client is required")
	}
	if cfg.ReconnectMin == 0 {
		cfg.ReconnectMin = time.Second
	}
	if cfg.ReconnectMax == 0 {
		cfg.ReconnectMax = 30 * time.Second
	}
	if cfg.BufferSize == 0 {
		cfg.BufferSize = 128
	}
	if cfg.MaxBytes == 0 {
		cfg.MaxBytes = 16 << 20
	}
	if cfg.ReconnectMin < 0 || cfg.ReconnectMax < cfg.ReconnectMin || cfg.BufferSize < 0 || cfg.MaxBytes < 0 {
		return nil, errors.New("napcat: invalid event limits")
	}
	return &Events{cfg: cfg, address: address, token: token, client: client, subscribers: make(map[chan []byte]struct{})}, nil
}

// Subscribe returns a live event stream, or false if the upstream is unavailable.
// Closing the stream signals an interruption or a subscriber that could not keep up.
func (e *Events) Subscribe() (<-chan []byte, func(), bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.ready {
		return nil, nil, false
	}
	ch := make(chan []byte, e.cfg.BufferSize)
	lifecycle, _ := json.Marshal(struct {
		Time          int64  `json:"time"`
		SelfID        int64  `json:"self_id"`
		PostType      string `json:"post_type"`
		MetaEventType string `json:"meta_event_type"`
		SubType       string `json:"sub_type"`
	}{time.Now().Unix(), e.selfID, "meta_event", "lifecycle", "connect"})
	ch <- lifecycle
	e.subscribers[ch] = struct{}{}
	var once sync.Once
	cancel := func() {
		once.Do(func() {
			e.mu.Lock()
			defer e.mu.Unlock()
			if _, ok := e.subscribers[ch]; ok {
				delete(e.subscribers, ch)
				drainAndClose(ch)
			}
		})
	}
	return ch, cancel, true
}

func (e *Events) publish(raw []byte) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for ch := range e.subscribers {
		select {
		case ch <- raw:
		default:
			delete(e.subscribers, ch)
			drainAndClose(ch)
		}
	}
}

func (e *Events) disconnect() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.ready = false
	for ch := range e.subscribers {
		delete(e.subscribers, ch)
		drainAndClose(ch)
	}
}

// drainAndClose prevents buffered stale events from delaying interruption detection.
func drainAndClose(ch chan []byte) {
	for {
		select {
		case <-ch:
		default:
			close(ch)
			return
		}
	}
}

// Run reconnects until cancellation, closing subscribers whenever the stream is lost.
func (e *Events) Run(ctx context.Context) {
	delay := e.cfg.ReconnectMin
	for ctx.Err() == nil {
		started := time.Now()
		err := e.receive(ctx)
		if time.Since(started) >= e.cfg.ReconnectMax {
			delay = e.cfg.ReconnectMin
		}
		e.disconnect()
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			log.Printf("napcat: event stream disconnected: %v", err)
		}
		jitter := time.Duration(rand.Int63n(int64(delay/2) + 1))
		wait := delay + jitter
		if wait > e.cfg.ReconnectMax {
			wait = e.cfg.ReconnectMax
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		delay *= 2
		if delay > e.cfg.ReconnectMax {
			delay = e.cfg.ReconnectMax
		}
	}
}

func (e *Events) receive(ctx context.Context) error {
	info, err := e.client.Check(ctx)
	if err != nil {
		return errors.New("identity lookup failed")
	}
	if info.UserID <= 0 {
		return errors.New("invalid upstream identity")
	}
	headers := http.Header{}
	if e.token != "" {
		headers.Set("Authorization", "Bearer "+e.token)
	}
	dialCtx, cancel := context.WithTimeout(ctx, e.client.cfg.Timeout)
	defer cancel()
	conn, _, err := websocket.Dial(dialCtx, e.address, &websocket.DialOptions{
		HTTPHeader: headers,
		HTTPClient: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	})
	if err != nil {
		return errors.New("event connection failed")
	}
	defer conn.CloseNow()
	conn.SetReadLimit(e.cfg.MaxBytes)
	pingCtx, stopPing := context.WithCancel(ctx)
	defer stopPing()
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-pingCtx.Done():
				return
			case <-ticker.C:
				checkCtx, cancel := context.WithTimeout(pingCtx, 10*time.Second)
				err := conn.Ping(checkCtx)
				cancel()
				if err != nil {
					conn.CloseNow()
					return
				}
			}
		}
	}()
	ready := false
	for {
		readCtx := ctx
		cancelRead := func() {}
		if !ready {
			readCtx, cancelRead = context.WithTimeout(ctx, e.client.cfg.Timeout)
		}
		kind, raw, err := conn.Read(readCtx)
		cancelRead()
		if err != nil {
			return err
		}
		if kind != websocket.MessageText {
			return errors.New("non-text event")
		}
		var event struct {
			SelfID        int64  `json:"self_id"`
			PostType      string `json:"post_type"`
			MetaEventType string `json:"meta_event_type"`
			SubType       string `json:"sub_type"`
		}
		if err := json.Unmarshal(raw, &event); err != nil || event.SelfID != info.UserID || event.PostType == "" {
			return errors.New("invalid event or upstream identity changed")
		}
		if !ready {
			e.mu.Lock()
			e.selfID, e.ready = info.UserID, true
			e.mu.Unlock()
			ready = true
		}
		if event.PostType == "meta_event" && event.MetaEventType == "lifecycle" && event.SubType == "connect" {
			continue
		}
		e.publish(raw)
	}
}
