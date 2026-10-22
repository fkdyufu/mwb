// internal/clipboard/codec.go

// Package clipboard syncs the local clipboard with the MWB peer and converts
// clipboard contents to and from the MWB wire format.
package clipboard

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/bketelsen/mwb/internal/protocol"
)

// MIME types of the clipboard contents mwb syncs.
const (
	MIMEText = "text/plain;charset=utf-8"
	MIMEPNG  = "image/png"
)

// MaxPayload is the largest payload MWB sends over the message connection.
// Bigger contents use a separate clipboard connection, which mwb only fetches
// from.
const MaxPayload = 1024 * 1024

// maxTextSize bounds decompressed text, as MWB limits text to 20M characters.
const maxTextSize = 40 * 1024 * 1024

// textTypeSep separates the TXT, RTF and HTM sections of MWB clipboard text.
const textTypeSep = "{4CFF57F7-BEDD-43d5-AE8F-27A61E886F2F}"

// Content is a single clipboard entry.
type Content struct {
	MIME string
	Data []byte
}

// EncodeText converts text to an MWB clipboard text payload: a TXT section
// encoded as UTF-16LE and compressed with raw DEFLATE. Line endings become CRLF.
func EncodeText(text string) ([]byte, error) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\n", "\r\n")

	raw := protocol.EncodeUTF16LE("TXT" + text + textTypeSep)

	var buf bytes.Buffer
	w, err := flate.NewWriter(&buf, flate.DefaultCompression)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(raw); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// DecodeText extracts the plain text section from an MWB clipboard text
// payload. Line endings become LF.
func DecodeText(payload []byte) (string, error) {
	r := flate.NewReader(bytes.NewReader(payload))
	defer func() { _ = r.Close() }()

	raw, err := io.ReadAll(io.LimitReader(r, maxTextSize+1))
	if err != nil {
		return "", fmt.Errorf("decompress: %w", err)
	}
	if len(raw) > maxTextSize {
		return "", fmt.Errorf("text larger than %d bytes", maxTextSize)
	}

	sections := strings.Split(protocol.DecodeUTF16LE(raw), textTypeSep)

	for i, section := range sections {
		section = strings.TrimRight(section, "\x00")
		if section == "" {
			continue
		}
		var text string
		switch {
		case hasPrefixFold(section, "TXT"):
			text = section[3:]
		case hasPrefixFold(section, "RTF"), hasPrefixFold(section, "HTM"):
			continue
		case i == 0:
			// Payloads without section prefixes are plain text.
			text = section
		default:
			continue
		}
		return strings.ReplaceAll(text, "\r\n", "\n"), nil
	}
	return "", errors.New("no plain text section")
}

func hasPrefixFold(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}

var pngSignature = []byte("\x89PNG\r\n\x1a\n")

// TrimPNG returns the PNG image at the start of data, dropping the zero
// padding that follows it in MWB clipboard image payloads.
func TrimPNG(data []byte) ([]byte, error) {
	if !bytes.HasPrefix(data, pngSignature) {
		return nil, errors.New("not a PNG image")
	}
	// Each chunk is a 4-byte length, a 4-byte type, the data and a 4-byte CRC.
	for off := len(pngSignature); off+12 <= len(data); {
		end := off + 12 + int(binary.BigEndian.Uint32(data[off:]))
		if end < off || end > len(data) {
			break
		}
		if string(data[off+4:off+8]) == "IEND" {
			return data[:end], nil
		}
		off = end
	}
	return nil, errors.New("truncated PNG image")
}
