package pairing

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"net"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShravanAmudala55/ephdrop/core/identity"
)

type node struct {
	id    *identity.Identity
	store *Store
	name  string
}

func newNode(t *testing.T, name string) node {
	t.Helper()
	s, err := OpenStore(filepath.Join(t.TempDir(), "peers.json"))
	if err != nil {
		t.Fatal(err)
	}
	return node{id: newIdentity(t), store: s, name: name}
}

func listen(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	return ln
}

type serveResult struct {
	peer Peer
	err  error
}

// startInviter makes an invite for n, serves it on a fresh listener and
// returns the invite, the inviter and a channel for Serve's result.
func startInviter(t *testing.T, ctx context.Context, n node, confirm func(Peer) bool, ttl time.Duration) (*Invite, <-chan serveResult) {
	t.Helper()
	ln := listen(t)
	inv, err := NewInvite(n.id, []string{ln.Addr().String()}, ttl)
	if err != nil {
		t.Fatal(err)
	}
	v := &Inviter{Self: n.id, Name: n.name, Store: n.store, Invite: inv, Confirm: confirm}
	ch := make(chan serveResult, 1)
	go func() {
		p, err := v.Serve(ctx, ln)
		ch <- serveResult{p, err}
	}()
	return inv, ch
}

func result(t *testing.T, ch <-chan serveResult) serveResult {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return")
		return serveResult{}
	}
}

func yes(Peer) bool { return true }

func withBadSecret(inv *Invite) *Invite {
	c := *inv
	c.Secret = append([]byte(nil), inv.Secret...)
	c.Secret[0] ^= 0xff
	return &c
}

func TestPairEndToEnd(t *testing.T) {
	a := newNode(t, "Laptop")
	b := newNode(t, "Pixel")

	var seen Peer
	inv, ch := startInviter(t, context.Background(), a, func(p Peer) bool { seen = p; return true }, time.Minute)

	// Go through the text form, as a scanned QR code would.
	scanned, err := ParseInvite(inv.String())
	if err != nil {
		t.Fatal(err)
	}
	got, err := Join(context.Background(), b.id, b.name, b.store, scanned)
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	r := result(t, ch)
	if r.err != nil {
		t.Fatalf("Serve: %v", r.err)
	}

	if seen.ID != b.id.ID() || seen.Name != "Pixel" || !seen.PublicKey.Equal(b.id.PublicKey()) {
		t.Fatalf("Confirm saw %+v", seen)
	}
	if got.ID != a.id.ID() || got.Name != "Laptop" {
		t.Fatalf("Join returned %+v", got)
	}
	if r.peer.ID != b.id.ID() {
		t.Fatalf("Serve returned %+v", r.peer)
	}
	if !a.store.Allow(b.id.ID()) || !b.store.Allow(a.id.ID()) {
		t.Fatal("devices do not trust each other after pairing")
	}
}

func TestPairedDevicesCanConnectAndStrangersCannot(t *testing.T) {
	a := newNode(t, "A")
	b := newNode(t, "B")
	inv, ch := startInviter(t, context.Background(), a, yes, time.Minute)
	if _, err := Join(context.Background(), b.id, b.name, b.store, inv); err != nil {
		t.Fatal(err)
	}
	if r := result(t, ch); r.err != nil {
		t.Fatal(r.err)
	}

	aCert, _ := a.id.TLSCertificate()
	bCert, _ := b.id.TLSCertificate()
	srv := identity.PinnedConfig(aCert, a.store.Allow)

	if err := tlsConnect(t, srv, identity.PinnedConfig(bCert, b.store.Allow)); err != nil {
		t.Fatalf("paired devices could not connect: %v", err)
	}

	stranger := newIdentity(t)
	sCert, _ := stranger.TLSCertificate()
	if err := tlsConnect(t, srv, identity.PinnedConfig(sCert, func(identity.DeviceID) bool { return true })); err == nil {
		t.Fatal("a stranger connected to a paired device")
	}
}

