// internal/network/client_test.go
package network

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/bketelsen/mwb/internal/protocol"
)

// runFakeServer behaves like a Windows MWB server: it sends its preamble and
// handshakes before reading anything, then acknowledges the client's handshake.
func runFakeServer(conn net.Conn, securityKey string, scheme protocol.Scheme) error {
	magic := protocol.Get24BitHash(securityKey)

	enc, err := protocol.NewSchemeEncryptWriter(conn, securityKey, scheme)
	if err != nil {
		return err
	}

	// Server: send random IV block
	ranData := make([]byte, 16)
	_, _ = rand.Read(ranData)
	if _, err := enc.Write(ranData); err != nil {
		return err
	}

	// Server: send its own handshakes, as MainTCPRoutine does on connect
	for i := 0; i < 10; i++ {
		hs := &protocol.Packet{Type: protocol.Handshake, ID: int32(i + 1), Src: 7}
		hs.SetMachineName("WINHOST")
		buf := hs.Marshal()
		protocol.StampPacket(buf, magic)
		if _, err := enc.Write(buf); err != nil {
			return err
		}
	}

	dec, err := protocol.NewSchemeDecryptReader(conn, securityKey, scheme)
	if err != nil {
		return err
	}

	// Server: read random IV block from client
	clientRan := make([]byte, 16)
	if _, err := io.ReadFull(dec, clientRan); err != nil {
		return err
	}

	// Server: read the first handshake packet from the client
	pktBuf := make([]byte, protocol.PacketSizeEx)
	if _, err := io.ReadFull(dec, pktBuf); err != nil {
		return err
	}
	if err := protocol.ValidatePacket(pktBuf, magic); err != nil {
		return err
	}
	protocol.ClearStamp(pktBuf)

	pkt, err := protocol.UnmarshalPacket(pktBuf)
	if err != nil {
		return err
	}
	if pkt.Type != protocol.Handshake {
		return fmt.Errorf("expected Handshake, got %d", pkt.Type)
	}

	// Server: send HandshakeAck with inverted machine fields
	ack := &protocol.Packet{
		Type: protocol.HandshakeAck,
		Src:  7,
		Des:  pkt.Src,
	}
	ack.Handshake.Machine1 = ^pkt.Handshake.Machine1
	ack.Handshake.Machine2 = ^pkt.Handshake.Machine2
	ack.Handshake.Machine3 = ^pkt.Handshake.Machine3
	ack.Handshake.Machine4 = ^pkt.Handshake.Machine4
	ack.SetMachineName("WINHOST")

	ackBuf := ack.Marshal()
	protocol.StampPacket(ackBuf, magic)
	_, err = enc.Write(ackBuf)
	return err
}

func TestConnectionHandshake(t *testing.T) {
	securityKey := "TestSecurityKey!!"

	for _, scheme := range []protocol.Scheme{protocol.SchemeLegacy, protocol.SchemeHeader50k, protocol.SchemeHeader100k} {
		t.Run(scheme.String(), func(t *testing.T) {
			// Start a fake MWB server
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = ln.Close() }()

			serverDone := make(chan error, 1)
			go func() {
				conn, err := ln.Accept()
				if err != nil {
					serverDone <- err
					return
				}
				// Keep the connection open until the client is done with it
				serverDone <- runFakeServer(conn, securityKey, scheme)
				time.Sleep(500 * time.Millisecond)
				_ = conn.Close()
			}()

			// Client: connect and handshake
			conn, err := Connect(ln.Addr().String(), securityKey, "linux-test", 5*time.Second)
			if err != nil {
				t.Fatalf("Connect: %v", err)
			}
			defer func() { _ = conn.Close() }()

			if conn.RemoteName != "WINHOST" {
				t.Errorf("remote name = %q, want %q", conn.RemoteName, "WINHOST")
			}

			select {
			case err := <-serverDone:
				if err != nil {
					t.Fatalf("server error: %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("timeout waiting for server")
			}
		})
	}
}

func TestConnectWrongKey(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_ = runFakeServer(conn, "ServerSecurityKey", protocol.SchemeHeader100k)
	}()

	_, err = Connect(ln.Addr().String(), "ClientSecurityKey", "linux-test", 5*time.Second)
	if err == nil {
		t.Fatal("Connect succeeded with mismatched security key")
	}
}

