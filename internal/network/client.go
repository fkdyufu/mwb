// internal/network/client.go
package network

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bketelsen/mwb/internal/protocol"
)

// Conn represents an established, encrypted MWB connection.
type Conn struct {
	raw        net.Conn
	enc        *protocol.EncryptWriter
	dec        *protocol.DecryptReader
	magic      uint32
	MachineID  uint32
	LocalName  string
	RemoteName string
	nextID     atomic.Int32
	// securityKey and scheme are reused for clipboard connections.
	securityKey string
	scheme      protocol.Scheme
	// sendMu serializes writes to the CBC stream, which the receive loop and
	// the clipboard watcher both send on.
	sendMu sync.Mutex
}

// handshakeTimeout bounds each read phase of connection setup. The server
// resolves the client's name before sending anything, which takes ~5s.
const handshakeTimeout = 10 * time.Second

// Connect establishes a TCP connection, performs IV exchange and handshake.
func Connect(addr, securityKey, machineName string, timeout time.Duration) (*Conn, error) {
	magic := protocol.Get24BitHash(securityKey)

	raw, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return nil, fmt.Errorf("dial: %w", err)
	}

	ok := false
	defer func() {
		if !ok {
			_ = raw.Close()
		}
	}()

	if tc, ok := raw.(*net.TCPConn); ok {
		_ = tc.SetNoDelay(true)
	}

	// The server sends its stream preamble and handshakes without waiting for
	// ours, so read the start of its stream to learn which encryption scheme
	// its PowerToys version uses, then answer with the same scheme.
	probe := make([]byte, protocol.ProbeSize)
	_ = raw.SetReadDeadline(time.Now().Add(handshakeTimeout))
	if _, err := io.ReadFull(raw, probe); err != nil {
		return nil, fmt.Errorf("read stream preamble: %w", err)
	}
	_ = raw.SetReadDeadline(time.Time{})

	scheme, peerKey, peerIV, offset, err := protocol.DetectScheme(probe, securityKey, magic)
	if err != nil {
		return nil, err
	}
	slog.Debug("detected encryption scheme", "scheme", scheme)

	dec, err := protocol.NewDecryptReader(io.MultiReader(bytes.NewReader(probe[offset:]), raw), peerKey, peerIV)
	if err != nil {
		return nil, fmt.Errorf("decrypt stream: %w", err)
	}
	enc, err := protocol.NewSchemeEncryptWriter(raw, securityKey, scheme)
	if err != nil {
		return nil, fmt.Errorf("encrypt stream: %w", err)
	}

	// IV exchange: send random 16-byte block, read peer's random block
	ranData := make([]byte, 16)
	if _, err := rand.Read(ranData); err != nil {
		return nil, fmt.Errorf("rand: %w", err)
	}
	if _, err := enc.Write(ranData); err != nil {
		return nil, fmt.Errorf("send IV block: %w", err)
	}
	peerRan := make([]byte, 16)
	if _, err := io.ReadFull(dec, peerRan); err != nil {
		return nil, fmt.Errorf("read IV block: %w", err)
	}

	// Generate a machine ID from random data
	machineID := uint32(ranData[0])<<24 | uint32(ranData[1])<<16 | uint32(ranData[2])<<8 | uint32(ranData[3])
	if machineID == 0 || machineID == 255 {
		machineID = 1
	}

	c := &Conn{
		raw:         raw,
		enc:         enc,
		dec:         dec,
		magic:       magic,
		MachineID:   machineID,
		LocalName:   machineName,
		securityKey: securityKey,
		scheme:      scheme,
	}

	if err := c.doHandshake(machineName); err != nil {
		return nil, fmt.Errorf("handshake: %w", err)
	}

	// Send initial heartbeat to trigger device registration on the server
	hb := &protocol.Packet{
		Type: protocol.HeartbeatEx,
		Src:  c.MachineID,
		Des:  protocol.IDAll,
	}
	hb.SetMachineName(machineName)
	if err := c.SendPacket(hb); err != nil {
		return nil, fmt.Errorf("send heartbeat: %w", err)
	}

	ok = true
	return c, nil
}

