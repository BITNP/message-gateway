package httpapi

import (
	"sync"

	"github.com/coder/websocket"
)

// hub tracks live WebSocket connections so the server can cap and close them.
type hub struct {
	mu      sync.Mutex
	conns   map[*wsConn]struct{}
	pending int
	limit   int
	closed  bool
	wg      sync.WaitGroup
}

func newHub(limit int) *hub {
	return &hub{
		conns: make(map[*wsConn]struct{}),
		limit: limit,
	}
}

// reserve claims a connection slot before the upgrade so an over-limit client
// gets an HTTP error instead of an accepted socket that is closed immediately.
func (h *hub) reserve() bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.closed || len(h.conns)+h.pending >= h.limit {
		return false
	}
	h.pending++
	return true
}

// release returns a reservation that never became a connection.
func (h *hub) release() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pending--
}

// add turns a reservation into a tracked connection. It reports false when the
// hub closed while the connection was being upgraded.
func (h *hub) add(c *wsConn) bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.pending--
	if h.closed {
		return false
	}
	h.conns[c] = struct{}{}
	h.wg.Add(1)
	return true
}

func (h *hub) remove(c *wsConn) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if _, ok := h.conns[c]; ok {
		delete(h.conns, c)
		h.wg.Done()
	}
}

func (h *hub) closeAll(code websocket.StatusCode, reason string) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	conns := make([]*wsConn, 0, len(h.conns))
	for c := range h.conns {
		conns = append(conns, c)
	}
	h.mu.Unlock()

	for _, c := range conns {
		c.shutdown(code, reason)
	}
}

func (h *hub) wait() {
	h.wg.Wait()
}