// serveClipboard behaves like the MWB clipboard port: it runs the clipboard
// handshake, then sends a 1024-byte header and the data, zero-padded, and
// closes the connection.
// clipboardServerHello does the server side of a clipboard connection
// handshake, which MWB starts by sending ClipboardPush, and returns the
// client's packet.
func clipboardServerHello(conn net.Conn, securityKey string, scheme protocol.Scheme) (*protocol.EncryptWriter, io.Reader, *protocol.Packet, error) {
	enc, err := protocol.NewSchemeEncryptWriter(conn, securityKey, scheme)
	if err != nil {
		return nil, nil, nil, err
	}
	hello := &protocol.Packet{Type: protocol.ClipboardPush, Src: 7}
	hello.SetMachineName("WINHOST")
	if _, err := enc.Write(append(make([]byte, 16), hello.Marshal()...)); err != nil {
		return nil, nil, nil, err
	}

	dec, err := protocol.NewSchemeDecryptReader(conn, securityKey, scheme)
	if err != nil {
		return nil, nil, nil, err
	}
	buf := make([]byte, 16+protocol.PacketSizeEx)
	if _, err := io.ReadFull(dec, buf); err != nil {
		return nil, nil, nil, err
	}
	pkt, err := protocol.UnmarshalPacket(buf[16:])
	if err != nil {
		return nil, nil, nil, err
	}
	if pkt.MachineName() != "linux-test" {
		return nil, nil, nil, fmt.Errorf("handshake packet from %q", pkt.MachineName())
	}
	return enc, dec, pkt, nil
}

// serveClipboard sends name and data to a client requesting clipboard data.
func serveClipboard(conn net.Conn, securityKey string, scheme protocol.Scheme, name string, data []byte) error {
	defer func() { _ = conn.Close() }()

	enc, _, pkt, err := clipboardServerHello(conn, securityKey, scheme)
	if err != nil {
		return err
	}
	if pkt.Type != protocol.Clipboard {
		return fmt.Errorf("unexpected handshake packet type %d", pkt.Type)
	}

	header := make([]byte, clipboardHeaderSize)
	copy(header, protocol.EncodeUTF16LE(fmt.Sprintf("%d*%s", len(data), name)))
	body := append(header, data...)
	body = append(body, make([]byte, 16-len(body)%16)...)
	_, err = enc.Write(body)
	return err
}

// pushed is what a client pushed over a clipboard connection.
type pushed struct {
	postAction uint32
	header     string
	data       []byte
}

// receiveClipboard reads the data a client pushes, as MWB does: until the
// client closes the connection.
func receiveClipboard(conn net.Conn, securityKey string, scheme protocol.Scheme, got chan<- pushed) error {
	defer func() { _ = conn.Close() }()

	_, dec, pkt, err := clipboardServerHello(conn, securityKey, scheme)
	if err != nil {
		return err
	}
	if pkt.Type != protocol.ClipboardPush {
		return fmt.Errorf("unexpected handshake packet type %d", pkt.Type)
	}

	var all []byte
	block := make([]byte, 16)
	for {
		if _, err := io.ReadFull(dec, block); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return err
		}
		all = append(all, block...)
	}
	if len(all) < clipboardHeaderSize {
		return fmt.Errorf("received %d bytes, shorter than the header", len(all))
	}
	header := strings.ReplaceAll(protocol.DecodeUTF16LE(all[:clipboardHeaderSize]), "\x00", "")
	got <- pushed{pkt.PostAction, header, all[clipboardHeaderSize:]}
	return nil
}

