# mwb

A Linux and macOS client for [Microsoft PowerToys Mouse Without Borders](https://learn.microsoft.com/en-us/windows/powertoys/mouse-without-borders). Share your Windows keyboard and mouse with a Linux or macOS machine over the network.

## Prerequisites

- Go 1.25+
- A Windows machine running PowerToys Mouse Without Borders
- **Linux:** uinput support, Wayland display, [wl-clipboard](https://github.com/bugaevc/wl-clipboard) for clipboard sync
- **macOS:** Accessibility permission granted to the mwb binary

## Linux Setup

Before running mwb on Linux, you need to configure uinput access:

```bash
# Load the uinput kernel module
sudo modprobe uinput

# Load uinput automatically on boot
echo 'uinput' | sudo tee /etc/modules-load.d/uinput.conf

# Allow the input group to access /dev/uinput
echo 'KERNEL=="uinput", GROUP="input", MODE="0660"' | sudo tee /etc/udev/rules.d/99-uinput.rules
sudo udevadm control --reload-rules && sudo udevadm trigger /dev/uinput

# Add your user to the input group (log out and back in after this)
sudo usermod -aG input $USER
```

## macOS Setup

mwb needs Accessibility permission to inject mouse and keyboard events:

1. Build or install the `mwb` binary
2. Run it once — macOS will prompt for Accessibility access
3. Grant permission in **System Settings > Privacy & Security > Accessibility**

If running from Terminal, you may need to grant the permission to Terminal.app instead.

## Installation

### Homebrew (Linux and macOS) - Experimental

```bash
brew tap bketelsen/tap
brew install mwb
```

To start `mwb` automatically on login:

```bash
brew services start mwb
```

On macOS you must grant Accessibility permissions after installing:

> System Settings → Privacy & Security → Accessibility → Add `mwb`

### Linux (deb/rpm)

Download the latest `.deb` or `.rpm` from the [releases page](https://github.com/bketelsen/mwb/releases).

### Linux (from source)

```bash
make install
```

This builds the binary, installs it to `~/go/bin/mwb`, and sets up a systemd user service.

### macOS (manual)

```bash
make build
cp mwb /usr/local/bin/mwb
```

## Configuration

Create `~/.config/mwb/config.toml`:

```toml
host = "192.168.1.100"   # IP of the Windows machine
key = "YourSecurityKey"   # Must match the key set in PowerToys MWB
name = "linux"            # Name shown in the MWB device layout (max 15 chars)
# port = 15100            # Base port (default: 15100, message port = base + 1)
# clipboard = false       # Disable clipboard sync (default: true)
# clipboard_max_file_mb = 20  # Largest file or big content transferred, both directions (default: 20, 0 disables)
```

The security key is found in PowerToys > Mouse Without Borders > Security key.

## Clipboard Sync

On Linux, mwb syncs the clipboard in both directions: copy on one machine, paste on the other. It uses `wl-copy` and `wl-paste` from wl-clipboard (`sudo pacman -S wl-clipboard`, `sudo apt install wl-clipboard`), so it needs a Wayland compositor that supports the wlr-data-control or ext-data-control protocol, which wl-paste uses to watch for changes.

- Text and PNG images are synced. When both are available, text wins, as in MWB.
- Files copied in a file manager can be pasted on the other machine, as in MWB: within 30 seconds of the copy, move the mouse to the other screen, and the file is transferred and put in its clipboard.
  - Files from Windows are saved in `~/.cache/mwb/clipboard`, which keeps only the latest one.
  - Files from Linux are saved by MWB in a `ScreenCaptures` folder on the Windows desktop, where it deletes files older than a day.
- Only one file is transferred per copy (the first one when several are copied), and folders are not supported. Files over `clipboard_max_file_mb` (20 MB by default) are skipped, and MWB itself refuses to send files over 100 MB.
- Text and images over 1 MB after compression are transferred the same way.
- Contents marked as secret by password managers are not sent.
- "Share clipboard" must be enabled in PowerToys Mouse Without Borders, and "Transfer file" for files. Both are on by default.

When a copy on Windows does not reach Linux, check whether another clipboard manager is running on Windows. MWB ignores clipboard changes less than a second apart, so when a manager accesses the clipboard right after a screenshot tool, MWB can miss the image.

## Usage

### Linux: systemd service (recommended)

```bash
systemctl --user enable --now mwb
```

View logs:

```bash
journalctl --user -u mwb -f
```

To uninstall:

```bash
make uninstall
```

### macOS: LaunchAgent (recommended)

```bash
cp packaging/com.mwb.agent.plist ~/Library/LaunchAgents/
launchctl load ~/Library/LaunchAgents/com.mwb.agent.plist
```

This starts mwb at login and restarts it on failure. View logs:

```bash
tail -f /tmp/mwb.log
tail -f /tmp/mwb.err.log
```

To stop and unload:

```bash
launchctl unload ~/Library/LaunchAgents/com.mwb.agent.plist
```

### Manual

```bash
mwb                          # uses default config path
mwb -config /path/to/config  # custom config path
mwb -debug                   # enable debug logging
```

On the Windows side, open PowerToys Mouse Without Borders and click "Refresh connections" to discover the Linux client. Then drag it into your desired position in the device layout.

## How It Works

mwb connects to the Windows MWB server over TCP, performs an AES-256-CBC encrypted handshake, and receives mouse/keyboard events which it injects locally via Linux uinput virtual devices or macOS CoreGraphics events. Clipboard contents travel over the same connection in both directions; files and bigger contents are fetched over the separate MWB clipboard connection.
