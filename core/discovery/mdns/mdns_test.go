package mdns

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/grandcat/zeroconf"

	"github.com/ShravanAmudala55/ephdrop/core/discovery"
	"github.com/ShravanAmudala55/ephdrop/core/identity"
)

func newID(t *testing.T) identity.DeviceID {
	t.Helper()
	i, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	return i.ID()
}

func entryFor(t *testing.T, id identity.DeviceID, port int, ttl uint32, v4 []string, v6 []string) *zeroconf.ServiceEntry {
	t.Helper()
	e := zeroconf.NewServiceEntry("ephdrop-x", discovery.ServiceType, "local")
	e.Text = discovery.Advertisement{ID: id, Port: port}.TXT()
	e.Port = port
	e.TTL = ttl
	for _, s := range v4 {
		e.AddrIPv4 = append(e.AddrIPv4, net.ParseIP(s))
	}
	for _, s := range v6 {
		e.AddrIPv6 = append(e.AddrIPv6, net.ParseIP(s))
	}
	return e
}

func collect(b *Backend, e *zeroconf.ServiceEntry) []discovery.Sighting {
	var got []discovery.Sighting
	b.handle(e, func(s discovery.Sighting) { got = append(got, s) })
	return got
}

func TestHandleBuildsASighting(t *testing.T) {
	id := newID(t)
	e := entryFor(t, id, 4000, 120, []string{"192.168.1.20"}, []string{"fe80::1", "fd00::5"})
	got := collect(&Backend{}, e)
	if len(got) != 1 || got[0].ID != id {
		t.Fatalf("got %+v", got)
	}
	// The link-local IPv6 address is left out, the others are kept in order.
	if want := "192.168.1.20:4000 [fd00::5]:4000"; strings.Join(got[0].Addrs, " ") != want {
		t.Fatalf("addrs = %v, want %s", got[0].Addrs, want)
	}
}

func TestHandleIgnoresUnusableEntries(t *testing.T) {
	id := newID(t)
	b := &Backend{}
	cases := map[string]*zeroconf.ServiceEntry{
		"nil entry":       nil,
		"goodbye":         entryFor(t, id, 4000, 0, []string{"192.168.1.20"}, nil),
		"public only":     entryFor(t, id, 4000, 120, []string{"8.8.8.8"}, []string{"2001:db8::1"}),
		"link-local only": entryFor(t, id, 4000, 120, nil, []string{"fe80::1"}),
		"no addresses":    entryFor(t, id, 4000, 120, nil, nil),
		"port zero":       entryFor(t, id, 0, 120, []string{"192.168.1.20"}, nil),
	}
	bad := entryFor(t, id, 4000, 120, []string{"192.168.1.20"}, nil)
	bad.Text = []string{"v=1", "id=not-an-id"}
	cases["bad TXT"] = bad
	newer := entryFor(t, id, 4000, 120, []string{"192.168.1.20"}, nil)
	newer.Text = []string{"v=2", "id=" + string(id)}
	cases["newer protocol"] = newer

	for name, e := range cases {
		if got := collect(b, e); len(got) != 0 {
			t.Errorf("%s: reported %+v", name, got)
		}
	}
}

func TestFilterAddrs(t *testing.T) {
	ipnet := func(s string) net.Addr { return &net.IPNet{IP: net.ParseIP(s), Mask: net.CIDRMask(24, 32)} }
	addrs := []net.Addr{
		ipnet("192.168.1.20"),
		ipnet("10.0.0.7"),
		ipnet("fd12:3456::9"),
		ipnet("8.8.8.8"),     // public
		ipnet("169.254.3.4"), // link-local
		ipnet("fe80::1"),     // link-local
		ipnet("2001:db8::1"), // public
		ipnet("127.0.0.1"),   // loopback
		&net.TCPAddr{IP: net.ParseIP("192.168.9.9"), Port: 1}, // not an interface address
	}
	got := strings.Join(filterAddrs(addrs, defaultAddrFilter), " ")
	if want := "192.168.1.20 10.0.0.7 fd12:3456::9"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if d := dedupe([]string{"a", "b", "a", "c", "b"}); strings.Join(d, "") != "abc" {
		t.Fatalf("dedupe = %v", d)
	}
}

