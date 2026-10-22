// internal/network/handler.go
package network

import (
	"log/slog"

	"github.com/bketelsen/mwb/internal/clipboard"
	"github.com/bketelsen/mwb/internal/input"
	"github.com/bketelsen/mwb/internal/protocol"
)

// MouseDevice is the interface for mouse injection.
type MouseDevice interface {
	MoveTo(x, y int32) error
	ButtonDown(button uint16) error
	ButtonUp(button uint16) error
	Wheel(delta int32) error
}

// KeyboardDevice is the interface for keyboard injection.
type KeyboardDevice interface {
	KeyDown(code uint16) error
	KeyUp(code uint16) error
}

// ClipboardSink receives the peer's clipboard contents.
type ClipboardSink interface {
	ApplyRemote(image bool, payload []byte)
	// RemoteHasData records that the peer copied contents it only sends on
	// request: files and contents over clipboard.MaxPayload.
	RemoteHasData()
	// MachineSwitched reports that the peer moved the mouse to this machine.
	MachineSwitched()
	// PushRequested reports that the peer asks for the data announced with
	// a Clipboard packet to be pushed to it, with the request's PostAction.
	PushRequested(postAction uint32)
}

// maxClipboardPayload bounds a received clipboard payload: MWB sends at most
// clipboard.MaxPayload bytes, padded to a whole chunk.
const maxClipboardPayload = clipboard.MaxPayload + protocol.ClipboardChunkSize

// Handler processes incoming MWB packets and injects input events.
type Handler struct {
	Mouse    MouseDevice
	Keyboard KeyboardDevice
	// Clipboard receives the peer's clipboard; nil disables clipboard sync.
	Clipboard ClipboardSink

	clipboardRx clipboardTransfer
	// pressed holds the keys pressed on this machine and not released yet.
	pressed map[uint16]struct{}
}

// clipboardTransfer collects the chunks of a clipboard payload until
// ClipboardDataEnd arrives. Other packets may arrive in between.
type clipboardTransfer struct {
	active   bool
	image    bool
	overflow bool
	data     []byte
}

// HandlePacket dispatches a packet to the appropriate handler.
func (h *Handler) HandlePacket(pkt *protocol.Packet) {
	switch pkt.Type {
	case protocol.Mouse:
		h.handleMouse(pkt)
	case protocol.Keyboard:
		h.handleKeyboard(pkt)
	case protocol.ClipboardText, protocol.ClipboardImage:
		h.handleClipboardChunk(pkt)
	case protocol.ClipboardDataEnd:
		h.finishClipboard()
	case protocol.Clipboard:
		// Sent instead of the data for files and contents over MaxPayload,
		// which are fetched over the clipboard connection on MachineSwitched.
		if h.Clipboard != nil {
			h.Clipboard.RemoteHasData()
		}
	case protocol.MachineSwitched:
		if h.Clipboard != nil {
			h.Clipboard.MachineSwitched()
		}
	case protocol.ClipboardAsk:
		// Sent instead of fetching announced data when the peer cannot
		// connect to this machine, which does not listen for connections.
		if h.Clipboard != nil {
			h.Clipboard.PushRequested(pkt.PostAction)
		}
	case protocol.HideMouse:
		// Sent when the mouse leaves this machine. Keys released after that
		// are released on the next machine, so release them here, as MWB does.
		h.releaseKeys()
	case protocol.Hello, protocol.Awake, protocol.HandshakeAck:
		// expected control packets — ignore silently
	default:
		slog.Debug("unhandled packet type", "type", pkt.Type)
	}
}

func (h *Handler) handleMouse(pkt *protocol.Packet) {
	md := pkt.Mouse
	var err error
	switch int(md.DwFlags) {
	case protocol.WM_MOUSEMOVE:
		err = h.Mouse.MoveTo(md.X, md.Y)
	case protocol.WM_LBUTTONDOWN:
		if err = h.Mouse.MoveTo(md.X, md.Y); err == nil {
			err = h.Mouse.ButtonDown(input.BTN_LEFT)
		}
	case protocol.WM_LBUTTONUP:
		if err = h.Mouse.MoveTo(md.X, md.Y); err == nil {
			err = h.Mouse.ButtonUp(input.BTN_LEFT)
		}
	case protocol.WM_RBUTTONDOWN:
		if err = h.Mouse.MoveTo(md.X, md.Y); err == nil {
			err = h.Mouse.ButtonDown(input.BTN_RIGHT)
		}
	case protocol.WM_RBUTTONUP:
		if err = h.Mouse.MoveTo(md.X, md.Y); err == nil {
			err = h.Mouse.ButtonUp(input.BTN_RIGHT)
		}
	case protocol.WM_MBUTTONDOWN:
		if err = h.Mouse.MoveTo(md.X, md.Y); err == nil {
			err = h.Mouse.ButtonDown(input.BTN_MIDDLE)
		}
	case protocol.WM_MBUTTONUP:
		if err = h.Mouse.MoveTo(md.X, md.Y); err == nil {
			err = h.Mouse.ButtonUp(input.BTN_MIDDLE)
		}
	case protocol.WM_MOUSEWHEEL:
		delta := md.WheelDelta / 120
		if delta == 0 && md.WheelDelta > 0 {
			delta = 1
		} else if delta == 0 && md.WheelDelta < 0 {
			delta = -1
		}
		err = h.Mouse.Wheel(delta)
	default:
		slog.Debug("unhandled mouse event", "flags", md.DwFlags)
		return
	}
	if err != nil {
		slog.Error("mouse input error", "err", err)
	}
}

