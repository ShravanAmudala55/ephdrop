package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"math/big"
	"time"
)

// ErrUnknownPeer is returned when a TLS peer presents a key that is not allowed.
var ErrUnknownPeer = errors.New("identity: peer is not paired")

// TLSCertificate returns a self-signed certificate for the device key.
//
// The certificate carries no trust by itself. ephdrop peers ignore the usual
// certificate chain checks and instead pin the peer's public key (see
// PinnedConfig). The long validity period is therefore harmless.
func (i *Identity) TLSCertificate() (tls.Certificate, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("identity: certificate serial: %w", err)
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "ephdrop " + string(i.ID())},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(100, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, i.PublicKey(), i.priv)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("identity: create certificate: %w", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: i.priv}, nil
}

// PeerIDFromCertificate returns the device id for a DER encoded certificate.
// The certificate must contain an ed25519 public key.
func PeerIDFromCertificate(der []byte) (DeviceID, error) {
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return "", fmt.Errorf("identity: parse peer certificate: %w", err)
	}
	pub, ok := cert.PublicKey.(ed25519.PublicKey)
	if !ok {
		return "", fmt.Errorf("%w: peer certificate is not ed25519", ErrBadKey)
	}
	return IDFromPublicKey(pub), nil
}

// PinnedConfig returns a TLS 1.3 config for both ends of an ephdrop connection.
//
// Both sides present their certificate and require the other side's. Instead
// of chain validation, the peer's public key is hashed to a device id and
// passed to allow. The TLS handshake itself proves the peer holds the private
// key for the certificate, so a matching id means the right device.
//
// Use the same function for servers and clients. allow should return true only
// for devices that have been paired.
func PinnedConfig(self tls.Certificate, allow func(DeviceID) bool) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{self},
		MinVersion:   tls.VersionTLS13,
		ClientAuth:   tls.RequireAnyClientCert,
		// Chain validation is replaced by key pinning in VerifyPeerCertificate.
		InsecureSkipVerify: true, //nolint:gosec
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			if len(raw) == 0 {
				return fmt.Errorf("%w: no certificate presented", ErrUnknownPeer)
			}
			id, err := PeerIDFromCertificate(raw[0])
			if err != nil {
				return err
			}
			if !allow(id) {
				return fmt.Errorf("%w: %s", ErrUnknownPeer, id.Short())
			}
			return nil
		},
	}
}
