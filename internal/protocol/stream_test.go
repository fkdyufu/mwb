// internal/protocol/stream_test.go
package protocol

import (
	"bytes"
	"io"
	"testing"
)

func TestEncryptDecryptRoundTrip(t *testing.T) {
	key := DeriveKey("TestSecurityKey!!")
	iv := FixedIV()

	plaintext := []byte("Hello, Mouse Without Borders!!!!") // exactly 32 bytes
	if len(plaintext) != 32 {
		t.Fatalf("plaintext len = %d, want 32", len(plaintext))
	}

	// Encrypt
	var cipherBuf bytes.Buffer
	enc, err := NewEncryptWriter(&cipherBuf, key, iv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := enc.Write(plaintext); err != nil {
		t.Fatal(err)
	}

	// Ciphertext should not equal plaintext
	cipher := cipherBuf.Bytes()
	if bytes.Equal(cipher, plaintext) {
		t.Error("ciphertext should differ from plaintext")
	}

	// Decrypt
	dec, err := NewDecryptReader(bytes.NewReader(cipher), key, iv)
	if err != nil {
		t.Fatal(err)
	}
	result := make([]byte, len(plaintext))
	if _, err := io.ReadFull(dec, result); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(result, plaintext) {
		t.Errorf("decrypted = %q, want %q", result, plaintext)
	}
}

func TestEncryptedStreamMultipleBlocks(t *testing.T) {
	key := DeriveKey("AnotherTestKey123")
	iv := FixedIV()

	// Write two 32-byte packets
	pkt1 := bytes.Repeat([]byte{0xAA}, 32)
	pkt2 := bytes.Repeat([]byte{0xBB}, 32)

	var cipherBuf bytes.Buffer
	enc, err := NewEncryptWriter(&cipherBuf, key, iv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = enc.Write(pkt1); err != nil {
		t.Fatal(err)
	}
	if _, err = enc.Write(pkt2); err != nil {
		t.Fatal(err)
	}

	dec, err := NewDecryptReader(bytes.NewReader(cipherBuf.Bytes()), key, iv)
	if err != nil {
		t.Fatal(err)
	}

	got1 := make([]byte, 32)
	if _, err = io.ReadFull(dec, got1); err != nil {
		t.Fatal(err)
	}
	got2 := make([]byte, 32)
	if _, err = io.ReadFull(dec, got2); err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(got1, pkt1) {
		t.Error("packet 1 mismatch")
	}
	if !bytes.Equal(got2, pkt2) {
		t.Error("packet 2 mismatch")
	}
}

func TestDecryptReaderKeepsPartialBlocks(t *testing.T) {
	key := DeriveKey("TestSecurityKey!!")
	iv := FixedIV()
	plain := make([]byte, 100)
	for i := range plain {
		plain[i] = byte(i)
	}
	padded := append(append([]byte{}, plain...), make([]byte, 12)...)

	var buf bytes.Buffer
	enc, err := NewEncryptWriter(&buf, key, iv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := enc.Write(padded); err != nil {
		t.Fatal(err)
	}

	dec, err := NewDecryptReader(&buf, key, iv)
	if err != nil {
		t.Fatal(err)
	}
	// Read in sizes that do not line up with the 16-byte blocks
	var got []byte
	for _, n := range []int{3, 20, 7, 40, 30} {
		p := make([]byte, n)
		m, err := io.ReadFull(dec, p)
		if err != nil {
			t.Fatalf("read %d: %v", n, err)
		}
		got = append(got, p[:m]...)
	}
	if !bytes.Equal(got, plain) {
		t.Errorf("got %v, want %v", got, plain)
	}
}
