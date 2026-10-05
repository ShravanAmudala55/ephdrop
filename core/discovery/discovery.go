// Package discovery finds paired devices on the local network.
//
// It has two layers:
//
//   - The announcement format (Advertisement, ParseTXT, SightingFromService):
//     what a device publishes over DNS-SD / Bonjour, and how announcements it
//     hears are checked. Every platform uses this, so they all agree.
//   - Finder: keeps track of which paired devices are visible right now and at
//     which addresses, expires stale ones, and reports changes.
//
// How announcements are actually sent and received is up to a Backend. Desktop
// and Android can use a Go mDNS library. iOS cannot: sending or receiving raw
// multicast there needs an Apple entitlement that SideStore builds cannot get,
// so the iOS app uses the system's Bonjour APIs (NWBrowser and NetService) and
// reports what it finds with Finder.Seen and Finder.Gone.
//
// Announcements are not authenticated. Anyone on the network can claim any id.
// That is safe because discovery only suggests where to connect. The connection
// itself is pinned to the paired device's key (see identity.PinnedConfig), so a
// device that lies about its id is refused at the handshake.
package discovery

import (
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/ShravanAmudala55/ephdrop/core/identity"
)

const (
	// ServiceType is the DNS-SD service type devices advertise and browse for.
	// iOS apps must also list "_ephdrop._tcp" under NSBonjourServices.
	ServiceType = "_ephdrop._tcp"
	// Domain is the mDNS domain.
	Domain = "local."
	// ProtocolVersion is the announcement format version.
	ProtocolVersion = 1

	maxAddrs     = 8
	maxTXTLength = 255 // limit for one TXT entry in DNS-SD
)

var (
	// ErrBadAdvertisement means an announcement is malformed.
	ErrBadAdvertisement = errors.New("discovery: bad advertisement")
	// ErrUnsupportedVersion means the announcement is from a newer protocol.
	ErrUnsupportedVersion = errors.New("discovery: unsupported protocol version")
	// ErrNoUsableAddress means no announced address is acceptable.
	ErrNoUsableAddress = errors.New("discovery: no usable address")
)

var idPattern = regexp.MustCompile(`^[a-z2-7]{26}$`)

// Advertisement is what this device announces about itself. The addresses are
// not part of it: the mDNS layer adds this machine's own addresses.
type Advertisement struct {
	ID   identity.DeviceID
	Port int
}

// Validate checks the advertisement.
func (a Advertisement) Validate() error {
	if !idPattern.MatchString(string(a.ID)) {
		return fmt.Errorf("%w: device id %q", ErrBadAdvertisement, a.ID)
	}
	if a.Port < 1 || a.Port > 65535 {
		return fmt.Errorf("%w: port %d", ErrBadAdvertisement, a.Port)
	}
	return nil
}

// InstanceName is the DNS-SD instance name: "ephdrop-" and the first 12
// characters of the id. It only needs to be unique on the network. The id in
// the TXT record is what counts.
func (a Advertisement) InstanceName() string {
	id := string(a.ID)
	if len(id) > 12 {
		id = id[:12]
	}
	return "ephdrop-" + id
}

// TXT returns the DNS-SD TXT record entries to publish.
func (a Advertisement) TXT() []string {
	return []string{"v=" + strconv.Itoa(ProtocolVersion), "id=" + string(a.ID)}
}

