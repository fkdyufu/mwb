// internal/clipboard/backend_linux.go

//go:build linux

package clipboard

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// textTypes are the clipboard text MIME types read, in order of preference.
var textTypes = []string{"text/plain;charset=utf-8", "UTF8_STRING", "text/plain", "TEXT", "STRING"}

// passwordHint marks clipboard contents copied from a password manager.
const passwordHint = "x-kde-passwordManagerHint"

// copiedFilesType marks files copied in a file manager, which also offers them
// as text/uri-list. Their paths are useless to the peer as text.
const copiedFilesType = "x-special/gnome-copied-files"

// maxReadSize bounds how much clipboard data is read; anything bigger cannot
// fit in an MWB payload anyway.
const maxReadSize = 64 * 1024 * 1024

// NewBackend returns the Wayland clipboard backend, which uses the
// wl-clipboard tools (wl-copy and wl-paste).
func NewBackend() (Backend, error) {
	for _, tool := range []string{"wl-copy", "wl-paste"} {
		if _, err := exec.LookPath(tool); err != nil {
			return nil, fmt.Errorf("%s not found, install wl-clipboard: %w", tool, err)
		}
	}
	return wlClipboard{}, nil
}

type wlClipboard struct{}

func (wlClipboard) Set(c Content) error {
	cmd := exec.Command("wl-copy", "--type", c.MIME)
	cmd.Env = waylandEnv()
	cmd.Stdin = bytes.NewReader(c.Data)
	// wl-copy forks a child that serves the clipboard until it is replaced.
	// Pass stderr through as a file: a pipe would keep Run waiting for the child.
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("wl-copy: %w", err)
	}
	return nil
}

func (b wlClipboard) Watch(ctx context.Context, onChange func(Content)) error {
	// wl-paste runs the command on every change with CLIPBOARD_STATE set to
	// "data", "nil", "clear" or "sensitive" (password manager contents).
	cmd := exec.CommandContext(ctx, "wl-paste", "--watch", "sh", "-c", `echo "${CLIPBOARD_STATE:-data}"`)
	cmd.Env = waylandEnv()
	cmd.Stderr = os.Stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("wl-paste: %w", err)
	}

	scanner := bufio.NewScanner(out)
	for scanner.Scan() {
		if scanner.Text() != "data" {
			onChange(Content{})
			continue
		}
		c, err := b.read(ctx)
		if err != nil {
			slog.Warn("read clipboard", "err", err)
		}
		onChange(c)
	}
	return fmt.Errorf("wl-paste: %w", cmd.Wait())
}

// read returns the current clipboard contents, preferring copied files, then
// text over images as MWB does, or empty Content when nothing can be synced.
func (b wlClipboard) read(ctx context.Context) (Content, error) {
	listCmd := exec.CommandContext(ctx, "wl-paste", "--list-types")
	listCmd.Env = waylandEnv()
	list, err := listCmd.Output()
	if err != nil {
		return Content{}, fmt.Errorf("wl-paste --list-types: %w", err)
	}
	types := strings.Fields(string(list))
	if slices.Contains(types, passwordHint) {
		return Content{}, nil
	}
	if slices.Contains(types, MIMEURIList) {
		data, err := b.paste(ctx, MIMEURIList)
		if err != nil {
			return Content{}, err
		}
		// Links copied in other programs are synced as text.
		if len(filePaths(data)) > 0 {
			return Content{MIME: MIMEURIList, Data: data}, nil
		}
	}
	if slices.Contains(types, copiedFilesType) {
		return Content{}, nil
	}

	mime, offered := MIMEText, ""
	for _, t := range textTypes {
		if slices.Contains(types, t) {
			offered = t
			break
		}
	}
	if offered == "" && slices.Contains(types, MIMEPNG) {
		mime, offered = MIMEPNG, MIMEPNG
	}
	if offered == "" {
		return Content{}, nil
	}

	data, err := b.paste(ctx, offered)
	if err != nil || len(data) == 0 {
		return Content{}, err
	}
	return Content{MIME: mime, Data: data}, nil
}

// paste returns the clipboard contents offered as mime, or nil when they are
// too large to sync.
func (wlClipboard) paste(ctx context.Context, mime string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "wl-paste", "--no-newline", "--type", mime)
	cmd.Env = waylandEnv()
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("wl-paste --type %s: %w", mime, err)
	}
	data, readErr := io.ReadAll(io.LimitReader(out, maxReadSize+1))
	if len(data) > maxReadSize {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		slog.Info("clipboard too large to sync", "mime", mime, "max", maxReadSize)
		return nil, nil
	}
	if err := cmd.Wait(); err != nil {
		return nil, fmt.Errorf("wl-paste --type %s: %w", mime, err)
	}
	return data, readErr
}

// waylandEnv returns the environment for wl-clipboard commands. A systemd user
// service started before the compositor has no WAYLAND_DISPLAY, so fall back
// to the first Wayland socket in XDG_RUNTIME_DIR.
func waylandEnv() []string {
	env := os.Environ()
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" {
		return env
	}

	isSocket := func(path string) bool {
		fi, err := os.Stat(path)
		return err == nil && fi.Mode()&os.ModeSocket != 0
	}
	if display := os.Getenv("WAYLAND_DISPLAY"); display != "" {
		if !filepath.IsAbs(display) {
			display = filepath.Join(runtimeDir, display)
		}
		if isSocket(display) {
			return env
		}
	}

	sockets, _ := filepath.Glob(filepath.Join(runtimeDir, "wayland-*"))
	for _, s := range sockets {
		if !strings.HasSuffix(s, ".lock") && isSocket(s) {
			return append(env, "WAYLAND_DISPLAY="+filepath.Base(s))
		}
	}
	return env
}
