// Package mdns is the discovery Backend for desktop platforms. It sends and
// receives DNS-SD announcements with the grandcat/zeroconf library.
//
// It lives in its own package so that the rest of the core stays free of
// outside dependencies. iOS does not use it, see the discovery package.
//
// Things this backend does because of how the library behaves:
//
//   - The library reports each device only once per browse session, so Browse
//     restarts its session every 30 seconds. A device that is still there
//     answers again and stays fresh in the Finder.
//   - The library throws away goodbye packets, so a device that leaves is
//     noticed when it stops answering and its entry expires.
//   - The library cannot start browsing if IPv6 multicast is unavailable, so
//     newResolver falls back to IPv4 only (and IPv6 only) when needed.
//   - The library's own Register would publish the computer's real host name
//     and every address on every interface, public ones included. Advertise
//     uses RegisterProxy instead, with a neutral host name and only private
//     addresses.
//
// Addresses are read when Advertise starts. If the network changes (Wi-Fi
// reconnects, a new address), stop and start the Finder again.
package mdns

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"time"

	"github.com/grandcat/zeroconf"

	"github.com/ShravanAmudala55/ephdrop/core/discovery"
	"github.com/ShravanAmudala55/ephdrop/core/identity"
)

const (
	// browseCycle is how long one browse session runs. It must be well under
	// discovery.DefaultTTL so that a live device is refreshed several times
	// before it could expire.
	browseCycle = 30 * time.Second
	// retryDelay is the pause after a failed browse session.
	retryDelay = 5 * time.Second
	// maxFailures is how many sessions in a row may fail before Browse gives up
	// and returns the error.
	maxFailures = 5
)

// Backend implements discovery.Backend with multicast DNS.
type Backend struct {
	// Interfaces limits mDNS to these network interfaces. If empty, every
	// multicast capable interface is used.
	Interfaces []net.Interface

	// Overrides for tests. Zero values mean the defaults above.
	cycle   time.Duration
	retry   time.Duration
	once    func(ctx context.Context, d time.Duration, onSeen func(discovery.Sighting)) error
	ipOK    func(netip.Addr) bool
	toSight func(txt []string, port int, ips []netip.Addr) (discovery.Sighting, error)
}

var _ discovery.Backend = (*Backend)(nil)

// domain is discovery.Domain without the trailing dot, as zeroconf expects.
var domain = strings.TrimSuffix(discovery.Domain, ".")

// Advertise announces this device until ctx ends.
func (b *Backend) Advertise(ctx context.Context, ad discovery.Advertisement) error {
	if err := ad.Validate(); err != nil {
		return err
	}
	ips := advertiseIPs(b.Interfaces, b.addrFilter())
	if len(ips) == 0 {
		return errors.New("mdns: no private network address to advertise. Are you connected to a network?")
	}
	// The host name is neutral, so the computer's real name is not published.
	host := ad.InstanceName()
	srv, err := zeroconf.RegisterProxy(ad.InstanceName(), discovery.ServiceType, domain, ad.Port, host, ips, ad.TXT(), b.Interfaces)
	if err != nil {
		return fmt.Errorf("mdns: advertise: %w", err)
	}
	defer srv.Shutdown() // also sends a goodbye for listeners that understand it
	<-ctx.Done()
	return ctx.Err()
}

// Browse reports other devices' announcements until ctx ends. onGone is not
// used: see the package comment.
func (b *Backend) Browse(ctx context.Context, onSeen func(discovery.Sighting), _ func(identity.DeviceID)) error {
	once := b.once
	if once == nil {
		once = b.browseOnce
	}
	failures := 0
	for ctx.Err() == nil {
		err := once(ctx, b.cycleLen(), onSeen)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err == nil {
			failures = 0
			continue
		}
		failures++
		if failures >= maxFailures {
			return fmt.Errorf("mdns: browsing failed %d times in a row: %w", failures, err)
		}
		if !sleep(ctx, b.retryDelay()) {
			return ctx.Err()
		}
	}
	return ctx.Err()
}

// browseOnce runs one browse session of length d. It returns nil when the
// session ends normally and an error if the library stops early.
func (b *Backend) browseOnce(ctx context.Context, d time.Duration, onSeen func(discovery.Sighting)) error {
	cctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()

	resolver, err := b.newResolver()
	if err != nil {
		return fmt.Errorf("start resolver: %w", err)
	}
	entries := make(chan *zeroconf.ServiceEntry, 32)
	// Browse returns as soon as the session has started. The library closes
	// entries when cctx ends or when it hits an error.
	if err := resolver.Browse(cctx, discovery.ServiceType, domain, entries); err != nil {
		return fmt.Errorf("start browsing: %w", err)
	}

	for {
		select {
		case e, ok := <-entries:
			if !ok {
				if cctx.Err() == nil {
					return errors.New("the library stopped browsing")
				}
				return nil
			}
			b.handle(e, onSeen)
		case <-cctx.Done():
			// Keep reading until the library closes the channel, so it never
			// blocks on a send and its goroutines can end.
			go func() {
				for range entries {
				}
			}()
			return nil
		}
	}
}

