package auth

import (
	"crypto/rand"
	"encoding/hex"
)

// randomToken generates a 32-byte (64 hex char) opaque token — plenty of
// entropy without needing a JWT library and its signing-key management for
// a five-table coursework project.
func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