// tlsConnect handshakes over loopback and returns the server side's error, or
// the client's if the server had none.
func tlsConnect(t *testing.T, srvCfg, cliCfg *tls.Config) error {
	t.Helper()
	ln := listen(t)
	srvDone := make(chan error, 1)
	go func() {
		raw, err := ln.Accept()
		if err != nil {
			srvDone <- err
			return
		}
		defer raw.Close()
		raw.SetDeadline(time.Now().Add(5 * time.Second))
		srvDone <- tls.Server(raw, srvCfg).Handshake()
	}()
	raw, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	raw.SetDeadline(time.Now().Add(5 * time.Second))
	cliErr := tls.Client(raw, cliCfg).Handshake()
	if srvErr := <-srvDone; srvErr != nil {
		return srvErr
	}
	return cliErr
}

func TestWrongSecretIsRefusedThenRightSecretWorks(t *testing.T) {
	a, b := newNode(t, "A"), newNode(t, "B")
	var confirms atomic.Int32
	inv, ch := startInviter(t, context.Background(), a, func(Peer) bool { confirms.Add(1); return true }, time.Minute)

	_, err := Join(context.Background(), b.id, b.name, b.store, withBadSecret(inv))
	if !errors.Is(err, ErrBadProof) {
		t.Fatalf("want ErrBadProof, got %v", err)
	}
	if confirms.Load() != 0 {
		t.Fatal("user was asked to confirm a device that did not know the secret")
	}
	if b.store.Allow(a.id.ID()) || a.store.Allow(b.id.ID()) {
		t.Fatal("failed pairing left a trust entry behind")
	}

	if _, err := Join(context.Background(), b.id, b.name, b.store, inv); err != nil {
		t.Fatalf("correct secret after a failure: %v", err)
	}
	if r := result(t, ch); r.err != nil {
		t.Fatal(r.err)
	}
	if confirms.Load() != 1 {
		t.Fatalf("Confirm called %d times, want 1", confirms.Load())
	}
}

func TestTooManyWrongSecretsClosesTheInvite(t *testing.T) {
	a, b := newNode(t, "A"), newNode(t, "B")
	var confirms atomic.Int32
	inv, ch := startInviter(t, context.Background(), a, func(Peer) bool { confirms.Add(1); return true }, time.Minute)

	for i := 0; i < maxAttempts; i++ {
		if _, err := Join(context.Background(), b.id, b.name, b.store, withBadSecret(inv)); !errors.Is(err, ErrBadProof) {
			t.Fatalf("attempt %d: want ErrBadProof, got %v", i+1, err)
		}
	}
	if r := result(t, ch); !errors.Is(r.err, ErrTooManyAttempts) {
		t.Fatalf("Serve: want ErrTooManyAttempts, got %v", r.err)
	}
	if confirms.Load() != 0 {
		t.Fatal("Confirm was called")
	}
	// The invite is now dead, even for the right secret.
	if _, err := Join(context.Background(), b.id, b.name, b.store, inv); err == nil {
		t.Fatal("joined with a closed invite")
	}
}

func TestInviterUserDeclines(t *testing.T) {
	a, b := newNode(t, "A"), newNode(t, "B")
	inv, ch := startInviter(t, context.Background(), a, func(Peer) bool { return false }, time.Minute)

	_, err := Join(context.Background(), b.id, b.name, b.store, inv)
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("Join: want ErrRejected, got %v", err)
	}
	if r := result(t, ch); !errors.Is(r.err, ErrRejected) {
		t.Fatalf("Serve: want ErrRejected, got %v", r.err)
	}
	if a.store.Allow(b.id.ID()) || b.store.Allow(a.id.ID()) {
		t.Fatal("declined pairing left a trust entry behind")
	}
}

func TestNilConfirmDeclines(t *testing.T) {
	a, b := newNode(t, "A"), newNode(t, "B")
	inv, ch := startInviter(t, context.Background(), a, nil, time.Minute)
	if _, err := Join(context.Background(), b.id, b.name, b.store, inv); !errors.Is(err, ErrRejected) {
		t.Fatalf("want ErrRejected, got %v", err)
	}
	result(t, ch)
}

