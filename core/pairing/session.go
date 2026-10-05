package pairing

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/ShravanAmudala55/ephdrop/core/identity"
)

// Inviter runs the inviting side of pairing.
type Inviter struct {
	Self   *identity.Identity
	Name   string // shown to the joining device
	Store  *Store
	Invite *Invite

	// Confirm is called after a joiner has proved it knows the secret. It
	// should ask the user whether to pair with the device and return the
	// answer. If Confirm is nil every request is declined.
	Confirm func(Peer) bool

	// Timeout is how long the joiner waits for Confirm. Zero means 2 minutes.
	Timeout time.Duration
}

// Serve accepts connections on ln until one device pairs successfully, the
// user declines, the invite expires, too many wrong secrets are tried, or ctx
// ends. It closes ln before returning.
//
// A successful pairing uses up the invite. To pair another device, make a new
// invite.
func (v *Inviter) Serve(parent context.Context, ln net.Listener) (Peer, error) {
	defer ln.Close()
	if v.Self == nil || v.Store == nil || v.Invite == nil {
		return Peer{}, errors.New("pairing: inviter is not fully configured")
	}
	if !v.Invite.Key.Equal(v.Self.PublicKey()) {
		return Peer{}, errors.New("pairing: invite was made for a different device")
	}
	cert, err := v.Self.TLSCertificate()
	if err != nil {
		return Peer{}, err
	}
	// Joiners are not paired yet, so any key may connect. The secret, checked
	// below, decides whether the pairing goes ahead.
	cfg := identity.PinnedConfig(cert, func(identity.DeviceID) bool { return true })

	ctx, cancel := context.WithDeadline(parent, v.Invite.Expires)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { ln.Close() })
	defer stop()

	failures := 0
	for {
		conn, err := ln.Accept()
		if err != nil {
			if perr := parent.Err(); perr != nil {
				return Peer{}, perr
			}
			if ctx.Err() != nil {
				return Peer{}, ErrInviteExpired
			}
			return Peer{}, err
		}
		peer, err := v.handle(ctx, conn, cfg)
		switch {
		case err == nil:
			return peer, nil
		case errors.Is(err, ErrBadProof):
			failures++
			if failures >= maxAttempts {
				return Peer{}, ErrTooManyAttempts
			}
		case errors.Is(err, ErrRejected):
			return Peer{}, ErrRejected
		}
		// Anything else is a broken or hostile connection. Ignore it and wait
		// for the next one. It does not count against the attempt limit.
	}
}

func (v *Inviter) handle(ctx context.Context, conn net.Conn, cfg *tls.Config) (Peer, error) {
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	conn.SetDeadline(time.Now().Add(helloTimeout))
	tc := tls.Server(conn, cfg)
	if err := tc.Handshake(); err != nil {
		return Peer{}, err
	}
	joinerKey, exporter, err := sessionInfo(tc)
	if err != nil {
		return Peer{}, err
	}

	m, err := readMessage(bufio.NewReaderSize(tc, maxMessage))
	if err != nil {
		return Peer{}, err
	}
	if m.Type != msgHello {
		return Peer{}, fmt.Errorf("%w: expected hello", errBadMessage)
	}

	want := computeProof(v.Invite.Secret, "join", exporter, joinerKey, v.Invite.Key)
	if !hmac.Equal(want, m.Proof) {
		writeMessage(tc, message{Type: msgReject, Code: codeBadProof, Reason: "wrong pairing secret"})
		return Peer{}, ErrBadProof
	}

	id := identity.IDFromPublicKey(joinerKey)
	peer := Peer{ID: id, Name: cleanName(m.Name, id), PublicKey: joinerKey, Added: time.Now()}

	wait := v.Timeout
	if wait <= 0 {
		wait = defaultWait
	}
	conn.SetDeadline(time.Now().Add(wait + helloTimeout))
	if v.Confirm == nil || !v.Confirm(peer) {
		writeMessage(tc, message{Type: msgReject, Code: codeDeclined, Reason: "declined by the other device"})
		return Peer{}, ErrRejected
	}

	// Save before replying, so we never tell the joiner "paired" and then
	// fail to remember it.
	if err := v.Store.Add(peer); err != nil {
		writeMessage(tc, message{Type: msgReject, Code: codeDeclined, Reason: "could not save pairing"})
		return Peer{}, err
	}
	reply := message{
		Type:  msgAccept,
		Name:  v.Name,
		Proof: computeProof(v.Invite.Secret, "accept", exporter, joinerKey, v.Invite.Key),
	}
	if err := writeMessage(tc, reply); err != nil {
		v.Store.Remove(id)
		return Peer{}, err
	}
	return peer, nil
}

