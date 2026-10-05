package pairing

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ShravanAmudala55/ephdrop/core/identity"
)

const (
	invitePrefix  = "ephdrop://pair/"
	inviteVersion = 1
)

// Invite is the one time information needed to pair with a device.
type Invite struct {
	// Key is the inviter's public key.
	Key ed25519.PublicKey
	// Addrs are host:port addresses where the inviter listens.
	Addrs []string
	// Secret is the random pairing secret. Treat it like a password.
	Secret []byte
	// Expires is when the invite stops being valid (whole seconds).
	Expires time.Time
}

// ID returns the inviter's device id.
func (i *Invite) ID() identity.DeviceID { return identity.IDFromPublicKey(i.Key) }

type invitePayload struct {
	V      int      `json:"v"`
	Key    []byte   `json:"k"`
	Addrs  []string `json:"a"`
	Secret []byte   `json:"s"`
	Exp    int64    `json:"e"`
}

// NewInvite creates an invite for self. ttl must be at least one second.
func NewInvite(self *identity.Identity, addrs []string, ttl time.Duration) (*Invite, error) {
	if ttl < time.Second {
		return nil, fmt.Errorf("pairing: invite lifetime must be at least 1s, got %v", ttl)
	}
	if err := validateAddrs(addrs); err != nil {
		return nil, err
	}
	secret := make([]byte, secretSize)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("pairing: generate secret: %w", err)
	}
	return &Invite{
		Key:     self.PublicKey(),
		Addrs:   append([]string(nil), addrs...),
		Secret:  secret,
		Expires: time.Unix(now().Add(ttl).Unix(), 0),
	}, nil
}

// String encodes the invite as text suitable for a QR code.
func (i *Invite) String() string {
	data, _ := json.Marshal(invitePayload{
		V:      inviteVersion,
		Key:    i.Key,
		Addrs:  i.Addrs,
		Secret: i.Secret,
		Exp:    i.Expires.Unix(),
	})
	return invitePrefix + base64.RawURLEncoding.EncodeToString(data)
}

// ParseInvite decodes and validates an invite string. It rejects expired invites.
func ParseInvite(s string) (*Invite, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, invitePrefix) {
		return nil, fmt.Errorf("%w: not an ephdrop invite", ErrInvalidInvite)
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(s, invitePrefix))
	if err != nil {
		return nil, fmt.Errorf("%w: bad encoding", ErrInvalidInvite)
	}
	var p invitePayload
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("%w: bad contents", ErrInvalidInvite)
	}
	if p.V != inviteVersion {
		return nil, fmt.Errorf("%w: unsupported version %d", ErrInvalidInvite, p.V)
	}
	key, err := identity.ParsePublicKey(p.Key)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidInvite, err)
	}
	if len(p.Secret) != secretSize {
		return nil, fmt.Errorf("%w: bad secret", ErrInvalidInvite)
	}
	if err := validateAddrs(p.Addrs); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidInvite, err)
	}
	inv := &Invite{Key: key, Addrs: p.Addrs, Secret: p.Secret, Expires: time.Unix(p.Exp, 0)}
	if !now().Before(inv.Expires) {
		return nil, ErrInviteExpired
	}
	return inv, nil
}

func validateAddrs(addrs []string) error {
	if len(addrs) == 0 {
		return fmt.Errorf("pairing: at least one address is required")
	}
	if len(addrs) > maxAddrs {
		return fmt.Errorf("pairing: at most %d addresses allowed, got %d", maxAddrs, len(addrs))
	}
	for _, a := range addrs {
		host, port, err := net.SplitHostPort(a)
		if err != nil || host == "" {
			return fmt.Errorf("pairing: bad address %q", a)
		}
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return fmt.Errorf("pairing: bad port in address %q", a)
		}
	}
	return nil
}

// AddrsFromIPs turns IP addresses that the app found itself into invite
// addresses with the given port. Phones use this because a Go program on
// Android is not always allowed to list the network interfaces. Only IPv4
// addresses that are not loopback or link local are kept.
func AddrsFromIPs(ips []string, port int) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range ips {
		ip := net.ParseIP(strings.TrimSpace(s)).To4()
		if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || seen[ip.String()] {
			continue
		}
		seen[ip.String()] = true
		out = append(out, net.JoinHostPort(ip.String(), strconv.Itoa(port)))
	}
	sort.Strings(out)
	if len(out) > maxAddrs {
		out = out[:maxAddrs]
	}
	return out
}

// LocalAddrs lists this machine's non-loopback IPv4 addresses with the given
// port, for use in an invite.
func LocalAddrs(port int) []string {
	ifaceAddrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var out []string
	for _, a := range ifaceAddrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip := ipnet.IP.To4()
		if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			continue
		}
		out = append(out, net.JoinHostPort(ip.String(), strconv.Itoa(port)))
	}
	sort.Strings(out)
	if len(out) > maxAddrs {
		out = out[:maxAddrs]
	}
	return out
}
