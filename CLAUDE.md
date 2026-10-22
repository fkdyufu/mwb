# mwb - Mouse Without Borders Client (Linux & macOS)

## Project Structure

```
cmd/mwb/           - CLI entry point
internal/
  clipboard/        - clipboard sync: MWB payload codec, echo suppression,
                      wl-clipboard backend (Linux only)
  config/           - TOML config loading (~/.config/mwb/config.toml)
  input/            - platform-specific virtual mouse/keyboard devices
                      uinput.go (Linux), coregraphics.go (macOS)
  network/          - TCP connection, handshake, packet receive loop
  protocol/         - Packet types, binary serialization, AES-256-CBC encryption
docs/plans/         - Implementation plan
```

## Build & Test

```bash
make build   # builds ./mwb
make test    # runs go test ./...
make fmt     # format all Go files
make lint    # run golangci-lint
make check   # fmt + lint + test (run before committing)
```

## Before Committing

Always run `make check` before committing any changes. All code must pass formatting, linting, and tests.

## Protocol Notes

- Connects to Windows PowerToys MWB on port 15101 (base 15100 + 1 for messages)
- AES-256-CBC encryption with PBKDF2-SHA512 key derivation. Three stream schemes exist across PowerToys versions (`protocol.Scheme`):
  - legacy: fixed salt/IV (`"18446744073709551615"`), 50k iterations
  - header-50k (PowerToys #48742, 2026-06): each stream starts with a cleartext 32-byte header (16-byte random salt + 16-byte random IV), 50k iterations
  - header-100k (PowerToys #49600, 2026-07): same header, 100k iterations
- The server sends its preamble and 10 Handshakes without waiting for the client (after ~5s spent resolving the client's name), so `Connect` reads the first `protocol.ProbeSize` bytes, detects the scheme by decrypting the first packet, then replies with the same scheme
- Handshake: send 10 Handshake packets (type 126), respond to peer's with HandshakeAck (type 127)
- **Critical**: HandshakeAck must have `Src = MachineID` (not 0), otherwise server stores ID.NONE and never routes packets to the socket
- **Critical**: All packet IDs must be non-zero and unique — server has a zero-initialized dedup ring buffer that silently drops ID=0 packets
- After handshake, must send HeartbeatEx (type 51) with `Des=255` (ID.ALL) to trigger `AddToMachinePool()` on the server
- Must respond to Hi (type 2) with Hello (type 3) for device discovery
- Heartbeat responses must include the local machine name (not the remote's)
- Clipboard (≤1 MB payloads) travels over the message connection: ClipboardText (124) / ClipboardImage (125) big packets each carry 48 payload bytes at offsets 16-63, ending with ClipboardDataEnd (76). Other packets may be interleaved. Text payload = raw DEFLATE of UTF-16LE `"TXT"+text+SEP` (optionally also `RTF`/`HTM` sections), SEP = `{4CFF57F7-BEDD-43d5-AE8F-27A61E886F2F}`; images are raw PNG. Receivers see zero padding after the data
- The server only sends clipboard data to machines in its layout (`InMachineMatrix`). Files and bigger payloads send a Clipboard (69) beat instead; when the server later sends MachineSwitched (77) within 30s, mwb fetches them from the clipboard port (base port, 15100) in `Conn.FetchClipboard`:
  - Both sides send their scheme preamble, a 16-byte random block and an unstamped 64-byte packet (Type Clipboard 69 or ClipboardPush 79, Src, MachineName, PostAction at offset 16). The server only accepts it when `ResolveID(MachineName) == Src` and it is connected to Src
  - The server then sends a 1024-byte UTF-16LE header `"<size>*<name>"` (name is `text`, `image` or a file path; size 0 means nothing follows and the name says why, e.g. folders or files over 100 MB), then the data padded with zeros, and closes the connection
  - Received files go to `~/.cache/mwb/clipboard`, which is emptied first, and are put in the clipboard as `text/uri-list`
- Sending works the other way round: mwb announces local files (`text/uri-list`) and payloads over 1 MB with a Clipboard (69) beat. When the mouse moves to the server within 30s, it cannot connect to mwb, which does not listen, so it sends ClipboardAsk (78, PostAction at offset 16) instead. mwb then connects with ClipboardPush (79) and the request's PostAction (`Conn.PushClipboard`), sends the header and the data padded to whole blocks, and closes the connection: the server reads until EOF and keeps `<size>` bytes
  - The server only keeps the file name, and takes names starting with `text` or `image` for those transfers, so mwb sends files as `/<name>` with characters Windows rejects replaced
- Keys go out as Windows virtual keys with the low-level hook flags (`LLKHF_*`). The server suppresses keys locally while controlling another machine, so NumLock only toggles there; with the server's NumLock off, number pad keys arrive as VK_END, VK_DOWN, ... without LLKHF_EXTENDED, which mwb maps back to number pad keys so the local NumLock decides
- The server sends HideMouse (50) to the machine the mouse leaves, which releases its pressed modifiers; mwb releases the keys it pressed, which would otherwise stay down (e.g. Ctrl turning scrolling into zoom)

## System Requirements

### Linux

/dev/uinput must be accessible. Requires:
1. `uinput` kernel module loaded (`modprobe uinput`)
2. udev rule: `KERNEL=="uinput", GROUP="input", MODE="0660"` in `/etc/udev/rules.d/99-uinput.rules`
3. User in `input` group

### macOS

Accessibility permission must be granted:
1. Open System Settings > Privacy & Security > Accessibility
2. Add and enable the `mwb` binary (or Terminal.app if running from terminal)
3. Build requires CGo: `CGO_ENABLED=1 go build ./cmd/mwb`