func (c *Conn) doHandshake(machineName string) error {
	hs := &protocol.Packet{
		Type: protocol.Handshake,
		ID:   1,
		Src:  c.MachineID,
		Des:  0,
	}

	// Random machine fields for challenge
	challenge := make([]byte, 16)
	_, _ = rand.Read(challenge)
	hs.Handshake.Machine1 = uint32(challenge[0])<<24 | uint32(challenge[1])<<16 | uint32(challenge[2])<<8 | uint32(challenge[3])
	hs.Handshake.Machine2 = uint32(challenge[4])<<24 | uint32(challenge[5])<<16 | uint32(challenge[6])<<8 | uint32(challenge[7])
	hs.Handshake.Machine3 = uint32(challenge[8])<<24 | uint32(challenge[9])<<16 | uint32(challenge[10])<<8 | uint32(challenge[11])
	hs.Handshake.Machine4 = uint32(challenge[12])<<24 | uint32(challenge[13])<<16 | uint32(challenge[14])<<8 | uint32(challenge[15])
	hs.SetMachineName(machineName)

	// Expected response: bitwise inverted fields
	expect1 := ^hs.Handshake.Machine1
	expect2 := ^hs.Handshake.Machine2
	expect3 := ^hs.Handshake.Machine3
	expect4 := ^hs.Handshake.Machine4

	// Send 10 handshake packets (per MWB protocol)
	for i := 0; i < 10; i++ {
		if err := c.SendPacket(hs); err != nil {
			return fmt.Errorf("send handshake %d: %w", i, err)
		}
	}

	// Read packets until we get a valid HandshakeAck
	_ = c.raw.SetReadDeadline(time.Now().Add(handshakeTimeout))
	defer func() { _ = c.raw.SetReadDeadline(time.Time{}) }()

	for i := 0; i < 20; i++ {
		pkt, err := c.RecvPacket()
		if err != nil {
			return fmt.Errorf("recv: %w", err)
		}

		if pkt.Type == protocol.Handshake {
			// Peer's handshake; respond with ACK
			ack := &protocol.Packet{
				Type: protocol.HandshakeAck,
				Src:  c.MachineID,
				Des:  pkt.Src,
			}
			ack.Handshake.Machine1 = ^pkt.Handshake.Machine1
			ack.Handshake.Machine2 = ^pkt.Handshake.Machine2
			ack.Handshake.Machine3 = ^pkt.Handshake.Machine3
			ack.Handshake.Machine4 = ^pkt.Handshake.Machine4
			ack.SetMachineName(machineName)
			if err := c.SendPacket(ack); err != nil {
				return fmt.Errorf("send handshake ack: %w", err)
			}
			continue
		}

		if pkt.Type == protocol.HandshakeAck {
			if pkt.Handshake.Machine1 == expect1 &&
				pkt.Handshake.Machine2 == expect2 &&
				pkt.Handshake.Machine3 == expect3 &&
				pkt.Handshake.Machine4 == expect4 {
				c.RemoteName = pkt.MachineName()
				slog.Info("handshake complete", "remote", c.RemoteName)
				return nil
			}
			return fmt.Errorf("handshake verification failed")
		}
	}

	return fmt.Errorf("no HandshakeAck received")
}

// SendPacket marshals, stamps, and sends a packet.
// Automatically assigns an incrementing packet ID (starting at 1) to avoid
// the server's dedup ring buffer which is zero-initialized.
func (c *Conn) SendPacket(p *protocol.Packet) error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	p.ID = c.nextID.Add(1)
	buf := p.Marshal()
	protocol.StampPacket(buf, c.magic)
	_, err := c.enc.Write(buf)
	return err
}

// SendClipboard sends a clipboard payload to all machines as ClipboardText or
// ClipboardImage packets, followed by ClipboardDataEnd.
func (c *Conn) SendClipboard(image bool, payload []byte) error {
	for _, p := range clipboardPackets(c.MachineID, image, payload) {
		if err := c.SendPacket(p); err != nil {
			return err
		}
	}
	return nil
}

