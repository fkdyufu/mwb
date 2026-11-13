// internal/status/server.go
package status

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"time"
)

// writeTimeout drops clients that stop reading.
const writeTimeout = 5 * time.Second

// SocketPath returns the Unix socket mwb serves its Status on.
func SocketPath() string {
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		return filepath.Join(dir, "mwb.sock")
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("mwb-%d.sock", os.Getuid()))
}

// Server serves a Hub's Status on a Unix socket.
type Server struct {
	hub  *Hub
	ln   net.Listener
	done chan struct{}
}

// Listen listens on the Unix socket at path, replacing a socket left behind by
// an mwb that did not exit cleanly. It fails while another mwb serves it.
func Listen(path string, hub *Hub) (*Server, error) {
	if c, err := net.Dial("unix", path); err == nil {
		_ = c.Close()
		return nil, fmt.Errorf("%s is served by another mwb", path)
	}
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return nil, err
	}
	return &Server{hub: hub, ln: ln, done: make(chan struct{})}, nil
}

// Serve accepts clients until Close.
func (s *Server) Serve() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			select {
			case <-s.done:
			default:
				slog.Warn("status socket stopped", "err", err)
			}
			return
		}
		go s.serve(c)
	}
}

// Close stops the server, disconnects its clients and removes the socket.
func (s *Server) Close() error {
	close(s.done)
	return s.ln.Close()
}

// serve sends the current Status to the client, then each change.
func (s *Server) serve(c net.Conn) {
	defer func() { _ = c.Close() }()
	wake := s.hub.subscribe()
	defer s.hub.unsubscribe(wake)

	// A read only returns when the client hangs up.
	gone := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, c)
		close(gone)
	}()

	enc := json.NewEncoder(c)
	var last Status
	for {
		// A change made after subscribing may already be in cur.
		if cur := s.hub.Get(); cur != last {
			_ = c.SetWriteDeadline(time.Now().Add(writeTimeout))
			if err := enc.Encode(cur); err != nil {
				return
			}
			last = cur
		}
		select {
		case <-wake:
		case <-gone:
			return
		case <-s.done:
			return
		}
	}
}
