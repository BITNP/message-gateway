package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
)

const (
	// wsProtocolVersion is the version reported in the ready frame.
	wsProtocolVersion = 1
	// wsReadLimit matches the HTTP body limit so both transports reject the
	// same payloads.
	wsReadLimit = maxRequestBodyBytes
	// wsWriteTimeout bounds one frame write. The library does not apply the
	// context deadline to the socket, so a stalled write is cut off by
	// closing the connection.
	wsWriteTimeout = 10 * time.Second
	// wsCloseGrace bounds the graceful close handshake. If the peer does not
	// answer, the socket is closed so the connection goroutine can exit.
	wsCloseGrace = 2 * time.Second
	// wsPongTimeout bounds the wait for a ping reply.
	wsPongTimeout = 10 * time.Second
	// wsInboundBuffer bounds frames waiting for the connection goroutine. A
	// full buffer blocks the reader, which pushes back through TCP.
	wsInboundBuffer = 32

	statusGoingAway = websocket.StatusGoingAway
)

type wsReadyFrame struct {
	Type    string `json:"type"`
	Version int    `json:"version"`
	Sender  string `json:"sender"`
}

type wsErrorFrame struct {
	Type         string `json:"type"`
	RequestID    string `json:"request_id,omitempty"`
	Code         string `json:"code"`
	Message      string `json:"message"`
	RetryAfterMS int64  `json:"retry_after_ms,omitempty"`
}

type wsRequest struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
}

// wsConn is one WebSocket connection. Exactly one goroutine (writeLoop) writes
// to the socket; readLoop only feeds frames into inbound.
type wsConn struct {
	id      uint64
	ws      *websocket.Conn
	server  *Server
	inbound chan []byte

	ctx    context.Context
	cancel context.CancelFunc

	closeOnce   sync.Once
	closeCode   websocket.StatusCode
	closeReason string
}

func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	if !s.hub.reserve() {
		writeError(w, http.StatusServiceUnavailable, "too many websocket connections")
		return
	}

	// The server-wide read and write timeouts apply to the hijacked
	// connection too. Clear them before the upgrade and manage deadlines per
	// operation instead.
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Time{})
	_ = controller.SetWriteDeadline(time.Time{})

	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		s.hub.release()
		log.Printf("httpapi: websocket upgrade from %s: %v", r.RemoteAddr, err)
		return
	}
	conn.SetReadLimit(wsReadLimit)

	ctx, cancel := context.WithCancel(context.Background())
	c := &wsConn{
		id:      s.connSeq.Add(1),
		ws:      conn,
		server:  s,
		inbound: make(chan []byte, wsInboundBuffer),
		ctx:     ctx,
		cancel:  cancel,
	}

	if !s.hub.add(c) {
		_ = conn.Close(statusGoingAway, "gateway shutting down")
		cancel()
		return
	}
	defer s.hub.remove(c)
	defer cancel()
	// The reader is blocked in a socket read that no context can interrupt;
	// closing the socket here guarantees it exits.
	defer func() {
		_ = conn.CloseNow()
	}()

	log.Printf("httpapi: websocket %d connected from %s", c.id, r.RemoteAddr)
	defer func() {
		log.Printf(
			"httpapi: websocket %d closed with %d: %s",
			c.id,
			c.closeCode,
			c.closeReason,
		)
	}()

	if err := c.writeJSON(wsReadyFrame{
		Type:    "ready",
		Version: wsProtocolVersion,
		Sender:  s.senderKey,
	}); err != nil {
		_ = conn.Close(websocket.StatusInternalError, "write failed")
		return
	}

	go c.readLoop()
	c.writeLoop()
}

// shutdown records why the connection is closing and cancels its context. The
// first caller wins, so a server-side shutdown is not overwritten by a later
// read error.
func (c *wsConn) shutdown(code websocket.StatusCode, reason string) {
	c.closeOnce.Do(func() {
		c.closeCode = code
		c.closeReason = reason
		c.cancel()
		// Reads and writes are not interrupted by the context once they are
		// blocked in the socket, so force the socket closed if the graceful
		// path does not finish.
		time.AfterFunc(wsCloseGrace, func() {
			_ = c.ws.CloseNow()
		})
	})
}

func (c *wsConn) readLoop() {
	for {
		// Read must not use c.ctx: the library closes the socket as soon as
		// the read context is canceled, which would skip the close handshake.
		// shutdown unblocks this read by closing the connection.
		typ, data, err := c.ws.Read(context.Background())
		if err != nil {
			if status := websocket.CloseStatus(err); status != -1 {
				c.shutdown(status, "")
			} else if c.ctx.Err() == nil {
				log.Printf("httpapi: websocket %d read: %v", c.id, err)
				c.shutdown(websocket.StatusInternalError, "read failed")
			}
			return
		}
		if typ != websocket.MessageText {
			c.shutdown(websocket.StatusUnsupportedData, "text frames only")
			return
		}

		select {
		case c.inbound <- data:
		case <-c.ctx.Done():
			return
		}
	}
}

// writeLoop is the only writer on the connection. It also owns the keepalive
// ping, because Ping must not run concurrently with Write.
func (c *wsConn) writeLoop() {
	ticker := time.NewTicker(c.server.pingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			_ = c.ws.Close(c.closeCode, c.closeReason)
			return

		case <-ticker.C:
			pingCtx, cancel := context.WithTimeout(c.ctx, wsPongTimeout)
			err := c.ws.Ping(pingCtx)
			cancel()
			if err != nil {
				c.shutdown(statusGoingAway, "keepalive timeout")
			}

		case data := <-c.inbound:
			c.handleFrame(data)
		}
	}
}

func (c *wsConn) handleFrame(data []byte) {
	var request wsRequest
	if err := json.Unmarshal(data, &request); err != nil {
		c.sendError("", "bad_request", "invalid JSON frame", 0)
		return
	}

	switch request.Type {
	case "send":
		c.sendError(
			request.RequestID,
			"not_implemented",
			"sending over websocket is not implemented yet",
			0,
		)
	default:
		c.sendError(
			request.RequestID,
			"bad_request",
			fmt.Sprintf("unsupported frame type %q", request.Type),
			0,
		)
	}
}

func (c *wsConn) sendError(requestID, code, message string, retryAfterMS int64) {
	frame := wsErrorFrame{
		Type:         "error",
		RequestID:    requestID,
		Code:         code,
		Message:      message,
		RetryAfterMS: retryAfterMS,
	}
	if err := c.writeJSON(frame); err != nil {
		c.shutdown(websocket.StatusInternalError, "write failed")
	}
}

func (c *wsConn) writeJSON(value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(c.ctx, wsWriteTimeout)
	defer cancel()

	// The write context bounds lock acquisition, not the socket. Cut off a
	// client that stops reading instead of blocking this goroutine forever.
	timer := time.AfterFunc(wsWriteTimeout, func() {
		_ = c.ws.CloseNow()
	})
	defer timer.Stop()

	return c.ws.Write(ctx, websocket.MessageText, data)
}