// clipboardPackets splits payload into ClipboardChunkSize-byte clipboard data
// packets from src, zero-padding the last one, and appends ClipboardDataEnd.
func clipboardPackets(src uint32, image bool, payload []byte) []*protocol.Packet {
	typ := protocol.ClipboardText
	if image {
		typ = protocol.ClipboardImage
	}
	var pkts []*protocol.Packet
	for off := 0; off < len(payload); off += protocol.ClipboardChunkSize {
		p := &protocol.Packet{Type: typ, Src: src, Des: protocol.IDAll}
		copy(p.ClipboardData[:], payload[off:])
		pkts = append(pkts, p)
	}
	return append(pkts, &protocol.Packet{Type: protocol.ClipboardDataEnd, Src: src, Des: protocol.IDAll})
}

// clipboardIdleTimeout bounds how long a clipboard transfer may stall.
const clipboardIdleTimeout = 30 * time.Second

// clipboardHeaderSize is the size of the header preceding clipboard
// connection data: UTF-16LE "<size>*<name>", zero-padded.
const clipboardHeaderSize = 1024

// AnnounceClipboard tells the peer that clipboard data it has to request is
// available: files and contents too big for the message connection.
func (c *Conn) AnnounceClipboard() error {
	p := &protocol.Packet{Type: protocol.Clipboard, Src: c.MachineID, Des: protocol.IDAll}
	p.SetMachineName(c.LocalName)
	return c.SendPacket(p)
}

// FetchClipboard requests the clipboard data the peer announced with a
// Clipboard packet over a clipboard connection. It returns the name from the
// transfer header ("text", "image" or the file path; for a size of 0, why
// nothing is sent) and a reader for size bytes.
func (c *Conn) FetchClipboard() (name string, size int64, body io.ReadCloser, err error) {
	raw, dec, _, err := c.dialClipboard(protocol.Clipboard, 0)
	if err != nil {
		return "", 0, nil, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = raw.Close()
		}
	}()

	// The peer closes the connection here if it rejected our handshake.
	header := make([]byte, clipboardHeaderSize)
	if _, err := io.ReadFull(dec, header); err != nil {
		return "", 0, nil, fmt.Errorf("read clipboard header (handshake rejected?): %w", err)
	}
	sizeText, name, found := strings.Cut(strings.ReplaceAll(protocol.DecodeUTF16LE(header), "\x00", ""), "*")
	if size, err = strconv.ParseInt(sizeText, 10, 64); !found || err != nil || size < 0 {
		return "", 0, nil, fmt.Errorf("invalid clipboard header %q", sizeText)
	}

	ok = true
	return name, size, readCloser{io.LimitReader(dec, size), raw}, nil
}

// PushClipboard sends clipboard data the peer requested with ClipboardAsk
// over a clipboard connection: name is "text", "image" or a file path, and
// body holds size bytes. postAction is the one from the request.
func (c *Conn) PushClipboard(postAction uint32, name string, size int64, body io.Reader) error {
	header := protocol.EncodeUTF16LE(fmt.Sprintf("%d*%s", size, name))
	if len(header) > clipboardHeaderSize {
		return fmt.Errorf("clipboard name too long: %q", name)
	}
	header = append(header, make([]byte, clipboardHeaderSize-len(header))...)

	raw, _, enc, err := c.dialClipboard(protocol.ClipboardPush, postAction)
	if err != nil {
		return err
	}
	defer func() { _ = raw.Close() }()

	if _, err := enc.Write(header); err != nil {
		return fmt.Errorf("send clipboard header: %w", err)
	}
	// The peer reads data until the connection closes, then keeps size bytes.
	buf := make([]byte, 64*1024)
	for sent := int64(0); sent < size; {
		n, err := io.ReadFull(body, buf[:min(int64(len(buf)), size-sent)])
		if err != nil {
			return fmt.Errorf("read clipboard data after %d of %d bytes: %w", sent, size, err)
		}
		sent += int64(n)
		if pad := n % 16; pad != 0 {
			clear(buf[n : n+16-pad])
			n += 16 - pad
		}
		if _, err := enc.Write(buf[:n]); err != nil {
			return fmt.Errorf("send clipboard data: %w", err)
		}
	}
	return nil
}