// ParseTXT reads the device id from TXT record entries. Keys are matched
// case-insensitively and the first entry for a key wins, as DNS-SD specifies.
func ParseTXT(txt []string) (identity.DeviceID, error) {
	kv := map[string]string{}
	for _, entry := range txt {
		if len(entry) > maxTXTLength {
			return "", fmt.Errorf("%w: entry too long", ErrBadAdvertisement)
		}
		k, v, ok := strings.Cut(entry, "=")
		if !ok {
			continue // a key with no value carries nothing we need
		}
		k = strings.ToLower(k)
		if _, seen := kv[k]; !seen {
			kv[k] = v
		}
	}

	ver, ok := kv["v"]
	if !ok {
		return "", fmt.Errorf("%w: no version", ErrBadAdvertisement)
	}
	n, err := strconv.Atoi(ver)
	if err != nil {
		return "", fmt.Errorf("%w: version %q", ErrBadAdvertisement, ver)
	}
	if n != ProtocolVersion {
		return "", fmt.Errorf("%w: %d", ErrUnsupportedVersion, n)
	}
	id := kv["id"]
	if !idPattern.MatchString(id) {
		return "", fmt.Errorf("%w: device id %q", ErrBadAdvertisement, id)
	}
	return identity.DeviceID(id), nil
}

// Sighting is a device heard on the network: its claimed id and the host:port
// addresses it can be reached at.
type Sighting struct {
	ID    identity.DeviceID
	Addrs []string
}

// SightingFromService builds a Sighting from the parts of a resolved DNS-SD
// service. Backends call this so every platform applies the same checks.
// Addresses that are not on a local network are dropped.
func SightingFromService(txt []string, port int, ips []netip.Addr) (Sighting, error) {
	id, err := ParseTXT(txt)
	if err != nil {
		return Sighting{}, err
	}
	if port < 1 || port > 65535 {
		return Sighting{}, fmt.Errorf("%w: port %d", ErrBadAdvertisement, port)
	}
	var addrs []string
	for _, ip := range ips {
		ip = ip.Unmap()
		if !localAddr(ip) {
			continue
		}
		addrs = append(addrs, netip.AddrPortFrom(ip, uint16(port)).String())
	}
	addrs = tidy(addrs)
	if len(addrs) == 0 {
		return Sighting{}, ErrNoUsableAddress
	}
	return Sighting{ID: id, Addrs: addrs}, nil
}

// localAddr reports whether ip belongs on a home network: private, link-local
// or loopback. Announcements pointing anywhere else are ignored, so a hostile
// device cannot make us connect (and reveal our certificate) to the internet.
func localAddr(ip netip.Addr) bool {
	if !ip.IsValid() || ip.IsUnspecified() || ip.IsMulticast() {
		return false
	}
	return ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast()
}

// parseAddr parses a host:port with an IP literal host. With strict set, the
// address must also be a local one.
func parseAddr(s string, strict bool) (netip.AddrPort, error) {
	ap, err := netip.ParseAddrPort(s)
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("discovery: bad address %q", s)
	}
	ip := ap.Addr().Unmap()
	ap = netip.AddrPortFrom(ip, ap.Port())
	if ap.Port() == 0 || ip.IsUnspecified() || ip.IsMulticast() {
		return netip.AddrPort{}, fmt.Errorf("discovery: unusable address %q", s)
	}
	if strict && !localAddr(ip) {
		return netip.AddrPort{}, fmt.Errorf("discovery: address %q is not on a local network", s)
	}
	return ap, nil
}

// cleanAddrs validates, normalizes, dedupes and orders addresses, dropping any
// that fail. At most maxAddrs are kept.
func cleanAddrs(in []string, strict bool) []string {
	var out []string
	for _, s := range in {
		if ap, err := parseAddr(s, strict); err == nil {
			out = append(out, ap.String())
		}
	}
	return tidy(out)
}

// tidy dedupes and orders addresses (IPv4 before IPv6, then alphabetically)
// and keeps at most maxAddrs.
func tidy(addrs []string) []string {
	seen := map[string]bool{}
	out := addrs[:0:0]
	for _, a := range addrs {
		if !seen[a] {
			seen[a] = true
			out = append(out, a)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		i6, j6 := strings.HasPrefix(out[i], "["), strings.HasPrefix(out[j], "[")
		if i6 != j6 {
			return !i6
		}
		return out[i] < out[j]
	})
	if len(out) > maxAddrs {
		out = out[:maxAddrs]
	}
	return out
}
