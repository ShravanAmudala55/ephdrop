package pairing

import (
	"bufio"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Message types and reject codes on the wire. Messages are single lines of JSON.
const (
	msgHello  = "hello"
	msgAccept = "accept"
	msgReject = "reject"

	codeBadProof = "bad_proof"
	codeDeclined = "declined"
)

type message struct {
	Type   string `json:"type"`
	Name   string `json:"name,omitempty"`
	Proof  []byte `json:"proof,omitempty"`
	Code   string `json:"code,omitempty"`
	Reason string `json:"reason,omitempty"`
}

var errBadMessage = errors.New("pairing: malformed message")

// readMessage reads one line of JSON, refusing lines longer than maxMessage.
// r must have been created with a buffer of at least maxMessage bytes.
func readMessage(r *bufio.Reader) (message, error) {
	line, err := r.ReadSlice('\n')
	if errors.Is(err, bufio.ErrBufferFull) {
		return message{}, fmt.Errorf("%w: too long", errBadMessage)
	}
	if err != nil {
		return message{}, err
	}
	var m message
	if err := json.Unmarshal(line, &m); err != nil {
		return message{}, fmt.Errorf("%w: %v", errBadMessage, err)
	}
	return m, nil
}

func writeMessage(w io.Writer, m message) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	_, err = w.Write(append(data, '\n'))
	return err
}

// computeProof returns HMAC-SHA256 over the secret, a role label, the TLS
// session's exported keying material and both public keys. Binding to the TLS
// session means a proof recorded from one connection is useless on another.
// role is "join" for the joiner's proof and "accept" for the inviter's.
func computeProof(secret []byte, role string, exporter, joinerKey, inviterKey []byte) []byte {
	mac := hmac.New(sha256.New, secret)
	for _, part := range [][]byte{
		[]byte("ephdrop pairing v1"), []byte(role), exporter, joinerKey, inviterKey,
	} {
		var n [4]byte
		binary.BigEndian.PutUint32(n[:], uint32(len(part)))
		mac.Write(n[:])
		mac.Write(part)
	}
	return mac.Sum(nil)
}

// sessionInfo extracts what both sides need from a finished TLS handshake.
func sessionInfo(c *tls.Conn) (peerKey ed25519.PublicKey, exporter []byte, err error) {
	st := c.ConnectionState()
	if len(st.PeerCertificates) == 0 {
		return nil, nil, errors.New("pairing: peer sent no certificate")
	}
	peerKey, ok := st.PeerCertificates[0].PublicKey.(ed25519.PublicKey)
	if !ok {
		return nil, nil, errors.New("pairing: peer key is not ed25519")
	}
	exporter, err = st.ExportKeyingMaterial(exporterLabel, nil, 32)
	if err != nil {
		return nil, nil, fmt.Errorf("pairing: export keying material: %w", err)
	}
	return peerKey, exporter, nil
}