// dialClipboard opens a clipboard connection to the peer's clipboard port,
// which is one below its message port. Both sides send their scheme preamble,
// a random block and an identifying packet, typ: Clipboard to request data or
// ClipboardPush to send it. The packets are not stamped with magic or
// checksum.
func (c *Conn) dialClipboard(typ protocol.PackageType, postAction uint32) (raw net.Conn, dec io.Reader, enc io.Writer, err error) {
	remote, ok := c.raw.RemoteAddr().(*net.TCPAddr)
	if !ok {
		return nil, nil, nil, fmt.Errorf("unexpected remote address %v", c.raw.RemoteAddr())
	}
	addr := net.JoinHostPort(remote.IP.String(), strconv.Itoa(remote.Port-1))
	raw, err = net.DialTimeout("tcp", addr, handshakeTimeout)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("dial clipboard port: %w", err)
	}
	defer func() {
		if err != nil {
			_ = raw.Close()
		}
	}()

	w, err := protocol.NewSchemeEncryptWriter(raw, c.securityKey, c.scheme)
	if err != nil {
		return nil, nil, nil, err
	}
	ranData := make([]byte, 16)
	if _, err := rand.Read(ranData); err != nil {
		return nil, nil, nil, fmt.Errorf("rand: %w", err)
	}
	hello := &protocol.Packet{Type: typ, Src: c.MachineID, PostAction: postAction}
	hello.SetMachineName(c.LocalName)
	if _, err := w.Write(append(ranData, hello.Marshal()...)); err != nil {
		return nil, nil, nil, fmt.Errorf("send clipboard handshake: %w", err)
	}

	r, err := protocol.NewSchemeDecryptReader(&idleTimeoutReader{conn: raw, timeout: clipboardIdleTimeout}, c.securityKey, c.scheme)
	if err != nil {
		return nil, nil, nil, err
	}
	buf := make([]byte, 16+protocol.PacketSizeEx)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, nil, nil, fmt.Errorf("read clipboard handshake: %w", err)
	}
	if pt := protocol.PackageType(buf[16]); pt != protocol.Clipboard && pt != protocol.ClipboardPush {
		return nil, nil, nil, fmt.Errorf("unexpected clipboard handshake packet type %d", pt)
	}
	return raw, r, w, nil
}

// idleTimeoutReader fails a read that waits longer than timeout for data.
type idleTimeoutReader struct {
	conn    net.Conn
	timeout time.Duration
}

func (r *idleTimeoutReader) Read(p []byte) (int, error) {
	_ = r.conn.SetReadDeadline(time.Now().Add(r.timeout))
	return r.conn.Read(p)
}

type readCloser struct {
	io.Reader
	io.Closer
}

// RecvPacket reads, validates, and unmarshals a packet.
func (c *Conn) RecvPacket() (*protocol.Packet, error) {
	buf := make([]byte, protocol.PacketSize)
	if _, err := io.ReadFull(c.dec, buf); err != nil {
		return nil, fmt.Errorf("read packet: %w", err)
	}

	if err := protocol.ValidatePacket(buf, c.magic); err != nil {
		return nil, err
	}
	protocol.ClearStamp(buf)

	pt := protocol.PackageType(buf[0])
	if protocol.IsBigPacket(pt) {
		ext := make([]byte, protocol.PacketSize)
		if _, err := io.ReadFull(c.dec, ext); err != nil {
			return nil, fmt.Errorf("read extended: %w", err)
		}
		full := make([]byte, protocol.PacketSizeEx)
		copy(full, buf)
		copy(full[protocol.PacketSize:], ext)
		return protocol.UnmarshalPacket(full)
	}

	return protocol.UnmarshalPacket(buf)
}

// Close closes the connection.
func (c *Conn) Close() error {
	return c.raw.Close()
}
