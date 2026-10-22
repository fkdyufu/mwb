// internal/protocol/utf16.go
package protocol

import (
	"encoding/binary"
	"unicode/utf16"
)

// EncodeUTF16LE encodes s as UTF-16LE, the string encoding .NET uses on the wire.
func EncodeUTF16LE(s string) []byte {
	units := utf16.Encode([]rune(s))
	b := make([]byte, len(units)*2)
	for i, u := range units {
		binary.LittleEndian.PutUint16(b[i*2:], u)
	}
	return b
}

// DecodeUTF16LE decodes UTF-16LE bytes, ignoring a trailing odd byte.
func DecodeUTF16LE(b []byte) string {
	units := make([]uint16, len(b)/2)
	for i := range units {
		units[i] = binary.LittleEndian.Uint16(b[i*2:])
	}
	return string(utf16.Decode(units))
}
