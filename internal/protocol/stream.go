// internal/protocol/stream.go
package protocol

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"io"
)

// EncryptWriter wraps an io.Writer with AES-256-CBC encryption.
type EncryptWriter struct {
	w    io.Writer
	mode cipher.BlockMode
}

// NewEncryptWriter creates an AES-256-CBC encrypting writer.
func NewEncryptWriter(w io.Writer, key, iv []byte) (*EncryptWriter, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes.NewCipher: %w", err)
	}
	mode := cipher.NewCBCEncrypter(block, iv)
	return &EncryptWriter{w: w, mode: mode}, nil
}

// NewSchemeEncryptWriter writes the cleartext salt+IV header to w when the
// scheme uses one, and returns a writer encrypting with the matching key.
func NewSchemeEncryptWriter(w io.Writer, securityKey string, s Scheme) (*EncryptWriter, error) {
	if !s.HasHeader() {
		return NewEncryptWriter(w, DeriveKey(securityKey), FixedIV())
	}
	header := make([]byte, HeaderSize)
	if _, err := rand.Read(header); err != nil {
		return nil, fmt.Errorf("rand: %w", err)
	}
	if _, err := w.Write(header); err != nil {
		return nil, fmt.Errorf("send encryption header: %w", err)
	}
	return NewEncryptWriter(w, DeriveSchemeKey(securityKey, header[:saltSize], s), header[saltSize:])
}

// Write encrypts data and writes to the underlying writer.
// Data length must be a multiple of 16 (AES block size).
func (e *EncryptWriter) Write(p []byte) (int, error) {
	if len(p)%aesBlockSize != 0 {
		return 0, fmt.Errorf("data length %d not multiple of %d", len(p), aesBlockSize)
	}
	ct := make([]byte, len(p))
	e.mode.CryptBlocks(ct, p)
	return e.w.Write(ct)
}

// DecryptReader wraps an io.Reader with AES-256-CBC decryption.
type DecryptReader struct {
	r    io.Reader
	mode cipher.BlockMode
	// pending holds decrypted bytes not yet returned by Read.
	pending []byte
}

// NewDecryptReader creates an AES-256-CBC decrypting reader.
func NewDecryptReader(r io.Reader, key, iv []byte) (*DecryptReader, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes.NewCipher: %w", err)
	}
	mode := cipher.NewCBCDecrypter(block, iv)
	return &DecryptReader{r: r, mode: mode}, nil
}

// Read reads encrypted data, decrypts it, and returns plaintext.
// It reads whole 16-byte blocks from the underlying reader, keeping any
// plaintext that does not fit in p for the next call.
func (d *DecryptReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if len(d.pending) == 0 {
		ct := make([]byte, max(len(p)/aesBlockSize, 1)*aesBlockSize)
		if _, err := io.ReadFull(d.r, ct); err != nil {
			return 0, err
		}
		d.mode.CryptBlocks(ct, ct)
		d.pending = ct
	}
	n := copy(p, d.pending)
	d.pending = d.pending[n:]
	return n, nil
}