// numpadVKs maps the virtual keys Windows reports for number pad keys while
// its NumLock is off, which unlike the dedicated keys lack LLKHF_EXTENDED, to
// the number pad keys. NumLock presses only reach this machine, so its NumLock
// decides what they type, as with a local keyboard.
var numpadVKs = map[int32]int32{
	0x2D: 0x60, // VK_INSERT: VK_NUMPAD0
	0x23: 0x61, // VK_END: VK_NUMPAD1
	0x28: 0x62, // VK_DOWN: VK_NUMPAD2
	0x22: 0x63, // VK_NEXT: VK_NUMPAD3
	0x25: 0x64, // VK_LEFT: VK_NUMPAD4
	0x0C: 0x65, // VK_CLEAR: VK_NUMPAD5
	0x27: 0x66, // VK_RIGHT: VK_NUMPAD6
	0x24: 0x67, // VK_HOME: VK_NUMPAD7
	0x26: 0x68, // VK_UP: VK_NUMPAD8
	0x21: 0x69, // VK_PRIOR: VK_NUMPAD9
	0x2E: 0x6E, // VK_DELETE: VK_DECIMAL
}

func (h *Handler) handleKeyboard(pkt *protocol.Packet) {
	kd := pkt.Keyboard
	vk := kd.WVk
	// Key remappers inject navigation keys without LLKHF_EXTENDED too.
	if kd.DwFlags&(protocol.LLKHF_EXTENDED|protocol.LLKHF_INJECTED) == 0 {
		if numpad, ok := numpadVKs[vk]; ok {
			vk = numpad
		}
	}
	keyCode, ok := input.VKToKeyCode(vk)
	if !ok {
		slog.Debug("unknown VK code", "vk", vk)
		return
	}
	var err error
	isUp := (kd.DwFlags & protocol.LLKHF_UP) != 0
	if isUp {
		err = h.Keyboard.KeyUp(keyCode)
		delete(h.pressed, keyCode)
	} else if err = h.Keyboard.KeyDown(keyCode); err == nil {
		if h.pressed == nil {
			h.pressed = make(map[uint16]struct{})
		}
		h.pressed[keyCode] = struct{}{}
	}
	if err != nil {
		slog.Error("keyboard input error", "err", err)
	}
}

// releaseKeys releases the keys still pressed, which would otherwise stay
// pressed on this machine, e.g. Ctrl turning the next scroll into a zoom.
func (h *Handler) releaseKeys() {
	for code := range h.pressed {
		slog.Debug("releasing key left pressed", "code", code)
		if err := h.Keyboard.KeyUp(code); err != nil {
			slog.Error("keyboard input error", "err", err)
		}
	}
	clear(h.pressed)
}

func (h *Handler) handleClipboardChunk(pkt *protocol.Packet) {
	if h.Clipboard == nil {
		return
	}
	rx := &h.clipboardRx
	image := pkt.Type == protocol.ClipboardImage
	if !rx.active || rx.image != image {
		*rx = clipboardTransfer{active: true, image: image}
	}
	if len(rx.data)+protocol.ClipboardChunkSize > maxClipboardPayload {
		rx.overflow = true
		return
	}
	rx.data = append(rx.data, pkt.ClipboardData[:]...)
}

func (h *Handler) finishClipboard() {
	rx := h.clipboardRx
	h.resetClipboard()
	if !rx.active || h.Clipboard == nil {
		return
	}
	if rx.overflow {
		slog.Info("remote clipboard too large to sync", "max", clipboard.MaxPayload)
		return
	}
	h.Clipboard.ApplyRemote(rx.image, rx.data)
}

// resetClipboard drops a partially received clipboard payload.
func (h *Handler) resetClipboard() {
	h.clipboardRx = clipboardTransfer{}
}
