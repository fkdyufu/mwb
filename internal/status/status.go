// internal/status/status.go

// Package status shares the state of the connection to the MWB server with
// local clients, such as a status bar widget, over a Unix socket. A client
// receives the current Status as a JSON line when it connects, then one line
// per change. Clients only listen: the server ignores what they send.
package status

import (
	"sync"
	"time"
)

// Version is the version of the Status format, bumped on incompatible
// changes.
const Version = 1

// State is the state of the connection to the MWB server.
type State string

const (
	// Stopped is reported by clients when no mwb serves the socket.
	Stopped State = "stopped"
	// Disconnected means mwb is connecting, or waiting to retry after Error.
	Disconnected State = "disconnected"
	// Connected means the handshake with Remote is complete.
	Connected State = "connected"
)

// Status is the state of mwb, sent to clients as one JSON line.
type Status struct {
	V     int   `json:"v"`
	State State `json:"state"`
	// Host is the configured MWB server.
	Host string `json:"host,omitempty"`
	// Remote is the server's machine name while connected.
	Remote string `json:"remote,omitempty"`
	// Since is when State last changed, in Unix seconds.
	Since int64 `json:"since"`
	// Error is why the last connection attempt failed or the connection was
	// lost.
	Error string `json:"error,omitempty"`
}

// Hub holds the current Status and wakes up the clients waiting for a change.
type Hub struct {
	mu      sync.Mutex
	cur     Status
	waiters map[chan struct{}]struct{}
}

// NewHub returns a Hub whose current Status is s.
func NewHub(s Status) *Hub {
	s.V = Version
	s.Since = time.Now().Unix()
	return &Hub{cur: s, waiters: make(map[chan struct{}]struct{})}
}

// Get returns the current Status.
func (h *Hub) Get() Status {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cur
}

// Update changes the current Status with fn, and wakes up the clients when it
// changed. Since is set when the State changes.
func (h *Hub) Update(fn func(*Status)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.cur
	fn(&s)
	s.V = Version
	if s.State != h.cur.State {
		s.Since = time.Now().Unix()
	}
	if s == h.cur {
		return
	}
	h.cur = s
	for ch := range h.waiters {
		select {
		case ch <- struct{}{}:
		default: // already woken up, it will read the latest Status
		}
	}
}

// subscribe returns a channel that receives a value after a change. Changes
// made before the receiver reads it are coalesced into that one value.
func (h *Hub) subscribe() chan struct{} {
	ch := make(chan struct{}, 1)
	h.mu.Lock()
	h.waiters[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *Hub) unsubscribe(ch chan struct{}) {
	h.mu.Lock()
	delete(h.waiters, ch)
	h.mu.Unlock()
}
