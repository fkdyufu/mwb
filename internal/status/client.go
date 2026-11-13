// internal/status/client.go
package status

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"time"
)

// retryInterval is how often Watch looks for mwb while it is not running.
var retryInterval = time.Second

// stopped returns the Status clients report when no mwb serves the socket.
func stopped() Status {
	return Status{V: Version, State: Stopped, Since: time.Now().Unix()}
}

// Current returns the Status served at path, or a Stopped one when no mwb
// serves it.
func Current(path string) (Status, error) {
	c, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		return stopped(), nil
	}
	defer func() { _ = c.Close() }()
	_ = c.SetReadDeadline(time.Now().Add(time.Second))
	var s Status
	if err := json.NewDecoder(c).Decode(&s); err != nil {
		return Status{}, fmt.Errorf("read status: %w", err)
	}
	return s, nil
}

// Watch writes the Status served at path to w as JSON lines, the current one
// first, then one per change, until ctx is done. While no mwb serves it, Watch
// writes a Stopped Status once and keeps looking, so it outlives mwb restarts.
func Watch(ctx context.Context, path string, w io.Writer) error {
	var d net.Dialer
	// last is the last line relayed, nil after a Stopped one.
	var last []byte
	isStopped := false
	for {
		if c, err := d.DialContext(ctx, "unix", path); err == nil {
			isStopped = false
			if err := relay(ctx, c, w, &last); err != nil {
				return err
			}
		}
		if ctx.Err() != nil {
			return nil
		}
		if !isStopped {
			isStopped = true
			last = nil
			if err := json.NewEncoder(w).Encode(stopped()); err != nil {
				return err
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(retryInterval):
		}
	}
}

// relay copies the lines served on c to w until c is closed or ctx is done,
// skipping a line equal to the last one, as a reconnection to the same mwb
// sends. It only fails writing to w.
func relay(ctx context.Context, c net.Conn, w io.Writer, last *[]byte) error {
	defer func() { _ = c.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stop()
	sc := bufio.NewScanner(c)
	for sc.Scan() {
		line := sc.Bytes()
		if bytes.Equal(line, *last) {
			continue
		}
		if _, err := fmt.Fprintf(w, "%s\n", line); err != nil {
			return err
		}
		*last = append((*last)[:0], line...)
	}
	return nil
}
