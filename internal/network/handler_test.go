// internal/network/handler_test.go
package network

import (
	"bytes"
	"testing"

	"github.com/bketelsen/mwb/internal/input"
	"github.com/bketelsen/mwb/internal/protocol"
)

// MockInputDevice records calls for testing.
type MockInputDevice struct {
	MouseMoves  []struct{ X, Y int32 }
	ButtonDowns []uint16
	ButtonUps   []uint16
	Wheels      []int32
	KeyDowns    []uint16
	KeyUps      []uint16
}

func (m *MockInputDevice) MoveTo(x, y int32) error {
	m.MouseMoves = append(m.MouseMoves, struct{ X, Y int32 }{x, y})
	return nil
}
func (m *MockInputDevice) ButtonDown(btn uint16) error {
	m.ButtonDowns = append(m.ButtonDowns, btn)
	return nil
}
func (m *MockInputDevice) ButtonUp(btn uint16) error {
	m.ButtonUps = append(m.ButtonUps, btn)
	return nil
}
func (m *MockInputDevice) Wheel(delta int32) error {
	m.Wheels = append(m.Wheels, delta)
	return nil
}
func (m *MockInputDevice) KeyDown(code uint16) error {
	m.KeyDowns = append(m.KeyDowns, code)
	return nil
}
func (m *MockInputDevice) KeyUp(code uint16) error {
	m.KeyUps = append(m.KeyUps, code)
	return nil
}

func TestHandleMouseMove(t *testing.T) {
	mock := &MockInputDevice{}
	h := &Handler{Mouse: mock, Keyboard: mock}

	pkt := &protocol.Packet{Type: protocol.Mouse}
	pkt.Mouse.X = 32768
	pkt.Mouse.Y = 16384
	pkt.Mouse.DwFlags = protocol.WM_MOUSEMOVE

	h.HandlePacket(pkt)

	if len(mock.MouseMoves) != 1 {
		t.Fatalf("expected 1 move, got %d", len(mock.MouseMoves))
	}
	if mock.MouseMoves[0].X != 32768 || mock.MouseMoves[0].Y != 16384 {
		t.Errorf("move = (%d,%d), want (32768,16384)", mock.MouseMoves[0].X, mock.MouseMoves[0].Y)
	}
}

func TestHandleMouseButtons(t *testing.T) {
	mock := &MockInputDevice{}
	h := &Handler{Mouse: mock, Keyboard: mock}

	pkt := &protocol.Packet{Type: protocol.Mouse}
	pkt.Mouse.DwFlags = protocol.WM_LBUTTONDOWN
	h.HandlePacket(pkt)
	if len(mock.ButtonDowns) != 1 || mock.ButtonDowns[0] != input.BTN_LEFT {
		t.Errorf("expected BTN_LEFT down, got %v", mock.ButtonDowns)
	}

	pkt.Mouse.DwFlags = protocol.WM_LBUTTONUP
	h.HandlePacket(pkt)
	if len(mock.ButtonUps) != 1 || mock.ButtonUps[0] != input.BTN_LEFT {
		t.Errorf("expected BTN_LEFT up, got %v", mock.ButtonUps)
	}
}

func TestHandleKeyboard(t *testing.T) {
	mock := &MockInputDevice{}
	h := &Handler{Mouse: mock, Keyboard: mock}

	expectedCode, ok := input.VKToKeyCode(0x41)
	if !ok {
		t.Fatal("VKToKeyCode(0x41) should map VK_A")
	}

	// Key down: VK_A (0x41)
	pkt := &protocol.Packet{Type: protocol.Keyboard}
	pkt.Keyboard.WVk = 0x41
	pkt.Keyboard.DwFlags = 0

	h.HandlePacket(pkt)
	if len(mock.KeyDowns) != 1 || mock.KeyDowns[0] != expectedCode {
		t.Errorf("expected keycode %d down, got %v", expectedCode, mock.KeyDowns)
	}

	// Key up: VK_A with LLKHF_UP (0x80)
	pkt.Keyboard.DwFlags = protocol.LLKHF_UP
	h.HandlePacket(pkt)
	if len(mock.KeyUps) != 1 || mock.KeyUps[0] != expectedCode {
		t.Errorf("expected keycode %d up, got %v", expectedCode, mock.KeyUps)
	}
}

