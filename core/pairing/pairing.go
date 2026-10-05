// Package pairing lets two devices trust each other once, using a one time
// invite shown as a QR code.
//
// How it works:
//
//  1. Device A (the inviter) creates an Invite holding its public key, its
//     addresses on the local network, a random secret and an expiry time. It
//     shows the invite as a QR code.
//  2. Device B (the joiner) scans the QR code, connects to A and checks that A
//     presents the key from the QR code. The QR code is the trusted channel,
//     so this stops anyone else from posing as A.
//  3. B proves it knows the secret. The proof is bound to the TLS session, so a
//     recorded proof cannot be replayed.
//  4. Only after a valid proof does A ask its user "pair with B?". An attacker
//     without the QR code never gets that far.
//  5. If the user agrees, both devices store each other's public key in a Store
//     and A discards the invite.
//
// After pairing, devices connect with identity.PinnedConfig and Store.Allow.
package pairing

import (
	"errors"
	"time"
)

var (
	// ErrInvalidInvite means an invite string could not be understood or is unsafe.
	ErrInvalidInvite = errors.New("pairing: invalid invite")
	// ErrInviteExpired means the invite is past its expiry time.
	ErrInviteExpired = errors.New("pairing: invite expired")
	// ErrBadProof means the joiner did not know the pairing secret.
	ErrBadProof = errors.New("pairing: wrong pairing secret")
	// ErrRejected means the user on the inviting device declined to pair.
	ErrRejected = errors.New("pairing: pairing was declined")
	// ErrTooManyAttempts means the inviter gave up after repeated wrong secrets.
	ErrTooManyAttempts = errors.New("pairing: too many failed attempts")
)

const (
	secretSize    = 16
	maxAddrs      = 8
	maxNameRunes  = 64
	maxMessage    = 4096
	maxAttempts   = 5
	helloTimeout  = 10 * time.Second
	joinTimeout   = 3 * time.Minute
	defaultWait   = 2 * time.Minute
	exporterLabel = "EXPORTER-ephdrop-pairing-v1"
)

// now is replaced in tests.
var now = time.Now