func TestAdvertiseNeedsAUsableAddress(t *testing.T) {
	b := &Backend{ipOK: func(netip.Addr) bool { return false }}
	ad := discovery.Advertisement{ID: newID(t), Port: 4000}
	if err := b.Advertise(context.Background(), ad); err == nil {
		t.Fatal("expected an error when there is nothing to advertise")
	}
	if err := b.Advertise(context.Background(), discovery.Advertisement{ID: "bad", Port: 1}); !errors.Is(err, discovery.ErrBadAdvertisement) {
		t.Fatalf("want ErrBadAdvertisement, got %v", err)
	}
}

func fastBackend(once func(context.Context, time.Duration, func(discovery.Sighting)) error) *Backend {
	return &Backend{cycle: 20 * time.Millisecond, retry: time.Millisecond, once: once}
}

func browseAsync(ctx context.Context, b *Backend, onSeen func(discovery.Sighting)) <-chan error {
	ch := make(chan error, 1)
	go func() { ch <- b.Browse(ctx, onSeen, nil) }()
	return ch
}

func waitErr(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("Browse did not return")
		return nil
	}
}

func TestBrowseRestartsEachCycle(t *testing.T) {
	var calls atomic.Int32
	var seen atomic.Int32
	b := fastBackend(func(ctx context.Context, d time.Duration, onSeen func(discovery.Sighting)) error {
		calls.Add(1)
		onSeen(discovery.Sighting{})
		select {
		case <-time.After(d):
		case <-ctx.Done():
		}
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := browseAsync(ctx, b, func(discovery.Sighting) { seen.Add(1) })

	deadline := time.Now().Add(3 * time.Second)
	for calls.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if err := waitErr(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if calls.Load() < 3 || seen.Load() < 3 {
		t.Fatalf("only %d sessions and %d reports, want at least 3 of each", calls.Load(), seen.Load())
	}
}

func TestBrowseGivesUpAfterRepeatedFailures(t *testing.T) {
	boom := errors.New("no multicast")
	var calls atomic.Int32
	b := fastBackend(func(context.Context, time.Duration, func(discovery.Sighting)) error {
		calls.Add(1)
		return boom
	})
	err := waitErr(t, browseAsync(context.Background(), b, func(discovery.Sighting) {}))
	if !errors.Is(err, boom) {
		t.Fatalf("want the cause wrapped, got %v", err)
	}
	if calls.Load() != maxFailures {
		t.Fatalf("tried %d times, want %d", calls.Load(), maxFailures)
	}
}

func TestBrowseSurvivesOccasionalFailures(t *testing.T) {
	var calls atomic.Int32
	b := fastBackend(func(context.Context, time.Duration, func(discovery.Sighting)) error {
		// Four failures, then a success, over and over. The failure count must
		// reset on success, or this would give up after the first round.
		if n := calls.Add(1); n%int32(maxFailures) == 0 {
			return nil
		}
		return errors.New("transient")
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := browseAsync(ctx, b, func(discovery.Sighting) {})
	deadline := time.Now().Add(5 * time.Second)
	for calls.Load() < int32(4*maxFailures) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if err := waitErr(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v (calls: %d)", err, calls.Load())
	}
	if calls.Load() < int32(4*maxFailures) {
		t.Fatalf("only %d sessions ran", calls.Load())
	}
}

func TestBrowseStopsPromptlyWhileWaitingToRetry(t *testing.T) {
	var calls atomic.Int32
	b := &Backend{retry: time.Hour, once: func(context.Context, time.Duration, func(discovery.Sighting)) error {
		calls.Add(1)
		return errors.New("fail")
	}}
	ctx, cancel := context.WithCancel(context.Background())
	done := browseAsync(ctx, b, func(discovery.Sighting) {})
	for calls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	start := time.Now()
	cancel()
	if err := waitErr(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("did not stop promptly")
	}
}

func TestBrowseDoesNotStartAfterCancel(t *testing.T) {
	var calls atomic.Int32
	b := fastBackend(func(context.Context, time.Duration, func(discovery.Sighting)) error {
		calls.Add(1)
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := b.Browse(ctx, func(discovery.Sighting) {}, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if calls.Load() != 0 {
		t.Fatal("started a session although ctx was already cancelled")
	}
}

// multicastIfaces returns up, multicast capable, non-loopback interfaces that
// have an IPv4 address.
func multicastIfaces() []net.Interface {
	all, _ := net.Interfaces()
	var out []net.Interface
	for _, ifc := range all {
		const need = net.FlagUp | net.FlagMulticast
		if ifc.Flags&need != need || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			if ipnet, ok := a.(*net.IPNet); ok && ipnet.IP.To4() != nil {
				out = append(out, ifc)
				break
			}
		}
	}
	return out
}

// TestIntegrationDevicesFindEachOther runs a real announcement and a real
// browse over the network. It needs working multicast and is skipped without it.
func TestIntegrationDevicesFindEachOther(t *testing.T) {
	// GitHub's hosted macOS runners are VMs that do not deliver multicast
	// between two sockets on the same machine, so the browser never hears the
	// announcer there. The test passes on a real Mac and on Linux CI.
	if runtime.GOOS == "darwin" && os.Getenv("CI") != "" {
		t.Skip("multicast between local sockets does not work on hosted macOS CI runners")
	}
	ifaces := multicastIfaces()
	if len(ifaces) == 0 {
		t.Skip("no multicast capable network interface with an IPv4 address")
	}
	probe, err := net.ListenMulticastUDP("udp4", &ifaces[0], &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: 5353})
	if err != nil {
		t.Skipf("cannot join the mDNS multicast group here: %v", err)
	}
	probe.Close()

	// The test machine's addresses may not be private, so accept any
	// non-loopback IPv4 address on both sides.
	anyV4 := func(ip netip.Addr) bool { return ip.Is4() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() }
	skipLocalityCheck := func(txt []string, port int, ips []netip.Addr) (discovery.Sighting, error) {
		id, err := discovery.ParseTXT(txt)
		if err != nil {
			return discovery.Sighting{}, err
		}
		var addrs []string
		for _, ip := range ips {
			addrs = append(addrs, netip.AddrPortFrom(ip, uint16(port)).String())
		}
		return discovery.Sighting{ID: id, Addrs: addrs}, nil
	}

	announcer := &Backend{Interfaces: ifaces, ipOK: anyV4}
	browser := &Backend{Interfaces: ifaces, toSight: skipLocalityCheck, cycle: 3 * time.Second}

	id := newID(t)
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()

	advErr := make(chan error, 1)
	go func() { advErr <- announcer.Advertise(ctx, discovery.Advertisement{ID: id, Port: 4455}) }()
	found := make(chan discovery.Sighting, 16)
	browseErr := make(chan error, 1)
	go func() {
		browseErr <- browser.Browse(ctx, func(s discovery.Sighting) {
			if s.ID == id {
				select {
				case found <- s:
				default:
				}
			}
		}, nil)
	}()

	select {
	case s := <-found:
		ok := false
		for _, a := range s.Addrs {
			if strings.HasSuffix(a, ":4455") {
				ok = true
			}
		}
		if !ok {
			t.Fatalf("sighting has no address with the announced port: %+v", s)
		}
		t.Logf("found %s at %v after %v", s.ID.Short(), s.Addrs, time.Since(start).Round(time.Millisecond))
	case err := <-advErr:
		t.Fatalf("Advertise stopped early: %v", err)
	case err := <-browseErr:
		t.Fatalf("Browse stopped early: %v", err)
	case <-ctx.Done():
		t.Fatal("the announced device was never found, although multicast is available")
	}

	cancel()
	select {
	case err := <-advErr:
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Advertise ended with %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Advertise did not stop")
	}
}

func TestUsableInterface(t *testing.T) {
	cases := []struct {
		name  string
		flags net.Flags
		want  bool
	}{
		{"up and multicast", net.FlagUp | net.FlagMulticast, true},
		{"down", net.FlagMulticast, false},
		{"no multicast", net.FlagUp, false},
		{"loopback", net.FlagUp | net.FlagMulticast | net.FlagLoopback, false},
	}
	for _, c := range cases {
		if got := usableInterface(c.flags); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestDedupeKeepsFirstOccurrenceOrder(t *testing.T) {
	got := dedupe([]string{"a", "b", "a", "c", "b"})
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}
