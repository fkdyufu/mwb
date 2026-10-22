// internal/clipboard/codec_test.go
package clipboard

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"io"
	"strings"
	"testing"
	"unicode/utf16"
)

// windowsPayload builds a clipboard text payload the way PowerToys does:
// sections joined in UTF-16LE, raw DEFLATE, then zero padding from the
// MemoryStream buffer and the last 48-byte chunk.
func windowsPayload(t *testing.T, sections string) []byte {
	t.Helper()
	units := utf16.Encode([]rune(sections))
	raw := make([]byte, len(units)*2)
	for i, u := range units {
		binary.LittleEndian.PutUint16(raw[i*2:], u)
	}
	var buf bytes.Buffer
	w, _ := flate.NewWriter(&buf, flate.DefaultCompression)
	_, _ = w.Write(raw)
	_ = w.Close()
	return append(buf.Bytes(), make([]byte, 100)...)
}

func TestDecodeTextWindowsPayload(t *testing.T) {
	sections := "TXTline 1\r\n第二行 🎉" + textTypeSep +
		"RTF{\\rtf1 line 1}" + textTypeSep +
		"HTM<b>line 1</b>" + textTypeSep
	got, err := DecodeText(windowsPayload(t, sections))
	if err != nil {
		t.Fatal(err)
	}
	if want := "line 1\n第二行 🎉"; got != want {
		t.Errorf("text = %q, want %q", got, want)
	}
}

func TestDecodeTextUnprefixed(t *testing.T) {
	got, err := DecodeText(windowsPayload(t, "plain text"))
	if err != nil {
		t.Fatal(err)
	}
	if got != "plain text" {
		t.Errorf("text = %q, want %q", got, "plain text")
	}
}

func TestDecodeTextWithoutPlainSection(t *testing.T) {
	if _, err := DecodeText(windowsPayload(t, "HTM<b>x</b>"+textTypeSep)); err == nil {
		t.Fatal("expected error for payload without a TXT section")
	}
}

func TestDecodeTextGarbage(t *testing.T) {
	if _, err := DecodeText([]byte("not deflate data at all")); err == nil {
		t.Fatal("expected error for invalid payload")
	}
}

func TestEncodeTextWireFormat(t *testing.T) {
	payload, err := EncodeText("a\nb")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(flate.NewReader(bytes.NewReader(payload)))
	if err != nil {
		t.Fatal(err)
	}
	units := make([]uint16, len(raw)/2)
	for i := range units {
		units[i] = binary.LittleEndian.Uint16(raw[i*2:])
	}
	if got, want := string(utf16.Decode(units)), "TXTa\r\nb"+textTypeSep; got != want {
		t.Errorf("decoded = %q, want %q", got, want)
	}
}

func TestEncodeDecodeTextRoundTrip(t *testing.T) {
	for _, text := range []string{"", "hello", "a\nb\r\nc", "中文 emoji 🎉\n", strings.Repeat("long line\n", 10000)} {
		payload, err := EncodeText(text)
		if err != nil {
			t.Fatal(err)
		}
		// Receivers see the payload zero-padded to whole chunks.
		payload = append(payload, make([]byte, 47)...)
		got, err := DecodeText(payload)
		if text == "" {
			if err == nil && got != "" {
				t.Errorf("empty text decoded as %q", got)
			}
			continue
		}
		if err != nil {
			t.Fatalf("DecodeText(%q): %v", text, err)
		}
		if want := strings.ReplaceAll(text, "\r\n", "\n"); got != want {
			t.Errorf("round trip = %q, want %q", got, want)
		}
	}
}

func testPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 3))
	img.Set(1, 1, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestTrimPNG(t *testing.T) {
	want := testPNG(t)
	got, err := TrimPNG(append(append([]byte{}, want...), make([]byte, 40)...))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("trimmed %d bytes, want %d", len(got), len(want))
	}
}

func TestTrimPNGInvalid(t *testing.T) {
	img := testPNG(t)
	for name, data := range map[string][]byte{
		"not png":   []byte("GIF89a......"),
		"truncated": img[:len(img)-8],
		"empty":     nil,
	} {
		if _, err := TrimPNG(data); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}