func TestHandleNumpadWithWindowsNumLockOff(t *testing.T) {
	keyCode := func(vk int32) uint16 {
		code, ok := input.VKToKeyCode(vk)
		if !ok {
			t.Fatalf("VKToKeyCode(0x%X) not mapped", vk)
		}
		return code
	}
	for _, tc := range []struct {
		name  string
		vk    int32
		flags int32
		want  uint16
	}{
		{"number pad 1", 0x23, 0, keyCode(0x61)},                        // VK_END
		{"number pad 5", 0x0C, 0, keyCode(0x65)},                        // VK_CLEAR
		{"number pad dot", 0x2E, protocol.LLKHF_UP, keyCode(0x6E)},      // VK_DELETE
		{"dedicated End", 0x23, protocol.LLKHF_EXTENDED, keyCode(0x23)}, // E0-prefixed
		{"injected End", 0x23, protocol.LLKHF_INJECTED, keyCode(0x23)},
		{"number pad with NumLock on", 0x61, 0, keyCode(0x61)}, // VK_NUMPAD1
		{"dedicated Left released", 0x25, protocol.LLKHF_EXTENDED | protocol.LLKHF_UP, keyCode(0x25)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mock := &MockInputDevice{}
			h := &Handler{Mouse: mock, Keyboard: mock}
			pkt := &protocol.Packet{Type: protocol.Keyboard}
			pkt.Keyboard.WVk = tc.vk
			pkt.Keyboard.DwFlags = tc.flags
			h.HandlePacket(pkt)

			got := mock.KeyDowns
			if tc.flags&protocol.LLKHF_UP != 0 {
				got = mock.KeyUps
			}
			if len(got) != 1 || got[0] != tc.want {
				t.Errorf("keys = %v, want [%d]", got, tc.want)
			}
		})
	}
}

func TestHideMouseReleasesPressedKeys(t *testing.T) {
	mock := &MockInputDevice{}
	h := &Handler{Mouse: mock, Keyboard: mock}

	key := func(vk int32, flags int32) {
		pkt := &protocol.Packet{Type: protocol.Keyboard}
		pkt.Keyboard.WVk = vk
		pkt.Keyboard.DwFlags = flags
		h.HandlePacket(pkt)
	}
	ctrl, _ := input.VKToKeyCode(0xA2) // VK_LCONTROL
	key(0xA2, 0)
	key(0x41, 0)
	key(0x41, protocol.LLKHF_UP)

	// The mouse leaves while Ctrl is still down.
	h.HandlePacket(&protocol.Packet{Type: protocol.HideMouse})
	if len(mock.KeyUps) != 2 || mock.KeyUps[1] != ctrl {
		t.Fatalf("key ups = %v, want A then Ctrl (%d)", mock.KeyUps, ctrl)
	}

	h.HandlePacket(&protocol.Packet{Type: protocol.HideMouse})
	if len(mock.KeyUps) != 2 {
		t.Errorf("released keys again: %v", mock.KeyUps)
	}
}

func TestHandleMouseWheel(t *testing.T) {
	mock := &MockInputDevice{}
	h := &Handler{Mouse: mock, Keyboard: mock}

	pkt := &protocol.Packet{Type: protocol.Mouse}
	pkt.Mouse.DwFlags = protocol.WM_MOUSEWHEEL
	pkt.Mouse.WheelDelta = 120

	h.HandlePacket(pkt)
	if len(mock.Wheels) != 1 || mock.Wheels[0] != 1 {
		t.Errorf("expected wheel=1, got %v", mock.Wheels)
	}
}

func TestHandleRightButton(t *testing.T) {
	mock := &MockInputDevice{}
	h := &Handler{Mouse: mock, Keyboard: mock}

	pkt := &protocol.Packet{Type: protocol.Mouse}
	pkt.Mouse.DwFlags = protocol.WM_RBUTTONDOWN
	h.HandlePacket(pkt)
	if len(mock.ButtonDowns) != 1 || mock.ButtonDowns[0] != input.BTN_RIGHT {
		t.Errorf("expected BTN_RIGHT down, got %v", mock.ButtonDowns)
	}
}

func TestHandleMiddleButton(t *testing.T) {
	mock := &MockInputDevice{}
	h := &Handler{Mouse: mock, Keyboard: mock}

	pkt := &protocol.Packet{Type: protocol.Mouse}
	pkt.Mouse.DwFlags = protocol.WM_MBUTTONDOWN
	h.HandlePacket(pkt)
	if len(mock.ButtonDowns) != 1 || mock.ButtonDowns[0] != input.BTN_MIDDLE {
		t.Errorf("expected BTN_MIDDLE down, got %v", mock.ButtonDowns)
	}
}

// fakeClipboard records clipboard payloads handed to the sink.
type fakeClipboard struct {
	applied []struct {
		image   bool
		payload []byte
	}
	announced int
	switched  int
	pushes    []uint32
}

