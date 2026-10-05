package identity

import (
	"crypto/ed25519"
	"crypto/tls"
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"
	"time"
)

func mustGenerate(t *testing.T) *Identity {
	t.Helper()
	id, err := Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	return id
}

func TestGenerateUniqueIDs(t *testing.T) {
	seen := map[DeviceID]bool{}
	for n := 0; n < 50; n++ {
		id := mustGenerate(t).ID()
		if seen[id] {
			t.Fatalf("duplicate id %s", id)
		}
		seen[id] = true
	}
}

func TestIDFormat(t *testing.T) {
	id := mustGenerate(t).ID()
	if !regexp.MustCompile(`^[a-z2-7]{26}$`).MatchString(string(id)) {
		t.Fatalf("unexpected id format: %q", id)
	}
	if got := id.Short(); got != string(id)[:8] {
		t.Fatalf("Short = %q", got)
	}
	if DeviceID("abc").Short() != "abc" {
		t.Fatal("Short should not truncate short ids")
	}
}

func TestIDIsStableForKey(t *testing.T) {
	a := mustGenerate(t)
	if a.ID() != IDFromPublicKey(a.PublicKey()) {
		t.Fatal("ID does not match IDFromPublicKey")
	}
}

func TestSignVerify(t *testing.T) {
	a, b := mustGenerate(t), mustGenerate(t)
	msg := []byte("hello ephdrop")
	sig := a.Sign(msg)

	if !Verify(a.PublicKey(), msg, sig) {
		t.Fatal("valid signature rejected")
	}
	if Verify(a.PublicKey(), []byte("tampered"), sig) {
		t.Fatal("tampered message accepted")
	}
	if Verify(b.PublicKey(), msg, sig) {
		t.Fatal("signature accepted for wrong key")
	}
	if Verify(ed25519.PublicKey{1, 2, 3}, msg, sig) {
		t.Fatal("short public key accepted")
	}
}

func TestParsePublicKey(t *testing.T) {
	a := mustGenerate(t)
	pub, err := ParsePublicKey(a.PublicKey())
	if err != nil || !pub.Equal(a.PublicKey()) {
		t.Fatalf("ParsePublicKey: %v", err)
	}
	if _, err := ParsePublicKey([]byte{1, 2, 3}); !errors.Is(err, ErrBadKey) {
		t.Fatalf("want ErrBadKey, got %v", err)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	a := mustGenerate(t)
	path := filepath.Join(t.TempDir(), "k.pem")
	if err := a.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	b, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if a.ID() != b.ID() {
		t.Fatal("id changed after save and load")
	}
	if !Verify(a.PublicKey(), []byte("x"), b.Sign([]byte("x"))) {
		t.Fatal("loaded key does not match original")
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("key file mode = %v, want 0600", fi.Mode().Perm())
		}
	}
}

func TestSaveLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	if err := mustGenerate(t).Save(filepath.Join(dir, KeyFileName)); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("want only the key file, got %d entries", len(entries))
	}
}

func TestLoadOrCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "data")
	first, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatalf("first LoadOrCreate: %v", err)
	}
	second, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatalf("second LoadOrCreate: %v", err)
	}
	if first.ID() != second.ID() {
		t.Fatal("identity changed between runs")
	}
}

func TestLoadMissing(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "nope"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("want ErrNotExist, got %v", err)
	}
}

func TestLoadCorrupt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.pem")
	if err := os.WriteFile(path, []byte("not a key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); !errors.Is(err, ErrBadKey) {
		t.Fatalf("want ErrBadKey, got %v", err)
	}
}