// newResolver opens the multicast sockets. The library fails outright if it
// cannot join the IPv6 group, which happens on machines and networks with IPv6
// switched off, so it falls back to IPv4 only, then IPv6 only.
func (b *Backend) newResolver() (*zeroconf.Resolver, error) {
	var errs []error
	for _, mode := range []zeroconf.IPType{zeroconf.IPv4AndIPv6, zeroconf.IPv4, zeroconf.IPv6} {
		opts := []zeroconf.ClientOption{zeroconf.SelectIPTraffic(mode)}
		if len(b.Interfaces) > 0 {
			opts = append(opts, zeroconf.SelectIfaces(b.Interfaces))
		}
		r, err := zeroconf.NewResolver(opts...)
		if err == nil {
			return r, nil
		}
		errs = append(errs, err)
	}
	return nil, errors.Join(errs...)
}

// handle turns one resolved service into a Sighting. Entries that are
// malformed or have no usable address are ignored.
func (b *Backend) handle(e *zeroconf.ServiceEntry, onSeen func(discovery.Sighting)) {
	// A time to live of zero means goodbye. The library does not deliver
	// these, but if it ever did, a goodbye is not a sighting.
	if e == nil || e.TTL == 0 {
		return
	}
	toSight := b.toSight
	if toSight == nil {
		toSight = discovery.SightingFromService
	}
	s, err := toSight(e.Text, e.Port, addrsOf(e))
	if err != nil {
		return
	}
	onSeen(s)
}

// addrsOf lists the IP addresses of a resolved service. IPv6 link-local
// addresses are skipped, because without the interface name they cannot be
// dialled.
func addrsOf(e *zeroconf.ServiceEntry) []netip.Addr {
	var out []netip.Addr
	for _, list := range [][]net.IP{e.AddrIPv4, e.AddrIPv6} {
		for _, ip := range list {
			a, ok := netip.AddrFromSlice(ip)
			if !ok {
				continue
			}
			a = a.Unmap()
			if a.Is6() && a.IsLinkLocalUnicast() {
				continue
			}
			out = append(out, a)
		}
	}
	return out
}

// advertiseIPs lists the addresses to publish: those of up, multicast capable,
// non-loopback interfaces that pass ok. By default only private addresses pass
// (see defaultAddrFilter).
func advertiseIPs(ifaces []net.Interface, ok func(netip.Addr) bool) []string {
	if len(ifaces) == 0 {
		ifaces, _ = net.Interfaces()
	}
	var out []string
	for _, ifc := range ifaces {
		if !usableInterface(ifc.Flags) {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		out = append(out, filterAddrs(addrs, ok)...)
	}
	return dedupe(out)
}

// usableInterface reports whether an interface with these flags can carry
// announcements: it must be up and multicast capable, and not loopback.
func usableInterface(f net.Flags) bool {
	const need = net.FlagUp | net.FlagMulticast
	return f&need == need && f&net.FlagLoopback == 0
}

// filterAddrs returns the IP addresses in addrs that pass ok, as strings.
func filterAddrs(addrs []net.Addr, ok func(netip.Addr) bool) []string {
	var out []string
	for _, a := range addrs {
		ipnet, isNet := a.(*net.IPNet)
		if !isNet {
			continue
		}
		ip, valid := netip.AddrFromSlice(ipnet.IP)
		if !valid {
			continue
		}
		ip = ip.Unmap()
		if ok(ip) {
			out = append(out, ip.String())
		}
	}
	return out
}

// defaultAddrFilter accepts private addresses only. That leaves out public
// addresses (not for announcing to a home network), link-local addresses (not
// reachable without an interface name) and loopback.
func defaultAddrFilter(ip netip.Addr) bool { return ip.IsPrivate() }

func (b *Backend) addrFilter() func(netip.Addr) bool {
	if b.ipOK != nil {
		return b.ipOK
	}
	return defaultAddrFilter
}

func (b *Backend) cycleLen() time.Duration {
	if b.cycle > 0 {
		return b.cycle
	}
	return browseCycle
}

func (b *Backend) retryDelay() time.Duration {
	if b.retry > 0 {
		return b.retry
	}
	return retryDelay
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// sleep waits for d and reports false if ctx ended first.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