// Join pairs this device with the device that made inv. It tries each address
// in the invite in turn and, on success, stores the inviter in store.
func Join(ctx context.Context, self *identity.Identity, name string, store *Store, inv *Invite) (Peer, error) {
	if !now().Before(inv.Expires) {
		return Peer{}, ErrInviteExpired
	}
	cert, err := self.TLSCertificate()
	if err != nil {
		return Peer{}, err
	}
	// Only the key from the QR code is accepted as the inviter.
	want := inv.ID()
	cfg := identity.PinnedConfig(cert, func(id identity.DeviceID) bool { return id == want })

	dialer := net.Dialer{Timeout: 5 * time.Second}
	var dialErrs []error
	for _, addr := range inv.Addrs {
		conn, err := dialer.DialContext(ctx, "tcp", addr)
		if err != nil {
			dialErrs = append(dialErrs, err)
			if ctx.Err() != nil {
				break
			}
			continue
		}
		return joinOver(ctx, conn, cfg, self, name, store, inv)
	}
	return Peer{}, fmt.Errorf("pairing: could not reach the other device: %w", errors.Join(dialErrs...))
}

func joinOver(ctx context.Context, conn net.Conn, cfg *tls.Config, self *identity.Identity,
	name string, store *Store, inv *Invite) (Peer, error) {

	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	conn.SetDeadline(time.Now().Add(joinTimeout))

	tc := tls.Client(conn, cfg)
	if err := tc.Handshake(); err != nil {
		return Peer{}, fmt.Errorf("pairing: secure connection failed: %w", err)
	}
	inviterKey, exporter, err := sessionInfo(tc)
	if err != nil {
		return Peer{}, err
	}
	selfKey := self.PublicKey()

	hello := message{
		Type:  msgHello,
		Name:  strings.TrimSpace(name),
		Proof: computeProof(inv.Secret, "join", exporter, selfKey, inviterKey),
	}
	if err := writeMessage(tc, hello); err != nil {
		return Peer{}, err
	}

	m, err := readMessage(bufio.NewReaderSize(tc, maxMessage))
	if err != nil {
		if ctx.Err() != nil {
			return Peer{}, ctx.Err()
		}
		return Peer{}, fmt.Errorf("pairing: no answer from the other device: %w", err)
	}
	switch m.Type {
	case msgReject:
		if m.Code == codeBadProof {
			return Peer{}, ErrBadProof
		}
		return Peer{}, fmt.Errorf("%w: %s", ErrRejected, cleanReason(m.Reason))
	case msgAccept:
	default:
		return Peer{}, fmt.Errorf("%w: unexpected %q", errBadMessage, m.Type)
	}

	// The inviter proves it knows the secret too, so both sides are sure.
	wantProof := computeProof(inv.Secret, "accept", exporter, selfKey, inviterKey)
	if !hmac.Equal(wantProof, m.Proof) {
		return Peer{}, fmt.Errorf("pairing: the other device did not prove it knows the secret")
	}

	id := inv.ID()
	peer := Peer{ID: id, Name: cleanName(m.Name, id), PublicKey: inviterKey, Added: time.Now()}
	if err := store.Add(peer); err != nil {
		return Peer{}, err
	}
	return peer, nil
}

// cleanReason shortens text from the other device before it is shown.
func cleanReason(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	if len(s) > 100 {
		s = s[:100]
	}
	return s
}