func TestLoadOrCreateDoesNotOverwriteCorrupt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, KeyFileName)
	if err := os.WriteFile(path, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreate(dir); !errors.Is(err, ErrBadKey) {
		t.Fatalf("want ErrBadKey, got %v", err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "garbage" {
		t.Fatal("corrupt key file was overwritten")
	}
}

func TestPeerIDFromCertificate(t *testing.T) {
	a := mustGenerate(t)
	cert, err := a.TLSCertificate()
	if err != nil {
		t.Fatal(err)
	}
	got, err := PeerIDFromCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if got != a.ID() {
		t.Fatalf("cert id %s != identity id %s", got, a.ID())
	}
	if _, err := PeerIDFromCertificate([]byte("junk")); err == nil {
		t.Fatal("junk certificate accepted")
	}
}

func pinnedPair(t *testing.T) (a, b *Identity, aCert, bCert tls.Certificate) {
	t.Helper()
	a, b = mustGenerate(t), mustGenerate(t)
	var err error
	if aCert, err = a.TLSCertificate(); err != nil {
		t.Fatal(err)
	}
	if bCert, err = b.TLSCertificate(); err != nil {
		t.Fatal(err)
	}
	return
}

// connect runs a TLS handshake between a server and a client over loopback
// TCP and returns the handshake error from each side.
func connect(t *testing.T, srvCfg, cliCfg *tls.Config) (srvErr, cliErr error) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

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

	raw, err := net.DialTimeout("tcp", ln.Addr().String(), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	raw.SetDeadline(time.Now().Add(5 * time.Second))
	cliErr = tls.Client(raw, cliCfg).Handshake()

	// In TLS 1.3 the client can finish before the server has checked the
	// client certificate, so always wait for the server's result too.
	return <-srvDone, cliErr
}

func TestPinnedTLSAcceptsPairedPeers(t *testing.T) {
	a, b, aCert, bCert := pinnedPair(t)
	srvCfg := PinnedConfig(aCert, func(id DeviceID) bool { return id == b.ID() })
	cliCfg := PinnedConfig(bCert, func(id DeviceID) bool { return id == a.ID() })

	srvErr, cliErr := connect(t, srvCfg, cliCfg)
	if srvErr != nil || cliErr != nil {
		t.Fatalf("handshake failed: server=%v client=%v", srvErr, cliErr)
	}
}

func TestPinnedTLSRejectsUnknownClient(t *testing.T) {
	a, _, aCert, bCert := pinnedPair(t)
	srvCfg := PinnedConfig(aCert, func(DeviceID) bool { return false })
	cliCfg := PinnedConfig(bCert, func(id DeviceID) bool { return id == a.ID() })

	srvErr, _ := connect(t, srvCfg, cliCfg)
	if !errors.Is(srvErr, ErrUnknownPeer) {
		t.Fatalf("server: want ErrUnknownPeer, got %v", srvErr)
	}
}

func TestPinnedTLSRejectsUnknownServer(t *testing.T) {
	_, b, aCert, bCert := pinnedPair(t)
	srvCfg := PinnedConfig(aCert, func(id DeviceID) bool { return id == b.ID() })
	cliCfg := PinnedConfig(bCert, func(DeviceID) bool { return false })

	_, cliErr := connect(t, srvCfg, cliCfg)
	if !errors.Is(cliErr, ErrUnknownPeer) {
		t.Fatalf("client: want ErrUnknownPeer, got %v", cliErr)
	}
}

func TestPinnedTLSRejectsImpostor(t *testing.T) {
	// The server expects device B, but a third device C connects.
	a, b, aCert, _ := pinnedPair(t)
	c := mustGenerate(t)
	cCert, err := c.TLSCertificate()
	if err != nil {
		t.Fatal(err)
	}
	srvCfg := PinnedConfig(aCert, func(id DeviceID) bool { return id == b.ID() })
	cliCfg := PinnedConfig(cCert, func(id DeviceID) bool { return id == a.ID() })

	srvErr, _ := connect(t, srvCfg, cliCfg)
	if !errors.Is(srvErr, ErrUnknownPeer) {
		t.Fatalf("server: want ErrUnknownPeer, got %v", srvErr)
	}
}
