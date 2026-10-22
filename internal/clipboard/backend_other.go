// internal/clipboard/backend_other.go

//go:build !linux

package clipboard

import "errors"

// NewBackend reports that clipboard sync is not implemented on this platform.
func NewBackend() (Backend, error) {
	return nil, errors.New("clipboard sync is not supported on this platform yet")
}
