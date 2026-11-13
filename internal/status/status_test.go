// internal/status/status_test.go
package status

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"path/filepath"
	"testing"
	"time"
)

func TestHubUpdate(t *testing.T) {
	h := NewHub(Status{State: Disconnected, Host: "10.0.0.1"})
	wake := h.subscribe()
	defer h.unsubscribe(wake)

	h.Update(func(s *Status) { s.Error = "" })
	select {
	case <-wake:
		t.Fatal("woken up by an update that changed nothing")
	default:
	}

	start := h.Get()
	if start.V != Version {
		t.Errorf("V = %d, want %d", start.V, Version)
	}
	h.Update(func(s *Status) { s.Error = "timeout" })
	if got := h.Get(); got.Error != "timeout" || got.Since != start.Since {
		t.Errorf("after an error update: %+v, want the error and Since %d", got, start.Since)
	}
	h.Update(func(s *Status) {
		s.State = Connected
		s.Remote = "DESKTOP"
		s.Since = 0
	})
	if got := h.Get(); got.State != Connected || got.Since == 0 {
		t.Errorf("after connecting: %+v, want Connected with Since set", got)
	}

	// Both changes wake the client up once.
	<-wake
	select {
	case <-wake:
		t.Fatal("woken up twice")
	default:
	}
}

// lines decodes the JSON lines read from r.
func lines(r io.Reader) <-chan Status {
	ch := make(chan Status, 16)
	go func() {
		defer close(ch)
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			var s Status
			if json.Unmarshal(sc.Bytes(), &s) == nil {
				ch <- s
			}
		}
	}()
	return ch
}

func next(t *testing.T, ch <-chan Status) Status {
	t.Helper()
	select {
	case s, ok := <-ch:
		if !ok {
			t.Fatal("stream ended")
		}
		return s
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a status")
	}
	return Status{}
}

func TestServeAndWatch(t *testing.T) {
	retryInterval = 10 * time.Millisecond
	t.Cleanup(func() { retryInterval = time.Second })
	path := filepath.Join(t.TempDir(), "mwb.sock")

	ctx, cancel := context.WithCancel(context.Background())
	r, w := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- Watch(ctx, path, w) }()
	ch := lines(r)

	if s := next(t, ch); s.State != Stopped || s.V != Version {
		t.Fatalf("before mwb runs: %+v, want Stopped", s)
	}

	hub := NewHub(Status{State: Disconnected, Host: "10.0.0.1"})
	srv, err := Listen(path, hub)
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	if s := next(t, ch); s.State != Disconnected || s.Host != "10.0.0.1" {
		t.Fatalf("first status: %+v, want Disconnected from 10.0.0.1", s)
	}

	hub.Update(func(s *Status) {
		s.State = Connected
		s.Remote = "DESKTOP"
	})
	if s := next(t, ch); s.State != Connected || s.Remote != "DESKTOP" {
		t.Fatalf("after connecting: %+v, want Connected to DESKTOP", s)
	}

	if err := srv.Close(); err != nil {
		t.Fatal(err)
	}
	if s := next(t, ch); s.State != Stopped {
		t.Fatalf("after mwb stops: %+v, want Stopped", s)
	}

	// A new mwb is picked up again.
	srv, err = Listen(path, NewHub(Status{State: Disconnected}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()
	go srv.Serve()
	if s := next(t, ch); s.State != Disconnected {
		t.Fatalf("after mwb restarts: %+v, want Disconnected", s)
	}

	cancel()
	if err := <-done; err != nil {
		t.Errorf("Watch: %v", err)
	}
	_ = w.Close()
}

func TestCurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mwb.sock")
	s, err := Current(path)
	if err != nil || s.State != Stopped {
		t.Fatalf("Current without mwb = %+v, %v, want Stopped", s, err)
	}

	srv, err := Listen(path, NewHub(Status{State: Disconnected, Error: "timeout"}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()
	go srv.Serve()
	s, err = Current(path)
	if err != nil || s.State != Disconnected || s.Error != "timeout" {
		t.Fatalf("Current = %+v, %v, want Disconnected with the error", s, err)
	}
}

func TestListen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mwb.sock")

	// A socket left behind by an mwb that did not exit cleanly is replaced.
	stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	stale.SetUnlinkOnClose(false)
	_ = stale.Close()

	srv, err := Listen(path, NewHub(Status{State: Disconnected}))
	if err != nil {
		t.Fatalf("Listen over a stale socket: %v", err)
	}
	defer func() { _ = srv.Close() }()
	go srv.Serve()

	if _, err := Listen(path, NewHub(Status{State: Disconnected})); err == nil {
		t.Fatal("Listen succeeded while another mwb serves the socket")
	}
}