func TestJoinerRefusesAnImpostorInviter(t *testing.T) {
	a := newNode(t, "Real")
	mallory := newNode(t, "Mallory")
	b := newNode(t, "Joiner")

	var confirms atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mallInv, ch := startInviter(t, ctx, mallory, func(Peer) bool { confirms.Add(1); return true }, time.Minute)

	// The QR code is for the real device, but its address leads to Mallory.
	inv, err := NewInvite(a.id, mallInv.Addrs, time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	_, err = Join(context.Background(), b.id, b.name, b.store, inv)
	if !errors.Is(err, identity.ErrUnknownPeer) {
		t.Fatalf("want ErrUnknownPeer, got %v", err)
	}
	if confirms.Load() != 0 || b.store.Allow(mallory.id.ID()) {
		t.Fatal("joiner trusted or talked to an impostor")
	}
	cancel()
	if r := result(t, ch); !errors.Is(r.err, context.Canceled) {
		t.Fatalf("Serve: want context.Canceled, got %v", r.err)
	}
}

func TestInviteExpiresWhileWaiting(t *testing.T) {
	a := newNode(t, "A")
	start := time.Now()
	_, ch := startInviter(t, context.Background(), a, yes, time.Second)
	r := result(t, ch)
	if !errors.Is(r.err, ErrInviteExpired) {
		t.Fatalf("want ErrInviteExpired, got %v", r.err)
	}
	if time.Since(start) > 4*time.Second {
		t.Fatalf("took %v to notice the expiry", time.Since(start))
	}
}

func TestServeStopsWhenContextIsCancelled(t *testing.T) {
	a := newNode(t, "A")
	ctx, cancel := context.WithCancel(context.Background())
	_, ch := startInviter(t, ctx, a, yes, time.Minute)
	cancel()
	if r := result(t, ch); !errors.Is(r.err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", r.err)
	}
}

func TestJoinWithExpiredInvite(t *testing.T) {
	a, b := newNode(t, "A"), newNode(t, "B")
	inv, _ := NewInvite(a.id, []string{"127.0.0.1:1"}, time.Minute)
	setNow(t, func() time.Time { return time.Now().Add(time.Hour) })
	if _, err := Join(context.Background(), b.id, b.name, b.store, inv); !errors.Is(err, ErrInviteExpired) {
		t.Fatalf("want ErrInviteExpired, got %v", err)
	}
}

func TestJoinWhenNobodyIsListening(t *testing.T) {
	a, b := newNode(t, "A"), newNode(t, "B")
	ln := listen(t)
	addr := ln.Addr().String()
	ln.Close()
	inv, _ := NewInvite(a.id, []string{addr}, time.Minute)
	if _, err := Join(context.Background(), b.id, b.name, b.store, inv); err == nil {
		t.Fatal("expected an error")
	}
}

func TestJoinTriesNextAddress(t *testing.T) {
	a, b := newNode(t, "A"), newNode(t, "B")
	dead := listen(t)
	deadAddr := dead.Addr().String()
	dead.Close()

	ln := listen(t)
	inv, _ := NewInvite(a.id, []string{deadAddr, ln.Addr().String()}, time.Minute)
	v := &Inviter{Self: a.id, Name: "A", Store: a.store, Invite: inv, Confirm: yes}
	ch := make(chan serveResult, 1)
	go func() { p, err := v.Serve(context.Background(), ln); ch <- serveResult{p, err} }()

	if _, err := Join(context.Background(), b.id, b.name, b.store, inv); err != nil {
		t.Fatalf("Join: %v", err)
	}
	if r := result(t, ch); r.err != nil {
		t.Fatal(r.err)
	}
}

func TestGarbageConnectionsDoNotBlockOrCountAsAttempts(t *testing.T) {
	a, b := newNode(t, "A"), newNode(t, "B")
	ln := listen(t)
	inv, _ := NewInvite(a.id, []string{ln.Addr().String()}, time.Minute)
	v := &Inviter{Self: a.id, Name: "A", Store: a.store, Invite: inv, Confirm: yes}
	ch := make(chan serveResult, 1)
	go func() { p, err := v.Serve(context.Background(), ln); ch <- serveResult{p, err} }()

	for i := 0; i < maxAttempts+2; i++ {
		c, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		c.Write([]byte("GET / HTTP/1.1\r\n\r\n"))
		c.Close()
	}
	// One real wrong secret must still count as just one failed attempt, not
	// tip the invite over the limit because of the junk before it.
	if _, err := Join(context.Background(), b.id, b.name, b.store, withBadSecret(inv)); !errors.Is(err, ErrBadProof) {
		t.Fatalf("want ErrBadProof, got %v", err)
	}
	if _, err := Join(context.Background(), b.id, b.name, b.store, inv); err != nil {
		t.Fatalf("Join after garbage: %v", err)
	}
	if r := result(t, ch); r.err != nil {
		t.Fatal(r.err)
	}
}

func TestJoinerRejectsAcceptWithoutValidProof(t *testing.T) {
	// A device that holds the inviter's key but not the QR secret (for example
	// a stolen key file) answers "accepted" with a made up proof.
	a, b := newNode(t, "A"), newNode(t, "B")
	ln := listen(t)
	inv, _ := NewInvite(a.id, []string{ln.Addr().String()}, time.Minute)
	cert, _ := a.id.TLSCertificate()
	cfg := identity.PinnedConfig(cert, func(identity.DeviceID) bool { return true })

	go func() {
		raw, err := ln.Accept()
		if err != nil {
			return
		}
		defer raw.Close()
		raw.SetDeadline(time.Now().Add(5 * time.Second))
		tc := tls.Server(raw, cfg)
		if tc.Handshake() != nil {
			return
		}
		readMessage(bufio.NewReaderSize(tc, maxMessage))
		writeMessage(tc, message{Type: msgAccept, Name: "A", Proof: []byte("not the right proof")})
	}()

	if _, err := Join(context.Background(), b.id, b.name, b.store, inv); err == nil {
		t.Fatal("joiner accepted a reply without a valid proof")
	}
	if b.store.Allow(a.id.ID()) {
		t.Fatal("joiner trusted a device that did not prove it knew the secret")
	}
}

func TestInviterRejectsMismatchedInvite(t *testing.T) {
	a, other := newNode(t, "A"), newNode(t, "Other")
	inv, _ := NewInvite(other.id, []string{"127.0.0.1:1"}, time.Minute)
	v := &Inviter{Self: a.id, Store: a.store, Invite: inv, Confirm: yes}
	if _, err := v.Serve(context.Background(), listen(t)); err == nil {
		t.Fatal("expected an error for an invite made for another device")
	}
}

func TestProofIsBoundToSessionAndKeys(t *testing.T) {
	secret := []byte("0123456789abcdef")
	e1, e2 := []byte("exporter-one"), []byte("exporter-two")
	j, i := []byte("joiner-key"), []byte("inviter-key")

	base := computeProof(secret, "join", e1, j, i)
	if string(base) != string(computeProof(secret, "join", e1, j, i)) {
		t.Fatal("proof is not deterministic")
	}
	for name, other := range map[string][]byte{
		"different session": computeProof(secret, "join", e2, j, i),
		"different joiner":  computeProof(secret, "join", e1, []byte("x"), i),
		"different inviter": computeProof(secret, "join", e1, j, []byte("x")),
		"different role":    computeProof(secret, "accept", e1, j, i),
		"different secret":  computeProof([]byte("fedcba9876543210"), "join", e1, j, i),
		"fields shifted":    computeProof(secret, "join", e1, []byte("joiner-keyi"), []byte("nviter-key")),
	} {
		if string(base) == string(other) {
			t.Errorf("proof unchanged for %s", name)
		}
	}
}