// withClipboardServer connects to a fake MWB server whose clipboard port is
// served by serve, and calls run with the connection.
func withClipboardServer(t *testing.T, securityKey string, scheme protocol.Scheme, serve func(net.Conn) error, run func(*Conn)) {
	t.Helper()
	// The clipboard port is one below the message port.
	clipLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = clipLn.Close() }()
	port := clipLn.Addr().(*net.TCPAddr).Port
	msgLn, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port+1))
	if err != nil {
		t.Skipf("message port %d unavailable: %v", port+1, err)
	}
	defer func() { _ = msgLn.Close() }()

	serverDone := make(chan error, 2)
	go func() {
		conn, err := msgLn.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		serverDone <- runFakeServer(conn, securityKey, scheme)
		time.Sleep(time.Second)
		_ = conn.Close()
	}()
	go func() {
		conn, err := clipLn.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		serverDone <- serve(conn)
	}()

	conn, err := Connect(msgLn.Addr().String(), securityKey, "linux-test", 5*time.Second)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = conn.Close() }()

	run(conn)
	for range 2 {
		if err := <-serverDone; err != nil {
			t.Fatalf("server error: %v", err)
		}
	}
}

var clipboardSchemes = []protocol.Scheme{protocol.SchemeLegacy, protocol.SchemeHeader100k}

func TestFetchClipboard(t *testing.T) {
	securityKey := "TestSecurityKey!!"
	data := []byte("file contents that do not fill whole blocks")

	for _, scheme := range clipboardSchemes {
		t.Run(scheme.String(), func(t *testing.T) {
			serve := func(conn net.Conn) error {
				return serveClipboard(conn, securityKey, scheme, `C:\Users\me\notes.txt`, data)
			}
			withClipboardServer(t, securityKey, scheme, serve, func(conn *Conn) {
				name, size, body, err := conn.FetchClipboard()
				if err != nil {
					t.Fatalf("FetchClipboard: %v", err)
				}
				got, err := io.ReadAll(body)
				_ = body.Close()
				if err != nil {
					t.Fatal(err)
				}
				if name != `C:\Users\me\notes.txt` || size != int64(len(data)) || !bytes.Equal(got, data) {
					t.Errorf("got name=%q size=%d data=%q", name, size, got)
				}
			})
		})
	}
}

func TestPushClipboard(t *testing.T) {
	securityKey := "TestSecurityKey!!"
	// Bigger than PushClipboard's buffer and not a whole number of blocks.
	data := bytes.Repeat([]byte("0123456789"), 7000)

	for _, scheme := range clipboardSchemes {
		t.Run(scheme.String(), func(t *testing.T) {
			got := make(chan pushed, 1)
			serve := func(conn net.Conn) error {
				return receiveClipboard(conn, securityKey, scheme, got)
			}
			withClipboardServer(t, securityKey, scheme, serve, func(conn *Conn) {
				if err := conn.PushClipboard(1, "/notes.txt", int64(len(data)), bytes.NewReader(data)); err != nil {
					t.Fatalf("PushClipboard: %v", err)
				}
			})

			p := <-got
			if p.postAction != 1 || p.header != fmt.Sprintf("%d*/notes.txt", len(data)) {
				t.Errorf("postAction=%d header=%q", p.postAction, p.header)
			}
			if len(p.data)%16 != 0 || !bytes.Equal(p.data[:len(data)], data) || len(p.data)-len(data) >= 16 {
				t.Errorf("received %d bytes, want %d padded to a block", len(p.data), len(data))
			}
		})
	}
}

func TestPushClipboardShortBody(t *testing.T) {
	securityKey := "TestSecurityKey!!"
	scheme := protocol.SchemeHeader100k
	got := make(chan pushed, 1)
	serve := func(conn net.Conn) error {
		return receiveClipboard(conn, securityKey, scheme, got)
	}
	withClipboardServer(t, securityKey, scheme, serve, func(conn *Conn) {
		// The file shrank after its size was announced.
		if err := conn.PushClipboard(0, "/a.txt", 10, strings.NewReader("abc")); err == nil {
			t.Error("expected error for a short body")
		}
	})
	if p := <-got; len(p.data) != 0 {
		t.Errorf("sent %d data bytes, want none", len(p.data))
	}
}
