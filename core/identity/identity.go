// Package identity manages a device's long-term keypair and the identifiers
// derived from it. Each device has one ed25519 key. The device id is a hash of
// the public key, so an id can never be claimed by a device that does not hold
// the matching private key.
package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"fmt"
	"strings"
)

// ErrBadKey is returned when key material is malformed or of the wrong type.
var ErrBadKey = errors.New("identity: invalid key")

// idBytes is how many bytes of the public key hash make up a device id.
const idBytes = 16

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// DeviceID identifies a device. It is the lowercase base32 encoding of the
// first 16 bytes of the SHA-256 hash of the device's public key (26 chars).
type DeviceID string

// IDFromPublicKey derives the device id for a public key.
func IDFromPublicKey(pub ed25519.PublicKey) DeviceID {
	sum := sha256.Sum256(pub)
	return DeviceID(strings.ToLower(b32.EncodeToString(sum[:idBytes])))
}

// Short returns the first 8 characters of the id, for display.
func (d DeviceID) Short() string {
	if len(d) > 8 {
		return string(d[:8])
	}
	return string(d)
}

// Identity is a device's private key and the operations that use it.
type Identity struct {
	priv ed25519.PrivateKey
}

// Generate creates a new random identity.
func Generate() (*Identity, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("identity: generate key: %w", err)
	}
	return &Identity{priv: priv}, nil
}

// PublicKey returns the device's public key.
func (i *Identity) PublicKey() ed25519.PublicKey {
	return i.priv.Public().(ed25519.PublicKey)
}

// ID returns the device id.
func (i *Identity) ID() DeviceID {
	return IDFromPublicKey(i.PublicKey())
}

// Sign signs msg with the device's private key.
func (i *Identity) Sign(msg []byte) []byte {
	return ed25519.Sign(i.priv, msg)
}

// Verify reports whether sig is a valid signature of msg by pub.
// It returns false, rather than panicking, if pub has the wrong length.
func Verify(pub ed25519.PublicKey, msg, sig []byte) bool {
	if len(pub) != ed25519.PublicKeySize {
		return false
	}
	return ed25519.Verify(pub, msg, sig)
}

// ParsePublicKey validates raw public key bytes and returns a copy.
func ParsePublicKey(b []byte) (ed25519.PublicKey, error) {
	if len(b) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("%w: public key must be %d bytes, got %d",
			ErrBadKey, ed25519.PublicKeySize, len(b))
	}
	pub := make(ed25519.PublicKey, len(b))
	copy(pub, b)
	return pub, nil
}