func (f *fakeClipboard) ApplyRemote(image bool, payload []byte) {
	f.applied = append(f.applied, struct {
		image   bool
		payload []byte
	}{image, payload})
}

func (f *fakeClipboard) RemoteHasData()   { f.announced++ }
func (f *fakeClipboard) MachineSwitched() { f.switched++ }
func (f *fakeClipboard) PushRequested(postAction uint32) {
	f.pushes = append(f.pushes, postAction)
}

// roundTrip marshals and unmarshals p, as it would cross the wire.
func roundTrip(t *testing.T, p *protocol.Packet) *protocol.Packet {
	t.Helper()
	got, err := protocol.UnmarshalPacket(p.Marshal())
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestHandleClipboardReassembly(t *testing.T) {
	mock := &MockInputDevice{}
	clip := &fakeClipboard{}
	h := &Handler{Mouse: mock, Keyboard: mock, Clipboard: clip}

	payload := make([]byte, 100)
	for i := range payload {
		payload[i] = byte(i + 1)
	}
	pkts := clipboardPackets(7, true, payload)
	if len(pkts) != 4 {
		t.Fatalf("got %d packets, want 3 chunks + end", len(pkts))
	}

	for i, p := range pkts {
		h.HandlePacket(roundTrip(t, p))
		if i == 0 {
			// Other packets may arrive in the middle of a transfer
			move := &protocol.Packet{Type: protocol.Mouse}
			move.Mouse.X, move.Mouse.Y, move.Mouse.DwFlags = 10, 20, protocol.WM_MOUSEMOVE
			h.HandlePacket(move)
		}
	}

	if len(mock.MouseMoves) != 1 {
		t.Errorf("mouse moves = %d, want 1", len(mock.MouseMoves))
	}
	if len(clip.applied) != 1 {
		t.Fatalf("applied %d payloads, want 1", len(clip.applied))
	}
	got := clip.applied[0]
	if !got.image {
		t.Error("image payload applied as text")
	}
	// Receivers see whole chunks; the tail of the last one is zero padding.
	if len(got.payload) != 3*protocol.ClipboardChunkSize || !bytes.Equal(got.payload[:100], payload) {
		t.Errorf("payload = %v", got.payload)
	}
	if !bytes.Equal(got.payload[100:], make([]byte, len(got.payload)-100)) {
		t.Error("padding is not zero")
	}
}

func TestHandleClipboardTooLarge(t *testing.T) {
	clip := &fakeClipboard{}
	h := &Handler{Clipboard: clip}

	chunk := &protocol.Packet{Type: protocol.ClipboardText}
	for i := 0; i <= maxClipboardPayload/protocol.ClipboardChunkSize; i++ {
		h.HandlePacket(chunk)
	}
	h.HandlePacket(&protocol.Packet{Type: protocol.ClipboardDataEnd})
	if len(clip.applied) != 0 {
		t.Error("oversized payload was applied")
	}

	// The next transfer starts fresh
	h.HandlePacket(chunk)
	h.HandlePacket(&protocol.Packet{Type: protocol.ClipboardDataEnd})
	if len(clip.applied) != 1 {
		t.Errorf("applied %d payloads after oversized one, want 1", len(clip.applied))
	}
}

func TestHandleClipboardAnnouncements(t *testing.T) {
	clip := &fakeClipboard{}
	h := &Handler{Clipboard: clip}
	h.HandlePacket(&protocol.Packet{Type: protocol.Clipboard})
	h.HandlePacket(&protocol.Packet{Type: protocol.MachineSwitched})
	h.HandlePacket(&protocol.Packet{Type: protocol.ClipboardAsk, PostAction: 1})
	if clip.announced != 1 || clip.switched != 1 {
		t.Errorf("announced = %d, switched = %d, want 1 and 1", clip.announced, clip.switched)
	}
	if len(clip.pushes) != 1 || clip.pushes[0] != 1 {
		t.Errorf("push requests = %v, want [1]", clip.pushes)
	}
}

func TestHandleClipboardDisabled(t *testing.T) {
	h := &Handler{}
	h.HandlePacket(&protocol.Packet{Type: protocol.ClipboardText})
	h.HandlePacket(&protocol.Packet{Type: protocol.ClipboardDataEnd})
	h.HandlePacket(&protocol.Packet{Type: protocol.Clipboard})
	h.HandlePacket(&protocol.Packet{Type: protocol.MachineSwitched})
	h.HandlePacket(&protocol.Packet{Type: protocol.ClipboardAsk})
}
